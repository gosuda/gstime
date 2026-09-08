package clock

import (
	"errors"
	"math"
	"testing"

	"gosuda.org/gstime/core"
)

func TestCompletedSmearReplanPreservesNonzeroBias(t *testing.T) {
	for _, target := range []core.PhaseNs{-1_000_000_000, 1_000_000_000} {
		pc := NewPublicClock()
		pc.SetSmearPlan(PlanSmear(0, target, 100, 10))
		const end = core.RawNanos(20_000_000_000_000)
		before, debt, err := pc.ReadPublicInstant(core.GstInstant(end), end)
		if err != nil || debt != 0 || before != core.GstInstant(end)+core.GstInstant(target) {
			t.Fatalf("completed smear: center=%d debt=%d error=%v", before, debt, err)
		}
		plan := ReplanSmearFromState(end, float64(target)/1e9, 0, target, 100, 10)
		pc.SetSmearPlan(plan)
		for _, elapsed := range []core.RawNanos{0, 1, 1_000_000_000_000} {
			raw := end + elapsed
			if bias := plan.EvaluateBiasAt(raw); bias != target {
				t.Fatalf("elapsed %d: completed bias=%d, want %d", elapsed, bias, target)
			}
			got, debt, err := pc.ReadPublicInstant(core.GstInstant(raw), raw)
			if err != nil || debt != 0 || got != before+core.GstInstant(elapsed) {
				t.Fatalf("elapsed %d: replan jumped or stalled: center=%d debt=%d error=%v", elapsed, got, debt, err)
			}
		}
	}
}

func TestPublicArithmeticFailurePreservesCenter(t *testing.T) {
	for _, tc := range []struct {
		name     string
		estimate core.GstInstant
		bias     core.PhaseNs
	}{
		{name: "positive smear overflow", estimate: math.MaxInt64, bias: 1},
		{name: "negative smear overflow", estimate: math.MinInt64, bias: -1},
		{name: "clamp debt overflow", estimate: math.MinInt64},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pc := NewPublicClock()
			if _, _, err := pc.ReadPublicInstant(17, 0); err != nil {
				t.Fatal(err)
			}
			pc.SetSmearPlan(&SmearPlan{TerminalBiasNs: tc.bias})
			got, _, err := pc.ReadPublicInstant(tc.estimate, 1)
			if !errors.Is(err, core.ErrOverflow) || got != 17 || pc.LastPublicInstant() != 17 {
				t.Fatalf("failed read = %d, %v; preserved center=%d, want 17 and ErrOverflow", got, err, pc.LastPublicInstant())
			}
			pc.SetSmearPlan(nil)
			if got, debt, err := pc.ReadPublicInstant(18, 2); err != nil || got != 18 || debt != 0 {
				t.Fatalf("read after failure = %d, %d, %v; want 18, 0", got, debt, err)
			}
		})
	}
}

func TestPublicClockAcceptsNegativeFirstInstant(t *testing.T) {
	pc := NewPublicClock()
	got, debt, err := pc.ReadPublicInstant(math.MinInt64, 0)
	if err != nil || got != math.MinInt64 || debt != 0 {
		t.Fatalf("first signed instant = %d, %d, %v; want MinInt64, 0", got, debt, err)
	}
}

func TestPublicEpsilonContainsSignedExtremes(t *testing.T) {
	interval := &core.TimeInterval{Earliest: math.MinInt64, Latest: math.MinInt64 + 10}
	if got := ComputePublicSymmetricEpsilon(math.MaxInt64, interval, 100); got != core.ErrorInfinity {
		t.Fatalf("opposite-extreme epsilon = %d, want infinity", got)
	}
	interval = &core.TimeInterval{Earliest: -20, Latest: 25}
	if got := ComputePublicSymmetricEpsilon(10, interval, 3); got != 33 {
		t.Fatalf("ordinary public epsilon = %d, want 33", got)
	}
}
