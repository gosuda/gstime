package gstime

import (
	"math"
	"sort"

	"gosuda.org/gstime/clock"
	"gosuda.org/gstime/config"
	"gosuda.org/gstime/core"
	"gosuda.org/gstime/ntp"
	"gosuda.org/gstime/source"
)

type roundMeasurement struct {
	source config.SourceConfig
	result *ntp.MeasurementResult
}

type endpointEstimate struct {
	track     *source.EstimateTrack
	newestRaw core.RawNanos
	domain    string
}

func intervalMidpoint(interval core.TimeInterval) core.GstInstant {
	return interval.Earliest + core.GstInstant((uint64(interval.Latest)-uint64(interval.Earliest))/2)
}

func phaseDifference(target, current core.GstInstant) (core.PhaseNs, error) {
	difference := target - current
	overflowsUpper := target > current && difference < 0
	overflowsLower := target < current && difference > 0
	if overflowsUpper || overflowsLower {
		return 0, core.ErrOverflow
	}
	return core.PhaseNs(difference), nil
}

// The regression reference is fixed across controller updates, so feedback does
// not turn our own slew into apparent source frequency error.
func (e *SyncEngine) selectControl(reading clock.RawReading, intervals []core.TimeInterval, samples []roundMeasurement, eligible map[string]bool) (core.GstInstant, core.RateFrac, error) {
	mapping := e.svc.estimateClock.Snapshot()
	centers := make([]core.GstInstant, len(intervals))
	for i, interval := range intervals {
		centers[i] = intervalMidpoint(interval)
	}
	sort.Slice(centers, func(i, j int) bool { return centers[i] < centers[j] })
	target := centers[len(centers)/2]
	rate := core.RateFrac(0)
	if mapping.Anchored && !rawDomainChanged(e.svc.publisher.Acquire(), reading.ContinuityToken) {
		value, err := mapping.Evaluate(reading.Raw)
		if err != nil {
			return 0, 0, err
		}
		target, rate = value, mapping.BaseRate
	}
	if !e.reference.Anchored || e.referenceToken != reading.ContinuityToken || e.referenceStep != mapping.StepGeneration {
		e.reference = clock.EstimateMapping{Anchored: true, RawAnchor: reading.Raw, TimeAnchor: target}
		e.referenceToken, e.referenceStep = reading.ContinuityToken, mapping.StepGeneration
		e.estimates = make(map[string]*endpointEstimate)
	}
	for _, sample := range samples {
		m := sample.result
		if !eligible[sample.source.FaultDomainID] || !m.HasEstimate {
			continue
		}
		base, err := e.reference.Evaluate(m.RawMid)
		if err != nil {
			continue
		}
		phase, err := phaseDifference(m.Center, base)
		if err != nil {
			continue
		}
		estimate := e.estimates[sample.source.Endpoint]
		if estimate == nil {
			estimate = &endpointEstimate{track: source.NewEstimateTrack(), domain: sample.source.FaultDomainID}
			e.estimates[sample.source.Endpoint] = estimate
		}
		if err := estimate.track.AddSample(source.EstimateSample{
			RawMid: m.RawMid, PhaseErrorSlowPositive: float64(phase) / 1e9,
			RootDispersion: float64(m.NetworkBound) / 1e9,
		}); err != nil {
			continue
		}
		estimate.newestRaw = max(estimate.newestRaw, m.RawMid)
	}
	baseNow, err := e.reference.Evaluate(reading.Raw)
	if err != nil {
		return 0, 0, err
	}
	endpoints := make([]string, 0, len(e.estimates))
	for endpoint := range e.estimates {
		endpoints = append(endpoints, endpoint)
	}
	sort.Strings(endpoints)
	bestError := math.Inf(1)
	for _, endpoint := range endpoints {
		estimate := e.estimates[endpoint]
		if !eligible[estimate.domain] || estimate.newestRaw > reading.Raw || reading.Raw-estimate.newestRaw > core.RawNanos(e.cfg.Assurance.MaxAgeNs) {
			continue
		}
		fit, err := estimate.track.Fit()
		if err != nil || fit.ResidualStandardDeviation >= bestError {
			continue
		}
		phaseNs := (fit.PhaseEstimate + fit.RateErrorEstimate*float64(reading.Raw-estimate.newestRaw)/1e9) * 1e9
		if math.IsNaN(phaseNs) || math.IsInf(phaseNs, 0) || phaseNs <= float64(math.MinInt64) || phaseNs >= float64(math.MaxInt64) {
			continue
		}
		candidate, err := core.CheckedInstantAdd(baseNow, int64(math.Round(phaseNs)))
		if err != nil {
			continue
		}
		ppm := max(-core.BaseRateLimitPpmDefault, min(core.BaseRateLimitPpmDefault, fit.RateErrorEstimate*1e6))
		target, rate = candidate, core.RateFromPpmEstimate(ppm)
		bestError = fit.ResidualStandardDeviation
	}
	return target, rate, nil
}
