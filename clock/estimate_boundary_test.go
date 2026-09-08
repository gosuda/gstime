package clock

import (
	"errors"
	"math"
	"testing"

	"gosuda.org/gstime/core"
)

func TestEstimateEvaluateRejectsSignedOverflow(t *testing.T) {
	for _, tc := range []struct {
		name    string
		mapping EstimateMapping
		raw     core.RawNanos
	}{
		{name: "positive anchor addition", mapping: EstimateMapping{TimeAnchor: math.MaxInt64}, raw: 1},
		{name: "negative anchor addition", mapping: EstimateMapping{RawAnchor: 1, TimeAnchor: math.MinInt64}, raw: 0},
		{name: "positive raw delta", raw: math.MaxUint64},
		{name: "negative raw delta", mapping: EstimateMapping{RawAnchor: math.MaxUint64}},
		{name: "base scale addition", mapping: EstimateMapping{BaseRate: math.MaxInt64}},
		{name: "positive slew addition", mapping: EstimateMapping{TimeAnchor: math.MaxInt64 - 1, PhaseDebt: 1, SlewRate: core.OneQ48}, raw: 1},
		{name: "negative slew addition", mapping: EstimateMapping{RawAnchor: 1, TimeAnchor: math.MinInt64 + 1, PhaseDebt: -2, SlewRate: -2 * core.OneQ48}, raw: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.mapping.Anchored = true
			if value, err := tc.mapping.Evaluate(tc.raw); !errors.Is(err, core.ErrOverflow) {
				t.Fatalf("Evaluate = %d, %v; want ErrOverflow", value, err)
			}
		})
	}
}

func TestEstimateEvaluateUsesUnsignedRawCoordinates(t *testing.T) {
	mapping := EstimateMapping{Anchored: true, RawAnchor: math.MaxUint64 - 2, TimeAnchor: -1}
	if got, err := mapping.Evaluate(math.MaxUint64); err != nil || got != 1 {
		t.Fatalf("upper raw range = %d, %v; want 1", got, err)
	}
	mapping = EstimateMapping{Anchored: true, RawAnchor: 1 << 63}
	if got, err := mapping.Evaluate(0); err != nil || got != math.MinInt64 {
		t.Fatalf("minimum signed delta = %d, %v; want MinInt64", got, err)
	}
}

func TestEstimateMutationFailuresPreserveMapping(t *testing.T) {
	for _, tc := range []struct {
		name   string
		target core.GstInstant
		debt   core.PhaseNs
		mutate func(*EstimateClock) error
	}{
		{name: "positive step", target: math.MaxInt64, mutate: func(c *EstimateClock) error { _, err := c.Step(10, 1); return err }},
		{name: "negative step", target: math.MinInt64, mutate: func(c *EstimateClock) error { _, err := c.Step(10, -1); return err }},
		{name: "positive debt", debt: math.MaxInt64, mutate: func(c *EstimateClock) error { _, err := c.ReanchorContinuity(10, 0, 1, ChangeSlewReplan); return err }},
		{name: "negative debt", debt: math.MinInt64, mutate: func(c *EstimateClock) error { _, err := c.ReanchorContinuity(10, 0, -1, ChangeSlewReplan); return err }},
		{name: "reanchor evaluation", target: math.MaxInt64, mutate: func(c *EstimateClock) error { _, err := c.ReanchorContinuity(11, 0, 0, ChangeRateReanchor); return err }},
		{name: "invalid initial scale", mutate: func(c *EstimateClock) error { return c.InitializeAnchors(20, 100, math.MaxInt64) }},
		{name: "invalid reanchor scale", mutate: func(c *EstimateClock) error {
			_, err := c.ReanchorContinuity(10, math.MaxInt64, 0, ChangeRateReanchor)
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clk := NewEstimateClock()
			if err := clk.InitializeAnchors(10, tc.target, 0); err != nil {
				t.Fatal(err)
			}
			if tc.debt != 0 {
				if _, err := clk.ReanchorContinuity(10, 0, tc.debt, ChangeSlewReplan); err != nil {
					t.Fatal(err)
				}
			}
			before := clk.Snapshot()
			if err := tc.mutate(clk); !errors.Is(err, core.ErrOverflow) {
				t.Fatalf("mutation error = %v, want ErrOverflow", err)
			}
			if after := clk.Snapshot(); after != before {
				t.Fatalf("failed mutation changed mapping: before=%+v after=%+v", before, after)
			}
			if got, err := clk.Evaluate(10); err != nil || got != tc.target {
				t.Fatalf("failed mutation changed clock: got %d, %v; want %d", got, err, tc.target)
			}
		})
	}
}

func TestLargeSlewDebtKeepsSign(t *testing.T) {
	for _, debt := range []core.PhaseNs{math.MinInt64, math.MaxInt64} {
		clk := NewEstimateClock()
		if err := clk.InitializeAnchors(0, 0, 0); err != nil {
			t.Fatal(err)
		}
		if _, err := clk.ReanchorContinuity(0, 0, debt, ChangeSlewReplan); err != nil {
			t.Fatal(err)
		}
		mapping := clk.Snapshot()
		if (mapping.SlewRate < 0) != (debt < 0) || mapping.SlewRate == 0 {
			t.Fatalf("debt %d produced wrong slew rate %d", debt, mapping.SlewRate)
		}
		got, err := clk.Evaluate(1_000_000_000)
		if err != nil || (got < 1_000_000_000) != (debt < 0) || got == 1_000_000_000 {
			t.Fatalf("debt %d: one second estimate = %d, %v", debt, got, err)
		}
		mapping.SlewRate = core.OneQ48
		if debt < 0 {
			mapping.SlewRate = -core.OneQ48
		}
		if got := mapping.SlewApplied(math.MaxUint64); got != int64(debt) {
			t.Fatalf("full raw range slew = %d, want debt %d", got, debt)
		}
	}
}

