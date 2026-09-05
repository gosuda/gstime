package source

import (
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

		et.AddSample(EstimateSample{
			RawMid:                 raw,
			PhaseErrorSlowPositive: phase,
			RoundTripDelay:         0.02,
			RootDelay:              0.01,
			RootDispersion:         0.005,
		})
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
	// Residuals within deadband should be omitted from sign sequence
	residuals := []float64{0.01, 1e-12, -0.01}
	p := ComputeRunsPValue(residuals, 1.0)
	if p <= 0 {
		t.Fatalf("unexpected p-value with tiny middle residual: %f", p)
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
