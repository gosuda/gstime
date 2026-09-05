package clock

import (
	"math"
	"math/rand/v2"
	"testing"

	"github.com/gosuda/gstime/core"
)

func TestReanchorContinuityRandomized(t *testing.T) {
	// Section 9.7: Verify exact re-anchor continuity E_before(c) == E_after(c)
	// under randomized base rate changes and debt accruals.
	clk := NewEstimateClock()
	raw0 := core.RawNanos(5_000_000_000)
	time0 := core.GstInstant(1_700_000_000 * 1_000_000_000)
	clk.InitializeAnchors(raw0, time0, 0)

	currRaw := raw0

	for i := 0; i < 100_000; i++ {
		// Advance raw by 0 to 1,000,000,000 ns
		advance := core.RawNanos(rand.Uint64N(1_000_000_000))
		currRaw += advance

		valBefore, err := clk.Evaluate(currRaw)
		if err != nil {
			t.Fatalf("Evaluate before failed: %v", err)
		}

		// Random new rate between -400 ppm and +400 ppm
		ppm := -400.0 + rand.Float64()*800.0
		newRate := core.RateFromPpmEstimate(ppm)

		// Random debt between -10,000 ns and +10,000 ns
		addedDebt := core.PhaseNs(rand.Int64N(20_000) - 10_000)

		valReanchor, err := clk.ReanchorContinuity(currRaw, newRate, addedDebt, ChangeRateReanchor)
		if err != nil {
			t.Fatalf("ReanchorContinuity failed: %v", err)
		}

		valAfter, err := clk.Evaluate(currRaw)
		if err != nil {
			t.Fatalf("Evaluate after failed: %v", err)
		}

		// Invariant: E_before(c) == E_after(c) bit-exact!
		if valBefore != valAfter {
			t.Fatalf("continuity violated at iter %d: before=%d, after=%d, diff=%d",
				i, valBefore, valAfter, valAfter-valBefore)
		}
		if valBefore != valReanchor {
			t.Fatalf("returned reanchor value mismatch: before=%d, reanchor=%d", valBefore, valReanchor)
		}
	}
}

func TestDebtAccrualNoInstantStep(t *testing.T) {
	// Section 1.8 invariant 3 & P2: adding phase debt alone never changes clock reading at same raw instant
	clk := NewEstimateClock()
	raw0 := core.RawNanos(10_000_000_000)
	time0 := core.GstInstant(1_700_000_000 * 1_000_000_000)
	clk.InitializeAnchors(raw0, time0, 0)

	val0, _ := clk.Evaluate(raw0)

	// Accrue huge phase debt: +500,000,000 ns (500 ms)
	_, err := clk.ReanchorContinuity(raw0, 0, 500_000_000, ChangeSlewReplan)
	if err != nil {
		t.Fatalf("ReanchorContinuity failed: %v", err)
	}

	valAfterDebt, _ := clk.Evaluate(raw0)
	if val0 != valAfterDebt {
		t.Fatalf("clock stepped on debt accrual: before=%d after=%d", val0, valAfterDebt)
	}

	// But as raw advances, the slew applies phase debt
	raw1 := raw0 + 1_000_000_000 // 1s later
	val1, _ := clk.Evaluate(raw1)
	nominalAdvance := 1_000_000_000
	actualAdvance := val1 - val0
	if actualAdvance <= core.GstInstant(nominalAdvance) {
		t.Fatalf("expected slew to advance clock faster than nominal: actual=%d nominal=%d",
			actualAdvance, nominalAdvance)
	}
}

func TestExplicitPhaseStep(t *testing.T) {
	clk := NewEstimateClock()
	raw0 := core.RawNanos(10_000_000_000)
	time0 := core.GstInstant(100_000_000_000)
	clk.InitializeAnchors(raw0, time0, 0)

	// Step forward by +50,000,000 ns
	stepVal := core.PhaseNs(50_000_000)
	res, err := clk.Step(raw0, stepVal)
	if err != nil {
		t.Fatalf("Step failed: %v", err)
	}

	expected := time0 + core.GstInstant(stepVal)
	if res != expected {
		t.Fatalf("step result mismatch: got %d want %d", res, expected)
	}

	valRead, _ := clk.Evaluate(raw0)
	if valRead != expected {
		t.Fatalf("evaluate after step mismatch: got %d want %d", valRead, expected)
	}
}

