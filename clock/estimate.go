package clock

import (
	"sync"

	"gosuda.org/gstime/core"
)

type MappingChangeKind uint8

const (
	ChangeRateReanchor MappingChangeKind = iota
	ChangeSlewReplan
	ChangePhaseStep
	ChangeRawDiscontinuity
	ChangePublicOnly
)

// EstimateMapping represents the epoch-anchored affine mapping (Section 5.3).
type EstimateMapping struct {
	RawAnchor         core.RawNanos
	TimeAnchor        core.GstInstant
	BaseRate          core.RateFrac
	PhaseDebt         core.PhaseNs
	SlewRate          core.RateFrac
	SlewStartRaw      core.RawNanos
	MappingGeneration core.Generation
	LastChangeKind    MappingChangeKind
}

// SlewApplied calculates the phase correction applied up to raw instant r.
func (m *EstimateMapping) SlewApplied(r core.RawNanos) int64 {
	if m.PhaseDebt == 0 || r <= m.SlewStartRaw {
		return 0
	}
	slewDr := core.DurationNs(r - m.SlewStartRaw)
	reqApplied, err := core.MulRateDurationFloor(m.SlewRate, slewDr)
	if err != nil {
		reqApplied = int64(m.PhaseDebt)
	}

	if m.PhaseDebt > 0 {
		if reqApplied < 0 {
			reqApplied = 0
		}
		if reqApplied > int64(m.PhaseDebt) {
			reqApplied = int64(m.PhaseDebt)
		}
		return reqApplied
	}

	// Negative debt
	if reqApplied > 0 {
		reqApplied = 0
	}
	if reqApplied < int64(m.PhaseDebt) {
		reqApplied = int64(m.PhaseDebt)
	}
	return reqApplied
}

// Evaluate maps raw nanoseconds to GstInstant continuous axis (Section 5.3).
func (m *EstimateMapping) Evaluate(r core.RawNanos) (core.GstInstant, error) {
	dr := int64(r) - int64(m.RawAnchor)

	var baseAdvance int64
	var err error
	if dr >= 0 {
		baseAdvance, err = core.MulScaleDurationFloor(core.RateScale(core.OneQ48+m.BaseRate), core.RawNanos(dr))
	} else {
		baseAdvance, err = core.MulRateDurationFloor(m.BaseRate, core.DurationNs(dr))
		baseAdvance += dr
	}
	if err != nil {
		return 0, err
	}

	base := m.TimeAnchor + core.GstInstant(baseAdvance)
	slew := m.SlewApplied(r)

	return base + core.GstInstant(slew), nil
}

// EstimateClock maintains the active estimate mapping with thread-safe atomic transitions.
type EstimateClock struct {
	mu      sync.RWMutex
	mapping EstimateMapping
}

// NewEstimateClock creates an unanchored EstimateClock.
func NewEstimateClock() *EstimateClock {
	return &EstimateClock{
		mapping: EstimateMapping{
			MappingGeneration: 1,
			LastChangeKind:    ChangeRateReanchor,
		},
	}
}

// InitializeAnchors establishes the initial epoch anchor (Section 5.4).
func (c *EstimateClock) InitializeAnchors(raw0 core.RawNanos, target core.GstInstant, baseRate core.RateFrac) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.mapping = EstimateMapping{
		RawAnchor:         raw0,
		TimeAnchor:        target,
		BaseRate:          baseRate,
		PhaseDebt:         0,
		SlewRate:          0,
		SlewStartRaw:      raw0,
		MappingGeneration: c.mapping.MappingGeneration + 1,
		LastChangeKind:    ChangeRateReanchor,
	}
}

// Snapshot returns the current immutable EstimateMapping.
func (c *EstimateClock) Snapshot() EstimateMapping {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.mapping
}

// Evaluate reads the estimate clock at raw instant r.
func (c *EstimateClock) Evaluate(r core.RawNanos) (core.GstInstant, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.mapping.Evaluate(r)
}

// ReanchorContinuity updates base rate or slew while preserving exact continuity at c (Section 5.5).
func (c *EstimateClock) ReanchorContinuity(
	cRaw core.RawNanos,
	newBaseRate core.RateFrac,
	addedDebt core.PhaseNs,
	changeKind MappingChangeKind,
) (core.GstInstant, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	oldValue, err := c.mapping.Evaluate(cRaw)
	if err != nil {
		return 0, err
	}

	appliedSlew := c.mapping.SlewApplied(cRaw)
	remainingDebt := core.PhaseNs(int64(c.mapping.PhaseDebt) - appliedSlew)
	newDebt := remainingDebt + addedDebt

	// Plan new slew rate for newDebt (Section 5.7)
	newSlewRate := planSlewRate(newDebt)

	c.mapping = EstimateMapping{
		RawAnchor:         cRaw,
		TimeAnchor:        oldValue,
		BaseRate:          newBaseRate,
		PhaseDebt:         newDebt,
		SlewRate:          newSlewRate,
		SlewStartRaw:      cRaw,
		MappingGeneration: c.mapping.MappingGeneration + 1,
		LastChangeKind:    changeKind,
	}

	return oldValue, nil
}

// Step performs an explicit discontinuous phase step (Section 5.9).
func (c *EstimateClock) Step(cRaw core.RawNanos, step core.PhaseNs) (core.GstInstant, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	oldValue, err := c.mapping.Evaluate(cRaw)
	if err != nil {
		return 0, err
	}

	c.mapping = EstimateMapping{
		RawAnchor:         cRaw,
		TimeAnchor:        oldValue + core.GstInstant(step),
		BaseRate:          c.mapping.BaseRate,
		PhaseDebt:         0,
		SlewRate:          0,
		SlewStartRaw:      cRaw,
		MappingGeneration: c.mapping.MappingGeneration + 1,
		LastChangeKind:    ChangePhaseStep,
	}

	return oldValue + core.GstInstant(step), nil
}

// planSlewRate calculates bounded slew rate with maxSlewRate clamp (Section 5.7).
func planSlewRate(debt core.PhaseNs) core.RateFrac {
	if debt == 0 {
		return 0
	}
	maxSlewQ48 := core.OneQ48 / 12

	// Plan nominal duration = 10 seconds
	durSec := 10.0
	desiredRate := float64(debt) / (durSec * 1e9)
	desiredQ48 := core.RateFrac(int64(desiredRate * float64(core.OneQ48)))

	if desiredQ48 > maxSlewQ48 {
		return maxSlewQ48
	}
	if desiredQ48 < -maxSlewQ48 {
		return -maxSlewQ48
	}
	return desiredQ48
}
