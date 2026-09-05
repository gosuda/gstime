package clock

import (
	"math"
	"sync/atomic"

	"github.com/gosuda/gstime/core"
)

// SmearSegment defines one polynomial segment in a smear plan (Section 5.14).
type SmearSegment struct {
	StartRaw    core.RawNanos
	DurationSec float64
	InitialRate float64 // dimensionless rate
	Wander      float64 // rate change per second
	InitialBias float64 // seconds
}

// Evaluate returns (rate, bias) at elapsed seconds t from segment start.
func (s SmearSegment) Evaluate(t float64) (rate float64, bias float64) {
	if t < 0 {
		t = 0
	}
	if t > s.DurationSec {
		t = s.DurationSec
	}
	rate = s.InitialRate + s.Wander*t
	bias = s.InitialBias + s.InitialRate*t + 0.5*s.Wander*t*t
	return rate, bias
}

// SmearPlan represents a complete planned sequence of smear segments.
type SmearPlan struct {
	Segments       []SmearSegment
	TerminalBiasNs core.PhaseNs
	ResidualNs     core.PhaseNs
}

// EvaluateBiasAt calculates smear bias in nanoseconds at raw instant r.
func (sp *SmearPlan) EvaluateBiasAt(r core.RawNanos) core.PhaseNs {
	if len(sp.Segments) == 0 {
		return 0
	}

	for i := range sp.Segments {
		seg := &sp.Segments[i]
		durNs := core.RawNanos(seg.DurationSec * 1e9)
		endRaw := seg.StartRaw + durNs
		if r <= endRaw || i == len(sp.Segments)-1 {
			t := float64(int64(r)-int64(seg.StartRaw)) / 1e9
			if t > seg.DurationSec {
				t = seg.DurationSec
			}
			_, biasSec := seg.Evaluate(t)
			return core.PhaseNs(math.Round(biasSec * 1e9))
		}
	}

	return sp.TerminalBiasNs
}

// PlanSmear constructs a bounded trapezoidal/triangular smear plan from zero initial rate (Section 5.14).
func PlanSmear(
	startRaw core.RawNanos,
	targetBiasNs core.PhaseNs,
	maxRatePpm float64,
	maxWanderPpmSec float64,
) *SmearPlan {
	return ReplanSmearFromState(startRaw, 0, 0, targetBiasNs, maxRatePpm, maxWanderPpmSec)
}

