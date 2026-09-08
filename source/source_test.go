package source

import (
	"errors"
	"math"
	"testing"

	"gosuda.org/gstime/core"
)

func TestWeightedRegressionPlantedPhaseAndRate(t *testing.T) {
	et := NewEstimateTrack()

	// Planted: phase = 0.005 s (5ms), rateError = 10 ppm = 1e-5
	plantedPhase := 0.005
	plantedRate := 1e-5

	raw0 := core.RawNanos(1_000_000_000)
	for i := 0; i < 10; i++ {
		raw := raw0 + core.RawNanos(i*1_000_000_000)               // 1 second intervals
		x := float64(int64(raw)-int64(raw0+9*1_000_000_000)) / 1e9 // relative to newest
		phase := plantedPhase + plantedRate*x

		if err := et.AddSample(EstimateSample{
			RawMid:                 raw,
			PhaseErrorSlowPositive: phase,
			RoundTripDelay:         0.02,
			RootDelay:              0.01,
			RootDispersion:         0.005,
		}); err != nil {
			t.Fatalf("AddSample failed: %v", err)
		}
	}

	res, err := et.Fit()
	if err != nil {
		t.Fatalf("Fit failed: %v", err)
	}

	if math.Abs(res.PhaseEstimate-plantedPhase) > 1e-9 {
		t.Fatalf("phase estimate mismatch: got %f want %f", res.PhaseEstimate, plantedPhase)
	}
	if math.Abs(res.RateErrorEstimate-plantedRate) > 1e-9 {
		t.Fatalf("rate estimate mismatch: got %e want %e", res.RateErrorEstimate, plantedRate)
	}
}

func TestEstimateTrackRetainsRunsPassingSuffix(t *testing.T) {
	et := NewEstimateTrack()
	for i := range 20 {
		phase := 0.0
		if i >= 10 {
			phase = 0.001
		}
		if err := et.AddSample(EstimateSample{
			RawMid:                 core.RawNanos(i) * 1_000_000_000,
			PhaseErrorSlowPositive: phase,
		}); err != nil {
			t.Fatalf("sample %d: %v", i, err)
		}
	}
	first, err := et.Fit()
	if err != nil {
		t.Fatalf("initial Fit: %v", err)
	}
	if first.SampleCount != 11 || first.ResidualRunsPValue < DefaultRunsAlpha {
		t.Fatalf("expected an 11-sample passing suffix, got %+v", first)
	}
	for i := 20; i < 30; i++ {
		if err := et.AddSample(EstimateSample{
			RawMid:                 core.RawNanos(i) * 1_000_000_000,
			PhaseErrorSlowPositive: float64(i%2) * 0.001,
		}); err != nil {
			t.Fatalf("sample %d: %v", i, err)
		}
	}
	second, err := et.Fit()
	if err != nil {
		t.Fatalf("Fit after new samples: %v", err)
	}
	if second.SampleCount > first.SampleCount+10 {
		t.Fatalf("discarded samples revived: fit uses %d samples after retaining %d and adding 10", second.SampleCount, first.SampleCount)
	}
}

func TestEstimateTrackRejectsNonfiniteSamplesWithoutRetainingThem(t *testing.T) {
	cases := []struct {
		name   string
		sample EstimateSample
	}{
		{"nan-phase", EstimateSample{PhaseErrorSlowPositive: math.NaN()}},
		{"infinite-phase", EstimateSample{PhaseErrorSlowPositive: math.Inf(1)}},
		{"nan-round-trip", EstimateSample{RoundTripDelay: math.NaN()}},
		{"infinite-round-trip", EstimateSample{RoundTripDelay: math.Inf(1)}},
		{"nan-root-delay", EstimateSample{RootDelay: math.NaN()}},
		{"infinite-root-delay", EstimateSample{RootDelay: math.Inf(-1)}},
		{"nan-root-dispersion", EstimateSample{RootDispersion: math.NaN()}},
		{"infinite-root-dispersion", EstimateSample{RootDispersion: math.Inf(1)}},
		{"overflowing-distance", EstimateSample{RootDispersion: math.MaxFloat64, RootDelay: math.MaxFloat64}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			et := NewEstimateTrack()
			if err := et.AddSample(EstimateSample{RawMid: 0}); err != nil {
				t.Fatal(err)
			}
			tc.sample.RawMid = 1_000_000_000
			if err := et.AddSample(tc.sample); !errors.Is(err, ErrInvalidEstimateSample) {
				t.Fatalf("invalid sample: got %v, want ErrInvalidEstimateSample", err)
			}
			if err := et.AddSample(EstimateSample{RawMid: 2_000_000_000}); err != nil {
				t.Fatal(err)
			}
			if res, err := et.Fit(); res != nil || !errors.Is(err, ErrNotEnoughSamples) {
				t.Fatalf("two valid samples must not fit: result=%+v error=%v", res, err)
			}
			if err := et.AddSample(EstimateSample{RawMid: 3_000_000_000}); err != nil {
				t.Fatal(err)
			}
			res, err := et.Fit()
			if err != nil {
				t.Fatalf("Fit after replacement sample: %v", err)
			}
			for name, value := range map[string]float64{
				"phase":                       res.PhaseEstimate,
				"rate":                        res.RateErrorEstimate,
				"phase standard error":        res.PhaseStandardError,
				"rate standard error":         res.RateStandardError,
				"residual standard deviation": res.ResidualStandardDeviation,
			} {
				if value != 0 {
					t.Errorf("%s = %g, want 0 for three zero-phase samples", name, value)
				}
			}
			if res.SampleCount != 3 || res.ResidualRunsPValue != 1 {
				t.Fatalf("invalid sample affected later fit: %+v", res)
			}
		})
	}
}

