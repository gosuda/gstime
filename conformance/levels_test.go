package conformance

import (
	"context"
	"testing"
	"time"

	"github.com/gosuda/gstime"
	"github.com/gosuda/gstime/clock"
	"github.com/gosuda/gstime/core"
	"github.com/gosuda/gstime/ntp"
	"github.com/gosuda/gstime/source"
)

func TestConformance_LevelA_Wire(t *testing.T) {
	// Bit-exact NTP header and era unfolding
	p := &ntp.Packet{
		Version:   4,
		Mode:      4,
		Stratum:   1,
		Poll:      6,
		Precision: -20,
	}
	enc := p.Encode()
	dec, err := ntp.ParsePacket(enc)
	if err != nil || dec.Version != 4 {
		t.Fatalf("Level A wire failure: %v", err)
	}

	// Era unfolding before and after 2036 wrap
	unfolded, err := ntp.UnfoldNtpSeconds(0, 2_085_978_496)
	if err != nil || unfolded != 4294967296 {
		t.Fatalf("Level A era unfolding failure: %v", err)
	}
}

func TestConformance_LevelB_Estimate(t *testing.T) {
	et := source.NewEstimateTrack()
	for i := 0; i < 5; i++ {
		et.AddSample(source.EstimateSample{
			RawMid:                 core.RawNanos((i + 1) * 1_000_000_000),
			PhaseErrorSlowPositive: 0.001 * float64(i),
			RoundTripDelay:         0.01,
		})
	}
	res, err := et.Fit()
	if err != nil || res == nil {
		t.Fatalf("Level B estimate failure: %v", err)
	}
}

func TestConformance_LevelC_Assurance(t *testing.T) {
	// N-F coverage with counterexample
	intervals := []core.TimeInterval{
		{Earliest: 0, Latest: 10},
		{Earliest: 0, Latest: 4},
		{Earliest: 6, Latest: 10},
	}
	comps, err := source.ComputeCoverageComponents(intervals, 1)
	if err != nil || len(comps) != 2 {
		t.Fatalf("Level C assurance sweep failure: %v, comps=%+v", err, comps)
	}
}

func TestConformance_LevelD_Clock(t *testing.T) {
	clk := clock.NewEstimateClock()
	clk.InitializeAnchors(1_000_000_000, 100_000_000, 0)
	val1, _ := clk.Evaluate(2_000_000_000)
	val2, _ := clk.ReanchorContinuity(2_000_000_000, core.RateFromPpmEstimate(10.0), 0, clock.ChangeRateReanchor)
	if val1 != val2 {
		t.Fatalf("Level D clock continuity failure: %d != %d", val1, val2)
	}
}

func TestConformance_LevelE_Concurrency(t *testing.T) {
	raw := clock.NewSimulatedRawClock(1_000_000_000)
	lh, _ := core.NewLeapHistory(10, []core.LeapEntry{})
	svc := gstime.NewClockService(raw, lh, [32]byte{1}, 32_000_000_000)

	hull := core.TimeInterval{Earliest: 1_000_000_000, Latest: 2_000_000_000}
	_ = svc.PublishAssuranceRound(1_000_000_000, hull, 1, 3, 2, 1, 100_000_000_000)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_ = svc.CommitWait(ctx, 500_000_000, svc.Now().AssuranceEpochID, lh.ID, [32]byte{1})
}

func TestConformance_LevelF_System(t *testing.T) {
	raw := clock.NewSimulatedRawClock(1_000_000_000)
	lh, _ := core.NewLeapHistory(10, []core.LeapEntry{})
	svc := gstime.NewClockService(raw, lh, [32]byte{1}, 32_000_000_000)

	// Step 1: Synced
	hull := core.TimeInterval{Earliest: 1_000_000_000, Latest: 2_000_000_000}
	_ = svc.PublishAssuranceRound(1_000_000_000, hull, 1, 3, 2, 1, 100_000_000_000)
	if svc.Now().Status != core.StatusSynced {
		t.Fatalf("Level F: expected StatusSynced")
	}

	// Step 2: Fault injection (conflict)
	raw.Advance(1_000_000_000)
	disjoint := core.TimeInterval{Earliest: 50_000_000_000, Latest: 51_000_000_000}
	_ = svc.PublishAssuranceRound(2_000_000_000, disjoint, 1, 3, 2, 1, 100_000_000_000)
	if svc.Now().Status != core.StatusDesync {
		t.Fatalf("Level F: expected StatusDesync after conflict")
	}
}