func TestSmearPlannerTriangularAndTrapezoidal(t *testing.T) {
	// Triangular plan: small displacement (e.g. 100 microseconds)
	smallTarget := core.PhaseNs(100_000) // 100 us
	planTri := PlanSmear(1_000_000_000, smallTarget, 100.0, 10.0)

	if len(planTri.Segments) != 2 {
		t.Fatalf("expected 2 segments for triangular plan, got %d", len(planTri.Segments))
	}
	biasTriEnd := planTri.EvaluateBiasAt(1_000_000_000 + 10_000_000_000)
	if math.Abs(float64(biasTriEnd-smallTarget)) > 1.0 { // within 1 ns
		t.Fatalf("triangular terminal bias mismatch: got %d want %d", biasTriEnd, smallTarget)
	}

	// Trapezoidal plan: large displacement (e.g. 1 second)
	largeTarget := core.PhaseNs(1_000_000_000)
	planTrap := PlanSmear(1_000_000_000, largeTarget, 100.0, 10.0)

	if len(planTrap.Segments) != 3 {
		t.Fatalf("expected 3 segments for trapezoidal plan, got %d", len(planTrap.Segments))
	}
	biasTrapEnd := planTrap.EvaluateBiasAt(1_000_000_000 + 20_000_000_000_000)
	if math.Abs(float64(biasTrapEnd-largeTarget)) > 1.0 {
		t.Fatalf("trapezoidal terminal bias mismatch: got %d want %d", biasTrapEnd, largeTarget)
	}
}

func TestSmearReplanFromNonzeroRateAppendixC(t *testing.T) {
	// Start with nonzero rate (e.g. 50 ppm) and existing bias (0.1s)
	initialBiasSec := 0.1
	initialRate := 50e-6
	targetBiasNs := core.PhaseNs(1_000_000_000) // 1s target

	plan := ReplanSmearFromState(1_000_000_000, initialBiasSec, initialRate, targetBiasNs, 100.0, 10.0)

	// First segment must be braking segment
	if len(plan.Segments) < 2 {
		t.Fatalf("expected at least 2 segments, got %d", len(plan.Segments))
	}
	seg0 := plan.Segments[0]
	endRate, _ := seg0.Evaluate(seg0.DurationSec)
	if math.Abs(endRate) > 1e-12 {
		t.Fatalf("braking segment did not bring rate to 0: %e", endRate)
	}

	// Terminal bias must reach target
	termBias := plan.EvaluateBiasAt(1_000_000_000 + 20_000_000_000_000)
	if math.Abs(float64(termBias-targetBiasNs)) > 1.0 {
		t.Fatalf("replan terminal bias mismatch: got %d want %d", termBias, targetBiasNs)
	}
}

func TestPublicClockMonotonicClamp(t *testing.T) {
	pc := NewPublicClock()

	// Read 1: normal
	p1, d1 := pc.ReadPublicInstant(100, 1000)
	if p1 != 100 || d1 != 0 {
		t.Fatalf("read 1 mismatch: p1=%d d1=%d", p1, d1)
	}

	// Read 2: higher
	p2, d2 := pc.ReadPublicInstant(200, 2000)
	if p2 != 200 || d2 != 0 {
		t.Fatalf("read 2 mismatch: p2=%d d2=%d", p2, d2)
	}

	// Read 3: estimate stepped backwards to 150!
	// Public clock must clamp to 200, with clamp debt = 50!
	p3, d3 := pc.ReadPublicInstant(150, 3000)
	if p3 != 200 {
		t.Fatalf("expected clamped p3=200, got %d", p3)
	}
	if d3 != 50 {
		t.Fatalf("expected clamp debt 50, got %d", d3)
	}

	// Read 4: caught back up to 250
	p4, d4 := pc.ReadPublicInstant(250, 4000)
	if p4 != 250 || d4 != 0 {
		t.Fatalf("read 4 mismatch: p4=%d d4=%d", p4, d4)
	}
}
