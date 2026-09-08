package clock

import (
	"errors"
	"math"
	"math/bits"
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

var (
	errInvalidBaseRate           = errors.New("estimate rate must be positive")
	errInvalidMappingChange      = errors.New("continuous reanchor requires a continuous change kind")
	errMappingHistoryUnavailable = errors.New("raw stamp is outside retained mapping history")
	errMappingBackwards          = errors.New("mapping change precedes the active raw anchor")
)

const mappingHistoryCapacity = 256

// EstimateMapping represents the epoch-anchored affine mapping (Section 5.3).
type EstimateMapping struct {
	Anchored          bool
	RawAnchor         core.RawNanos
	TimeAnchor        core.GstInstant
	BaseRate          core.RateFrac
	PhaseDebt         core.PhaseNs
	SlewRate          core.RateFrac
	SlewStartRaw      core.RawNanos
	MappingGeneration core.Generation
	StepGeneration    core.Generation
	LastChangeKind    MappingChangeKind
}

// SlewApplied calculates the phase correction applied up to raw instant r.
func (m *EstimateMapping) SlewApplied(r core.RawNanos) int64 {
	if m.PhaseDebt == 0 || r <= m.SlewStartRaw || m.SlewRate == 0 ||
		(m.PhaseDebt < 0) != (m.SlewRate < 0) {
		return 0
	}
	negative := m.PhaseDebt < 0
	rate, debt := uint64(m.SlewRate), uint64(m.PhaseDebt)
	if negative {
		rate, debt = -rate, -debt
	}
	hi, lo := bits.Mul64(rate, uint64(r-m.SlewStartRaw))
	if hi >= 1<<core.FracBits {
		return int64(m.PhaseDebt)
	}
	applied := hi<<(64-core.FracBits) | lo>>core.FracBits
	if applied >= debt {
		return int64(m.PhaseDebt)
	}
	if negative {
		if lo&((1<<core.FracBits)-1) != 0 {
			applied++
		}
		return int64(-applied)
	}
	return int64(applied)
}

// Evaluate maps raw nanoseconds to GstInstant continuous axis (Section 5.3).
func (m *EstimateMapping) Evaluate(r core.RawNanos) (core.GstInstant, error) {
	if !m.Anchored {
		return 0, core.ErrUnanchored
	}
	dr, err := rawDuration(r, m.RawAnchor)
	if err != nil {
		return 0, err
	}
	scale, err := estimateScale(m.BaseRate)
	if err != nil {
		return 0, err
	}
	baseAdvance, err := core.MulRateDurationFloor(scale, dr)
	if err != nil {
		return 0, err
	}
	base, err := core.CheckedInstantAdd(m.TimeAnchor, baseAdvance)
	if err != nil {
		return 0, err
	}
	return core.CheckedInstantAdd(base, m.SlewApplied(r))
}

func rawDuration(raw, anchor core.RawNanos) (core.DurationNs, error) {
	if raw >= anchor {
		delta := uint64(raw - anchor)
		if delta > math.MaxInt64 {
			return 0, core.ErrOverflow
		}
		return core.DurationNs(delta), nil
	}
	delta := uint64(anchor - raw)
	if delta > uint64(math.MaxInt64)+1 {
		return 0, core.ErrOverflow
	}
	return core.DurationNs(-delta), nil
}

func estimateScale(rate core.RateFrac) (core.RateFrac, error) {
	scale, err := core.CheckedInstantAdd(core.GstInstant(core.OneQ48), int64(rate))
	if err != nil {
		return 0, err
	}
	if scale <= 0 {
		return 0, errInvalidBaseRate
	}
	return core.RateFrac(scale), nil
}

// EstimateClock maintains the active estimate mapping with thread-safe atomic transitions.
type EstimateClock struct {
	mu           sync.RWMutex
	mapping      EstimateMapping
	history      [mappingHistoryCapacity]EstimateMapping
	historyStart int
	historyCount int
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
func (c *EstimateClock) InitializeAnchors(raw0 core.RawNanos, target core.GstInstant, baseRate core.RateFrac) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if _, err := estimateScale(baseRate); err != nil {
		return err
	}
	if c.mapping.MappingGeneration == math.MaxUint64 || c.mapping.StepGeneration == math.MaxUint64 {
		return core.ErrOverflow
	}
	kind := ChangeRateReanchor
	if c.mapping.Anchored {
		kind = ChangePhaseStep
	}
	c.install(EstimateMapping{
		Anchored:          true,
		RawAnchor:         raw0,
		TimeAnchor:        target,
		BaseRate:          baseRate,
		SlewStartRaw:      raw0,
		MappingGeneration: c.mapping.MappingGeneration + 1,
		StepGeneration:    c.mapping.StepGeneration + 1,
		LastChangeKind:    kind,
	})
	return nil
}

// Snapshot returns the current immutable EstimateMapping.
func (c *EstimateClock) Snapshot() EstimateMapping {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.mapping
}

// MappingAt resolves acquisition stamps; evicted epochs cannot be extrapolated.
func (c *EstimateClock) MappingAt(raw core.RawNanos) (EstimateMapping, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if !c.mapping.Anchored {
		return EstimateMapping{}, core.ErrUnanchored
	}
	if raw >= c.mapping.RawAnchor {
		return c.mapping, nil
	}
	for i := c.historyCount - 1; i >= 0; i-- {
		mapping := c.history[(c.historyStart+i)%len(c.history)]
		if raw >= mapping.RawAnchor {
			return mapping, nil
		}
	}
	return EstimateMapping{}, errMappingHistoryUnavailable
}

func (c *EstimateClock) install(mapping EstimateMapping) {
	if c.mapping.Anchored && mapping.RawAnchor >= c.mapping.RawAnchor {
		index := (c.historyStart + c.historyCount) % len(c.history)
		c.history[index] = c.mapping
		if c.historyCount < len(c.history) {
			c.historyCount++
		} else {
			c.historyStart = (c.historyStart + 1) % len(c.history)
		}
	} else {
		c.historyStart, c.historyCount = 0, 0
	}
	c.mapping = mapping
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

	if changeKind != ChangeRateReanchor && changeKind != ChangeSlewReplan && changeKind != ChangePublicOnly {
		return 0, errInvalidMappingChange
	}
	if _, err := estimateScale(newBaseRate); err != nil {
		return 0, err
	}
	if cRaw < c.mapping.RawAnchor {
		return 0, errMappingBackwards
	}
	if c.mapping.MappingGeneration == math.MaxUint64 {
		return 0, core.ErrOverflow
	}
	oldValue, err := c.mapping.Evaluate(cRaw)
	if err != nil {
		return 0, err
	}

	// SlewApplied is bounded by the signed debt, so this subtraction is exact.
	remainingDebt := int64(c.mapping.PhaseDebt) - c.mapping.SlewApplied(cRaw)
	debt, err := core.CheckedInstantAdd(core.GstInstant(remainingDebt), int64(addedDebt))
	if err != nil {
		return 0, err
	}
	newDebt := core.PhaseNs(debt)
	c.install(EstimateMapping{
		Anchored:          true,
		RawAnchor:         cRaw,
		TimeAnchor:        oldValue,
		BaseRate:          newBaseRate,
		PhaseDebt:         newDebt,
		SlewRate:          planSlewRate(newDebt),
		SlewStartRaw:      cRaw,
		MappingGeneration: c.mapping.MappingGeneration + 1,
		StepGeneration:    c.mapping.StepGeneration,
		LastChangeKind:    changeKind,
	})
	return oldValue, nil
}

// Step performs an explicit discontinuous phase step (Section 5.9).
func (c *EstimateClock) Step(cRaw core.RawNanos, step core.PhaseNs) (core.GstInstant, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if cRaw < c.mapping.RawAnchor {
		return 0, errMappingBackwards
	}
	if c.mapping.MappingGeneration == math.MaxUint64 || c.mapping.StepGeneration == math.MaxUint64 {
		return 0, core.ErrOverflow
	}
	oldValue, err := c.mapping.Evaluate(cRaw)
	if err != nil {
		return 0, err
	}
	newValue, err := core.CheckedInstantAdd(oldValue, int64(step))
	if err != nil {
		return 0, err
	}
	remainingDebt := core.PhaseNs(int64(c.mapping.PhaseDebt) - c.mapping.SlewApplied(cRaw))
	c.install(EstimateMapping{
		Anchored:          true,
		RawAnchor:         cRaw,
		TimeAnchor:        newValue,
		BaseRate:          c.mapping.BaseRate,
		PhaseDebt:         remainingDebt,
		SlewRate:          planSlewRate(remainingDebt),
		SlewStartRaw:      cRaw,
		MappingGeneration: c.mapping.MappingGeneration + 1,
		StepGeneration:    c.mapping.StepGeneration + 1,
		LastChangeKind:    ChangePhaseStep,
	})
	return newValue, nil
}

// planSlewRate calculates bounded slew rate with maxSlewRate clamp (Section 5.7).
func planSlewRate(debt core.PhaseNs) core.RateFrac {
	maxSlewQ48 := core.OneQ48 / 12
	desiredRate := float64(debt) / 10e9
	maxRate := float64(maxSlewQ48) / float64(core.OneQ48)
	if desiredRate >= maxRate {
		return maxSlewQ48
	}
	if desiredRate <= -maxRate {
		return -maxSlewQ48
	}
	return core.RateFrac(desiredRate * float64(core.OneQ48))
}
