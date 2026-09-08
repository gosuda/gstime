package ntp

import (
	"errors"
	"math"

	"gosuda.org/gstime/core"
)

var (
	ErrNonChronologicalTimes    = errors.New("receive timestamp earlier than send timestamp")
	ErrServerTimeReversal       = errors.New("server transmit time earlier than receive time")
	ErrStratumInvalid           = errors.New("stratum must be between 1 and 15")
	ErrRootDistanceExceeded     = errors.New("root distance exceeded maximum cap")
	errInvalidRawScale          = errors.New("raw scale envelope must be positive and ordered")
	errInvalidMeasurementError  = errors.New("measurement errors and limits must be non-negative")
	errServerTurnaroundExceeded = errors.New("server turnaround time exceeded limit")
	errInconsistentMeasurement  = errors.New("server timestamps and raw exchange have no feasible time interval")
)

// MeasurementInput holds inputs for constructing an NTP sample.
type MeasurementInput struct {
	T1LocalSendEstimate core.GstInstant
	T2ServerRecv        core.GstInstant
	T3ServerTx          core.GstInstant
	T4LocalRecvEstimate core.GstInstant
	LocalSendRaw        core.RawNanos
	LocalRecvRaw        core.RawNanos
	// Required SI nanoseconds per raw nanosecond throughout the acquisition envelope.
	RawScaleLower               core.RateScale
	RawScaleUpper               core.RateScale
	LocalEstimateAtMid          core.GstInstant
	ServerRootDispersionNs      int64
	ServerRootDelayNs           int64
	LocalSendReadError          core.ErrorNs // Raw-coordinate error, scaled by RawScaleUpper.
	LocalRecvReadError          core.ErrorNs // Raw-coordinate error, scaled by RawScaleUpper.
	RemoteTimestampQuantization core.ErrorNs // SI error bound on each server timestamp.
	LocalMappingIntegrationErr  core.ErrorNs // Statistical center only; never shifts or narrows HardInterval.
	StaticAsymmetryCorrection   int64        // Shifts statistical theta only.
	StaticAsymmetryUncertainty  core.ErrorNs // Additional SI uncertainty on each hard endpoint.
	ProtocolConversionError     core.ErrorNs // SI conversion error on each server timestamp.
	PrecisionFloorNs            int64
	MaxServerTurnaroundNs       int64
	MaxRootDistanceNs           int64
}

// MeasurementResult contains the calculated phase offset, delays, and hard interval.
type MeasurementResult struct {
	// HasEstimate distinguishes computed statistics from custom hard-only measurements.
	HasEstimate       bool
	ThetaSlowPositive int64 // phase error: reference - local
	DeltaRaw          int64 // round trip delay before clamp
	DeltaClamped      int64 // round trip delay clamped to precision floor
	RawMid            core.RawNanos
	// RawMidReadBound is zero for ComputeMeasurement: its synthetic integer
	// coordinate is exact and endpoint read errors are already in HardInterval.
	// Custom SourceQueriers must bound any uncertainty in their anchor coordinate.
	RawMidReadBound core.ErrorNs
	Center          core.GstInstant // Statistical center; need not be the hard interval midpoint.
	NetworkBound    int64           // SI radius about Center, including statistical mapping error.
	HardInterval    core.TimeInterval
}