func TestStepPreservesRemainingSlewDebt(t *testing.T) {
	for _, debt := range []core.PhaseNs{-1_000_000_000, 1_000_000_000} {
		clk := NewEstimateClock()
		if err := clk.InitializeAnchors(0, 0, 0); err != nil {
			t.Fatal(err)
		}
		if _, err := clk.ReanchorContinuity(0, 0, debt, ChangeSlewReplan); err != nil {
			t.Fatal(err)
		}
		before := clk.Snapshot()
		const raw = core.RawNanos(1_000_000_000)
		old, err := before.Evaluate(raw)
		if err != nil {
			t.Fatal(err)
		}
		got, err := clk.Step(raw, -100)
		if err != nil || got != old-100 {
			t.Fatalf("step = %d, %v; want %d", got, err, old-100)
		}
		after := clk.Snapshot()
		wantDebt := debt - core.PhaseNs(before.SlewApplied(raw))
		if after.PhaseDebt != wantDebt || (after.SlewRate < 0) != (debt < 0) {
			t.Fatalf("outstanding debt after step = %d, rate=%d; want %d", after.PhaseDebt, after.SlewRate, wantDebt)
		}
		if later, err := clk.Evaluate(raw + 1_000_000_000); err != nil || (later-got < 1_000_000_000) != (debt < 0) {
			t.Fatalf("post-step advance = %d, %v; debt=%d", later-got, err, debt)
		}
	}
}

func TestMappingHistoryRetainsStepAcrossContinuousReanchor(t *testing.T) {
	clk := NewEstimateClock()
	if _, err := clk.MappingAt(100); !errors.Is(err, core.ErrUnanchored) {
		t.Fatalf("unanchored MappingAt error = %v", err)
	}
	if _, err := clk.Evaluate(100); !errors.Is(err, core.ErrUnanchored) {
		t.Fatalf("unanchored Evaluate error = %v", err)
	}
	if err := clk.InitializeAnchors(100, 1000, 0); err != nil {
		t.Fatal(err)
	}
	sent := clk.Snapshot()
	if _, err := clk.Step(200, 200_000_000); err != nil {
		t.Fatal(err)
	}
	if _, err := clk.ReanchorContinuity(300, core.RateFromPpmEstimate(10), 100, ChangeRateReanchor); err != nil {
		t.Fatal(err)
	}
	received := clk.Snapshot()
	if !received.Anchored || received.LastChangeKind != ChangeRateReanchor || received.StepGeneration == sent.StepGeneration {
		t.Fatalf("step cannot be detected after reanchor: sent=%+v received=%+v", sent, received)
	}
	for _, tc := range []struct {
		raw  core.RawNanos
		want core.GstInstant
		step core.Generation
	}{
		{raw: 150, want: 1050, step: sent.StepGeneration},
		{raw: 250, want: 200_001_150, step: received.StepGeneration},
		{raw: 300, want: 200_001_200, step: received.StepGeneration},
	} {
		mapping, err := clk.MappingAt(tc.raw)
		if err != nil {
			t.Fatal(err)
		}
		if mapping.StepGeneration != tc.step {
			t.Fatalf("raw %d step generation = %d, want %d", tc.raw, mapping.StepGeneration, tc.step)
		}
		if got, err := mapping.Evaluate(tc.raw); err != nil || got != tc.want {
			t.Fatalf("raw %d estimate = %d, %v; want %d", tc.raw, got, err, tc.want)
		}
	}
	if err := clk.InitializeAnchors(400, 2000, 0); err != nil {
		t.Fatal(err)
	}
	reinitialized := clk.Snapshot()
	if reinitialized.StepGeneration != received.StepGeneration+1 || reinitialized.LastChangeKind != ChangePhaseStep {
		t.Fatalf("reinitialization did not record a step: %+v", reinitialized)
	}
	if got, err := sent.Evaluate(150); err != nil || got != 1050 {
		t.Fatalf("retained snapshot changed: %d, %v", got, err)
	}
}

func TestMappingHistoryRejectsEvictedOrResetCoordinates(t *testing.T) {
	clk := NewEstimateClock()
	if err := clk.InitializeAnchors(10, 0, 0); err != nil {
		t.Fatal(err)
	}
	for raw := core.RawNanos(11); raw <= 267; raw++ {
		if _, err := clk.ReanchorContinuity(raw, 0, 0, ChangeRateReanchor); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := clk.MappingAt(10); err == nil {
		t.Fatal("evicted mapping was extrapolated")
	}
	mapping, err := clk.MappingAt(11)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := mapping.Evaluate(11); err != nil || got != 1 {
		t.Fatalf("oldest retained mapping = %d, %v; want 1", got, err)
	}
	if err := clk.InitializeAnchors(5, 100, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := clk.MappingAt(4); err == nil {
		t.Fatal("raw coordinate before reset was extrapolated")
	}
	mapping, err = clk.MappingAt(11)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := mapping.Evaluate(11); err != nil || got != 106 {
		t.Fatalf("reset coordinate reused stale mapping: %d, %v; want 106", got, err)
	}
}