// ReplanSmearFromState implements Appendix C: smear replan from arbitrary bias and rate.
func ReplanSmearFromState(
	startRaw core.RawNanos,
	b0 float64, // initial bias in seconds
	v0 float64, // initial rate
	targetBiasNs core.PhaseNs,
	maxRatePpm float64,
	maxWanderPpmSec float64,
) *SmearPlan {
	R := maxRatePpm * 1e-6
	W := maxWanderPpmSec * 1e-6
	if R <= 0 {
		R = 100e-6
	}
	if W <= 0 {
		W = 10e-6
	}

	var segments []SmearSegment
	currRaw := startRaw
	currBias := b0
	currRate := v0

	// Step 1: If v0 != 0, append braking segment to bring rate to 0
	if math.Abs(currRate) > 1e-15 {
		brakeWander := -math.Copysign(W, currRate)
		brakeDur := math.Abs(currRate) / W

		seg := SmearSegment{
			StartRaw:    currRaw,
			DurationSec: brakeDur,
			InitialRate: currRate,
			Wander:      brakeWander,
			InitialBias: currBias,
		}
		segments = append(segments, seg)

		currRaw += core.RawNanos(brakeDur * 1e9)
		_, currBias = seg.Evaluate(brakeDur)
		currRate = 0
	}

	// Step 2 & 3: Remaining displacement from rest
	targetSec := float64(targetBiasNs) / 1e9
	D := targetSec - currBias

	if math.Abs(D) < 1e-12 {
		return &SmearPlan{
			Segments:       segments,
			TerminalBiasNs: targetBiasNs,
		}
	}

	// Step 4: Triangular or trapezoidal plan
	rampTime := R / W
	rampArea := R * rampTime

	signD := math.Copysign(1.0, D)
	if math.Abs(D) <= rampArea {
		// Triangular plan: 2 stages
		t := math.Sqrt(math.Abs(D) / W)
		seg1 := SmearSegment{
			StartRaw:    currRaw,
			DurationSec: t,
			InitialRate: 0,
			Wander:      signD * W,
			InitialBias: currBias,
		}
		segments = append(segments, seg1)
		currRaw += core.RawNanos(t * 1e9)
		r1, b1 := seg1.Evaluate(t)

		seg2 := SmearSegment{
			StartRaw:    currRaw,
			DurationSec: t,
			InitialRate: r1,
			Wander:      -signD * W,
			InitialBias: b1,
		}
		segments = append(segments, seg2)
	} else {
		// Trapezoidal plan: 3 stages
		flatTime := (math.Abs(D) - rampArea) / R

		// 1. Accel
		seg1 := SmearSegment{
			StartRaw:    currRaw,
			DurationSec: rampTime,
			InitialRate: 0,
			Wander:      signD * W,
			InitialBias: currBias,
		}
		segments = append(segments, seg1)
		currRaw += core.RawNanos(rampTime * 1e9)
		r1, b1 := seg1.Evaluate(rampTime)

		// 2. Flat
		seg2 := SmearSegment{
			StartRaw:    currRaw,
			DurationSec: flatTime,
			InitialRate: r1,
			Wander:      0,
			InitialBias: b1,
		}
		segments = append(segments, seg2)
		currRaw += core.RawNanos(flatTime * 1e9)
		r2, b2 := seg2.Evaluate(flatTime)

		// 3. Decel
		seg3 := SmearSegment{
			StartRaw:    currRaw,
			DurationSec: rampTime,
			InitialRate: r2,
			Wander:      -signD * W,
			InitialBias: b2,
		}
		segments = append(segments, seg3)
	}

	return &SmearPlan{
		Segments:       segments,
		TerminalBiasNs: targetBiasNs,
	}
}

// PublicClock handles presentation, leap smear, and linearized monotonic clamping (Section 5.13 & 7.15).
type PublicClock struct {
	lastPublic atomic.Int64
	smearPlan  atomic.Pointer[SmearPlan]
}

// NewPublicClock creates a new PublicClock instance.
func NewPublicClock() *PublicClock {
	pc := &PublicClock{}
	pc.smearPlan.Store(&SmearPlan{})
	return pc
}

// SetSmearPlan updates the active smear plan.
func (pc *PublicClock) SetSmearPlan(plan *SmearPlan) {
	pc.smearPlan.Store(plan)
}

// ReadPublicInstant evaluates the public clock with atomic-max monotonic clamp (Section 7.15).
func (pc *PublicClock) ReadPublicInstant(estimate core.GstInstant, raw core.RawNanos) (publicVal core.GstInstant, clampDebt core.PhaseNs) {
	plan := pc.smearPlan.Load()
	var smearBias core.PhaseNs
	if plan != nil {
		smearBias = plan.EvaluateBiasAt(raw)
	}

	unclamped := estimate + core.GstInstant(smearBias)
	candidate := int64(unclamped)

	for {
		old := pc.lastPublic.Load()
		returned := candidate
		if old > returned {
			returned = old
		}

		if pc.lastPublic.CompareAndSwap(old, returned) {
			debt := returned - candidate
			return core.GstInstant(returned), core.PhaseNs(debt)
		}
	}
}

// ComputePublicSymmetricEpsilon calculates epsilon including smear and clamp debt (Section 7.8).
func ComputePublicSymmetricEpsilon(
	p core.GstInstant,
	interval *core.TimeInterval,
	readBound core.ErrorNs,
) core.ErrorNs {
	if interval == nil {
		return core.ErrorInfinity
	}

	d1 := math.Abs(float64(p - interval.Earliest))
	d2 := math.Abs(float64(interval.Latest - p))
	maxDist := math.Max(d1, d2)

	return core.ErrorNs(maxDist) + readBound
}