// ComputeMeasurement keeps estimate-based statistics separate from raw-clock hard bounds.
func ComputeMeasurement(in MeasurementInput) (*MeasurementResult, error) {
	if in.RawScaleLower <= 0 || in.RawScaleUpper < in.RawScaleLower {
		return nil, errInvalidRawScale
	}
	if in.ServerRootDispersionNs < 0 || in.ServerRootDelayNs < 0 ||
		in.PrecisionFloorNs < 0 || in.MaxServerTurnaroundNs < 0 || in.MaxRootDistanceNs < 0 {
		return nil, errInvalidMeasurementError
	}
	for _, bound := range [...]core.ErrorNs{
		in.LocalSendReadError, in.LocalRecvReadError, in.RemoteTimestampQuantization,
		in.LocalMappingIntegrationErr, in.StaticAsymmetryUncertainty, in.ProtocolConversionError,
	} {
		if bound > math.MaxInt64 {
			return nil, core.ErrOverflow
		}
	}
	if in.LocalRecvRaw < in.LocalSendRaw {
		return nil, ErrNonChronologicalTimes
	}
	if in.T3ServerTx < in.T2ServerRecv {
		return nil, ErrServerTimeReversal
	}
	serverTurnaround, err := measurementDifference(int64(in.T3ServerTx), int64(in.T2ServerRecv))
	if err != nil {
		return nil, err
	}
	if in.MaxServerTurnaroundNs > 0 && serverTurnaround > in.MaxServerTurnaroundNs {
		return nil, errServerTurnaroundExceeded
	}
	rootDist, err := core.CheckedInstantAdd(core.GstInstant(in.ServerRootDispersionNs), in.ServerRootDelayNs/2+in.ServerRootDelayNs%2)
	if err != nil {
		return nil, err
	}
	if in.MaxRootDistanceNs > 0 && int64(rootDist) > in.MaxRootDistanceNs {
		return nil, ErrRootDistanceExceeded
	}

	thetaRecv, err := measurementDifference(int64(in.T2ServerRecv), int64(in.T1LocalSendEstimate))
	if err != nil {
		return nil, err
	}
	thetaTx, err := measurementDifference(int64(in.T3ServerTx), int64(in.T4LocalRecvEstimate))
	if err != nil {
		return nil, err
	}
	thetaSum, err := core.CheckedInstantAdd(core.GstInstant(thetaRecv), thetaTx)
	if err != nil {
		return nil, err
	}
	theta, err := core.CheckedInstantAdd(thetaSum/2, in.StaticAsymmetryCorrection)
	if err != nil {
		return nil, err
	}
	estimateSpan, err := measurementDifference(int64(in.T4LocalRecvEstimate), int64(in.T1LocalSendEstimate))
	if err != nil {
		return nil, err
	}
	deltaRaw, err := measurementDifference(estimateSpan, serverTurnaround)
	if err != nil {
		return nil, err
	}
	deltaClamped := deltaRaw
	if in.PrecisionFloorNs > 0 {
		deltaClamped = max(deltaRaw, in.PrecisionFloorNs)
	}
	center, err := core.CheckedInstantAdd(in.LocalEstimateAtMid, int64(theta))
	if err != nil {
		return nil, err
	}
	rawSpanUnsigned := in.LocalRecvRaw - in.LocalSendRaw
	if rawSpanUnsigned > math.MaxInt64 {
		return nil, core.ErrOverflow
	}
	rawSpan := int64(rawSpanUnsigned)
	// Both halves are explicit: an odd span does not lose half a raw nanosecond.
	sendHalf := rawSpan / 2
	recvHalf := rawSpan - sendHalf
	rawMid := in.LocalSendRaw + core.RawNanos(sendHalf) // Between ordered endpoints.
	hardInterval, err := acquisitionInterval(in, sendHalf, recvHalf, int64(rootDist))
	if err != nil {
		return nil, err
	}
	netBound := core.SymmetricEpsilon(center, hardInterval, in.LocalMappingIntegrationErr)
	if netBound > math.MaxInt64 {
		return nil, core.ErrOverflow
	}
	return &MeasurementResult{
		HasEstimate:       true,
		ThetaSlowPositive: int64(theta),
		DeltaRaw:          deltaRaw,
		DeltaClamped:      deltaClamped,
		RawMid:            rawMid,
		Center:            center,
		NetworkBound:      int64(netBound),
		HardInterval:      hardInterval,
	}, nil
}

func acquisitionInterval(in MeasurementInput, sendHalf, recvHalf, rootDistance int64) (core.TimeInterval, error) {
	sendDistance, err := core.CheckedInstantAdd(core.GstInstant(sendHalf), int64(in.LocalSendReadError))
	if err != nil {
		return core.TimeInterval{}, err
	}
	recvDistance, err := core.CheckedInstantAdd(core.GstInstant(recvHalf), int64(in.LocalRecvReadError))
	if err != nil {
		return core.TimeInterval{}, err
	}
	sendElapsed, err := core.MulScaleDurationCeil(in.RawScaleUpper, core.RawNanos(sendDistance))
	if err != nil {
		return core.TimeInterval{}, err
	}
	recvElapsed, err := core.MulScaleDurationCeil(in.RawScaleUpper, core.RawNanos(recvDistance))
	if err != nil {
		return core.TimeInterval{}, err
	}
	remoteError := core.GstInstant(rootDistance)
	for _, bound := range [...]core.ErrorNs{
		in.RemoteTimestampQuantization, in.ProtocolConversionError, in.StaticAsymmetryUncertainty,
	} {
		remoteError, err = core.CheckedInstantAdd(remoteError, int64(bound))
		if err != nil {
			return core.TimeInterval{}, err
		}
	}

	// S0=r1-sendError and R0=r4+recvError bracket the actual client events
	// and synthetic midpoint M. C(S0) <= true(T2), true(T3) <= C(R0),
	// and dC/dr <= U imply T3-e-U*(R0-M) <= C(M) <= T2+e+U*(M-S0).
	// This covers every server-event position, including full turnaround and
	// one-sided delay. A lower scale cannot tighten unrestricted path asymmetry.
	lower, err := core.CheckedInstantAdd(in.T3ServerTx, -recvElapsed)
	if err != nil {
		return core.TimeInterval{}, err
	}
	lower, err = core.CheckedInstantAdd(lower, -int64(remoteError))
	if err != nil {
		return core.TimeInterval{}, err
	}
	upper, err := core.CheckedInstantAdd(in.T2ServerRecv, sendElapsed)
	if err != nil {
		return core.TimeInterval{}, err
	}
	upper, err = core.CheckedInstantAdd(upper, int64(remoteError))
	if err != nil {
		return core.TimeInterval{}, err
	}
	if lower > upper {
		return core.TimeInterval{}, errInconsistentMeasurement
	}
	return core.TimeInterval{Earliest: lower, Latest: upper}, nil
}

func measurementDifference(a, b int64) (int64, error) {
	overflowsLower := b > 0 && a < math.MinInt64+b
	overflowsUpper := b < 0 && a > math.MaxInt64+b
	if overflowsLower || overflowsUpper {
		return 0, core.ErrOverflow
	}
	return a - b, nil
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