func TestEstimateTrackRejectsUnstableRegression(t *testing.T) {
	cases := []struct {
		name    string
		samples []EstimateSample
		wantErr error
	}{
		{
			name: "overflowing-weight-denominator",
			samples: []EstimateSample{
				{RawMid: 0},
				{RawMid: 1_000_000_000},
				{RawMid: 2_000_000_000, RootDispersion: math.MaxFloat64},
			},
			wantErr: ErrUnstableRegression,
		},
		{
			name: "overflowing-regression-numerator",
			samples: []EstimateSample{
				{RawMid: 0},
				{RawMid: 1_000_000_000, PhaseErrorSlowPositive: math.MaxFloat64},
				{RawMid: 2_000_000_000},
			},
			wantErr: ErrUnstableRegression,
		},
		{
			name: "overflowing-residual-variance",
			samples: []EstimateSample{
				{RawMid: 0, PhaseErrorSlowPositive: 1e200},
				{RawMid: 1_000_000_000, PhaseErrorSlowPositive: -1e200},
				{RawMid: 2_000_000_000, PhaseErrorSlowPositive: 1e200},
			},
			wantErr: ErrUnstableRegression,
		},
		{
			name: "singular-nanosecond-design",
			samples: []EstimateSample{
				{RawMid: 0},
				{RawMid: 1, RootDispersion: 1},
				{RawMid: 2, RootDispersion: 100},
			},
			wantErr: ErrSingularDesignMatrix,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			et := NewEstimateTrack()
			for _, sample := range tc.samples {
				if err := et.AddSample(sample); err != nil {
					t.Fatalf("finite sample rejected: %v", err)
				}
			}
			if res, err := et.Fit(); res != nil || !errors.Is(err, tc.wantErr) {
				t.Fatalf("Fit: result=%+v error=%v, want %v", res, err, tc.wantErr)
			}
		})
	}
}

func TestRunsTestRejectsNonfiniteValues(t *testing.T) {
	cases := []struct {
		name      string
		residuals []float64
		model     float64
	}{
		{"nan-residual", []float64{0.01, math.NaN(), -0.01}, 1},
		{"infinite-residual", []float64{0.01, math.Inf(1), -0.01}, 1},
		{"nan-model", []float64{0.01, -0.01}, math.NaN()},
		{"infinite-model", []float64{0.01, -0.01}, math.Inf(1)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if p := ComputeRunsPValue(tc.residuals, tc.model); !math.IsNaN(p) {
				t.Fatalf("invalid runs input returned p=%g, want NaN", p)
			}
		})
	}
}

func TestRunsTestRegimeChange(t *testing.T) {
	// Normal residuals around expected mean runs
	normal := []float64{0.01, 0.01, -0.01, 0.01, -0.01, -0.01, 0.01, -0.01}
	pNorm := ComputeRunsPValue(normal, 1.0)
	if pNorm < 0.2 {
		t.Fatalf("expected high p-value for normal residuals, got %f", pNorm)
	}

	// Strong regime change (all positive then all negative) -> only 2 runs -> very low p-value (2/252 ~= 0.0079)
	regimeChange := []float64{0.01, 0.01, 0.01, 0.01, 0.01, -0.01, -0.01, -0.01, -0.01, -0.01}
	pChange := ComputeRunsPValue(regimeChange, 1.0)
	if pChange > 0.05 {
		t.Fatalf("expected low p-value for regime change, got %f", pChange)
	}
}

