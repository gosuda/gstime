package ntp

import (
	"errors"
	"math"

	"gosuda.org/gstime/core"
)

var (
	ErrNonChronologicalTimes = errors.New("receive timestamp earlier than send timestamp")
	ErrServerTimeReversal    = errors.New("server transmit time earlier than receive time")
	ErrStratumInvalid        = errors.New("stratum must be between 1 and 15")
	ErrRootDistanceExceeded  = errors.New("root distance exceeded maximum cap")
)

// MeasurementInput holds inputs for constructing an NTP sample.
type MeasurementInput struct {
	T1LocalSendEstimate         core.GstInstant
	T2ServerRecv                core.GstInstant
	T3ServerTx                  core.GstInstant
	T4LocalRecvEstimate         core.GstInstant
	LocalSendRaw                core.RawNanos
	LocalRecvRaw                core.RawNanos
	LocalEstimateAtMid          core.GstInstant
	ServerRootDispersionNs      int64
	ServerRootDelayNs           int64
	LocalSendReadError          core.ErrorNs
	LocalRecvReadError          core.ErrorNs
	RemoteTimestampQuantization core.ErrorNs
	LocalMappingIntegrationErr  core.ErrorNs
	StaticAsymmetryCorrection   int64 // Shifts theta
	StaticAsymmetryUncertainty  core.ErrorNs
	ProtocolConversionError     core.ErrorNs
	PrecisionFloorNs            int64
	MaxServerTurnaroundNs       int64
	MaxRootDistanceNs           int64
}

// MeasurementResult contains the calculated phase offset, delays, and hard interval.
type MeasurementResult struct {
	ThetaSlowPositive int64 // phase error: reference - local
	DeltaRaw          int64 // round trip delay before clamp
	DeltaClamped      int64 // round trip delay clamped to precision floor
	RawMid            core.RawNanos
	// RawMidReadBound bounds the raw-coordinate error at RawMid, independently
	// of HardInterval's absolute-time uncertainty. Zero asserts an exact raw
	// reference; custom SourceQueriers must provide their acquisition bound.
	RawMidReadBound core.ErrorNs
	Center          core.GstInstant
	NetworkBound    int64
	HardInterval    core.TimeInterval
}

// ComputeMeasurement processes the four-timestamp exchange according to Sections 2.6, 2.11, and 2.12.
func ComputeMeasurement(in MeasurementInput) (*MeasurementResult, error) {
	if in.LocalRecvRaw < in.LocalSendRaw {
		return nil, ErrNonChronologicalTimes
	}
	if in.T3ServerTx < in.T2ServerRecv {
		return nil, ErrServerTimeReversal
	}

	serverTurnaround := int64(in.T3ServerTx - in.T2ServerRecv)
	if in.MaxServerTurnaroundNs > 0 && serverTurnaround > in.MaxServerTurnaroundNs {
		return nil, errors.New("server turnaround time exceeded limit")
	}

	rootDist := in.ServerRootDispersionNs + in.ServerRootDelayNs/2
	if in.MaxRootDistanceNs > 0 && rootDist > in.MaxRootDistanceNs {
		return nil, ErrRootDistanceExceeded
	}

	// theta = ((T2 - T1) + (T3 - T4)) / 2 (slow-positive)
	t1 := int64(in.T1LocalSendEstimate)
	t2 := int64(in.T2ServerRecv)
	t3 := int64(in.T3ServerTx)
	t4 := int64(in.T4LocalRecvEstimate)

	theta := ((t2 - t1) + (t3 - t4)) / 2
	theta += in.StaticAsymmetryCorrection

	// delta_raw = (T4 - T1) - (T3 - T2)
	deltaRaw := (t4 - t1) - (t3 - t2)
	deltaClamped := deltaRaw
	if in.PrecisionFloorNs > 0 && deltaClamped < in.PrecisionFloorNs {
		deltaClamped = in.PrecisionFloorNs
	}

	// Endpoint read errors also bound the midpoint's raw coordinate. Using
	// their maximum is conservative; an odd raw span needs one extra nanosecond
	// for the floored integer midpoint. Do not substitute a later clock read.
	rawMidReadBound := max(in.LocalSendReadError, in.LocalRecvReadError)
	rounding := core.ErrorNs((in.LocalRecvRaw - in.LocalSendRaw) & 1)
	if rawMidReadBound > math.MaxInt64-rounding ||
		in.LocalSendReadError > math.MaxInt64-in.LocalRecvReadError {
		return nil, core.ErrOverflow
	}
	rawMidReadBound += rounding
	rawMid := in.LocalSendRaw + (in.LocalRecvRaw-in.LocalSendRaw)/2
	center := in.LocalEstimateAtMid + core.GstInstant(theta)

	netBound := in.ServerRootDispersionNs +
		in.ServerRootDelayNs/2 +
		deltaClamped/2 +
		int64(in.LocalSendReadError) +
		int64(in.LocalRecvReadError) +
		int64(in.RemoteTimestampQuantization) +
		int64(in.LocalMappingIntegrationErr) +
		int64(in.StaticAsymmetryUncertainty) +
		int64(in.ProtocolConversionError)

	if netBound < 0 {
		netBound = 0
	}

	hardInterval := core.TimeInterval{
		Earliest: center - core.GstInstant(netBound),
		Latest:   center + core.GstInstant(netBound),
	}

	return &MeasurementResult{
		ThetaSlowPositive: theta,
		DeltaRaw:          deltaRaw,
		DeltaClamped:      deltaClamped,
		RawMid:            rawMid,
		RawMidReadBound:   rawMidReadBound,
		Center:            center,
		NetworkBound:      netBound,
		HardInterval:      hardInterval,
	}, nil
}

// IsKissOfDeath checks if a packet is a Stratum-0 Kiss-o'-Death packet.
func IsKissOfDeath(p *Packet) (bool, string) {
	if p.Stratum != 0 {
		return false, ""
	}
	code := string(p.RefID[:])
	return true, code
}

// CheckGateC checks the estimate-path step-escape gate (Section 2.13).
// abs(measuredOffsetSlowPositive + predictedOffsetFastPositive) - delayAnomaly > allowedInnovation
func CheckGateC(measuredSlowPos, predictedFastPos, delayAnomaly, allowedInnovation float64) bool {
	diff := math.Abs(measuredSlowPos+predictedFastPos) - delayAnomaly
	return diff > allowedInnovation
}