func TestDeadbandZeroResidualsOmitted(t *testing.T) {
	residuals := []float64{0.01, -0.01, 0.01, -0.01, 0.01, -0.01, 0.01, -0.01, 0.01, -0.01}
	withTinyResiduals := make([]float64, 0, 2*len(residuals))
	for _, r := range residuals {
		withTinyResiduals = append(withTinyResiduals, r, 1e-12)
	}
	want := ComputeRunsPValue(residuals, 1.0)
	got := ComputeRunsPValue(withTinyResiduals, 1.0)
	if math.IsNaN(got) || math.Abs(got-want) > 1e-12 {
		t.Fatalf("deadband residuals changed runs p-value: got %g want %g", got, want)
	}
}

func TestDomainConsolidation(t *testing.T) {
	// 1. Agreeing endpoint intervals: [100, 300] and [150, 250]
	// Intersection is [150, 250]
	d1 := ConsolidateDomainIntervals("domain1", []core.TimeInterval{
		{Earliest: 100, Latest: 300},
		{Earliest: 150, Latest: 250},
	})
	if d1.Inconsistent {
		t.Fatalf("expected domain1 consistent")
	}
	if d1.Interval.Earliest != 150 || d1.Interval.Latest != 250 {
		t.Fatalf("domain1 intersection mismatch: %+v", d1.Interval)
	}

	// 2. Disagreeing endpoint intervals (empty intersection): [100, 200] and [250, 350]
	d2 := ConsolidateDomainIntervals("domain2", []core.TimeInterval{
		{Earliest: 100, Latest: 200},
		{Earliest: 250, Latest: 350},
	})
	if !d2.Inconsistent {
		t.Fatalf("expected domain2 inconsistent (empty intersection)")
	}
}

func TestNormativeCounterexampleCoverageAppendixB(t *testing.T) {
	// Section 4.12 & 9.6: Counterexample
	// A = [0, 10]
	// B = [0, 4]
	// C = [6, 10]
	// With N=3, F=1 => k = 3 - 1 = 2
	// Expected components: [0, 4] and [6, 10]
	// Expected full hull: [0, 10]
	intervals := []core.TimeInterval{
		{Earliest: 0, Latest: 10},
		{Earliest: 0, Latest: 4},
		{Earliest: 6, Latest: 10},
	}

	comps, err := ComputeCoverageComponents(intervals, 1)
	if err != nil {
		t.Fatalf("ComputeCoverageComponents failed: %v", err)
	}

	if len(comps) != 2 {
		t.Fatalf("expected 2 components, got %d: %+v", len(comps), comps)
	}
	if comps[0].Earliest != 0 || comps[0].Latest != 4 {
		t.Fatalf("comp[0] mismatch: got %+v want [0, 4]", comps[0])
	}
	if comps[1].Earliest != 6 || comps[1].Latest != 10 {
		t.Fatalf("comp[1] mismatch: got %+v want [6, 10]", comps[1])
	}

	// Full consensus
	res, err := ComputeAssuranceConsensus(intervals, 1, 3, 2, 2, 5)
	if err != nil {
		t.Fatalf("ComputeAssuranceConsensus failed: %v", err)
	}

	// Full hull must be published: [0, 10]!
	if res.Hull.Earliest != 0 || res.Hull.Latest != 10 {
		t.Fatalf("full hull mismatch: got %+v want [0, 10]", res.Hull)
	}

	// Primary component containing prior target 2 should be [0, 4]
	if res.PrimaryComponent.Earliest != 0 || res.PrimaryComponent.Latest != 4 {
		t.Fatalf("primary component mismatch: got %+v want [0, 4]", res.PrimaryComponent)
	}

	// Clamped control target: estimate target was 5, primary is [0, 4] -> clamped to 4!
	if res.ControlTarget != 4 {
		t.Fatalf("control target mismatch: got %d want 4", res.ControlTarget)
	}
	if res.ControlBridge != -1 {
		t.Fatalf("control bridge mismatch: got %d want -1", res.ControlBridge)
	}
}

func TestFaultBudgetChecks(t *testing.T) {
	// N < 2F + 1 should fail
	intervals := []core.TimeInterval{
		{Earliest: 0, Latest: 10},
		{Earliest: 1, Latest: 9},
	}
	_, err := ComputeCoverageComponents(intervals, 1) // N=2, F=1 requires N>=3
	if err != ErrInsufficientDomains {
		t.Fatalf("expected ErrInsufficientDomains, got %v", err)
	}
}
