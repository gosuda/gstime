package conformance

import (
	"context"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/gosuda/gstime"
	"github.com/gosuda/gstime/assurance"
	"github.com/gosuda/gstime/clock"
	"github.com/gosuda/gstime/core"
	"github.com/gosuda/gstime/ntp"
	"github.com/gosuda/gstime/nts"
	"github.com/gosuda/gstime/publish"
	"github.com/gosuda/gstime/source"
)

// P1 ReanchorContinuity: E_old(c) == E_new(c) exactly in implementation arithmetic
func TestP1_ReanchorContinuity(t *testing.T) {
	clk := clock.NewEstimateClock()
	raw0 := core.RawNanos(10_000_000_000)
	time0 := core.GstInstant(1_700_000_000 * 1_000_000_000)
	clk.InitializeAnchors(raw0, time0, 0)

	currRaw := raw0
	for i := 0; i < 50_000; i++ {
		currRaw += core.RawNanos(rand.Uint64N(500_000_000))
		before, _ := clk.Evaluate(currRaw)

		newRate := core.RateFromPpmEstimate(-300.0 + rand.Float64()*600.0)
		debt := core.PhaseNs(rand.Int64N(10_000) - 5_000)

		reanchorVal, err := clk.ReanchorContinuity(currRaw, newRate, debt, clock.ChangeRateReanchor)
		if err != nil {
			t.Fatalf("reanchor failed: %v", err)
		}
		after, _ := clk.Evaluate(currRaw)

		if before != after || before != reanchorVal {
			t.Fatalf("P1 violation at iter %d: before=%d after=%d", i, before, after)
		}
	}
}

// P2 DebtAccrualNoInstantStep: Accruing debt alone produces zero step at instant c
func TestP2_DebtAccrualNoInstantStep(t *testing.T) {
	clk := clock.NewEstimateClock()
	raw0 := core.RawNanos(5_000_000_000)
	clk.InitializeAnchors(raw0, 100_000_000_000, 0)

	before, _ := clk.Evaluate(raw0)
	_, _ = clk.ReanchorContinuity(raw0, 0, 1_000_000_000, clock.ChangeSlewReplan) // +1s debt
	after, _ := clk.Evaluate(raw0)

	if before != after {
		t.Fatalf("P2 violation: clock stepped on debt accrual: before=%d after=%d", before, after)
	}
}

// P3 CoverageSetContainsHonestIntersection: True time in non-faulty domains is in C_k
func TestP3_CoverageSetContainsHonestIntersection(t *testing.T) {
	simulatedTrueTime := core.GstInstant(1_700_000_000 * 1_000_000_000)

	// 4 domains: 3 honest (contain true time), 1 faulty (disjoint)
	// N=4, F=1 => k=3
	intervals := []core.TimeInterval{
		{Earliest: simulatedTrueTime - 100, Latest: simulatedTrueTime + 100}, // honest 1
		{Earliest: simulatedTrueTime - 50, Latest: simulatedTrueTime + 150},  // honest 2
		{Earliest: simulatedTrueTime - 200, Latest: simulatedTrueTime + 50},  // honest 3
		{Earliest: simulatedTrueTime + 5000, Latest: simulatedTrueTime + 6000}, // faulty
	}

	comps, err := source.ComputeCoverageComponents(intervals, 1)
	if err != nil {
		t.Fatalf("coverage sweep failed: %v", err)
	}

	// Simulated true time must be contained in at least one component
	found := false
	for _, c := range comps {
		if c.Earliest <= simulatedTrueTime && simulatedTrueTime <= c.Latest {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("P3 violation: true time %d not contained in coverage components: %+v",
			simulatedTrueTime, comps)
	}
}

// P4 CoverageHullContainsEveryCoverageComponent
func TestP4_CoverageHullContainsEveryCoverageComponent(t *testing.T) {
	intervals := []core.TimeInterval{
		{Earliest: 0, Latest: 100},
		{Earliest: 0, Latest: 30},
		{Earliest: 70, Latest: 100},
	}
	res, err := source.ComputeAssuranceConsensus(intervals, 1, 3, 2, 0, 50)
	if err != nil {
		t.Fatalf("consensus failed: %v", err)
	}

	for _, c := range res.Components {
		if c.Earliest < res.Hull.Earliest || c.Latest > res.Hull.Latest {
			t.Fatalf("P4 violation: component %+v outside hull %+v", c, res.Hull)
		}
	}
}

// P5 AssuranceIndependentOfEstimateStatistics
func TestP5_AssuranceIndependentOfEstimateStatistics(t *testing.T) {
	raw := clock.NewSimulatedRawClock(1_000_000_000)
	lh, _ := core.NewLeapHistory(10, []core.LeapEntry{})
	svc := gstime.NewClockService(raw, lh, [32]byte{1}, 32_000_000_000)

	hull := core.TimeInterval{Earliest: 1_000_000_000, Latest: 2_000_000_000}
	_ = svc.PublishAssuranceRound(1_000_000_000, hull, 1, 3, 2, 1, 100_000_000_000)

	now1 := svc.Now()

	// Alter estimate track with arbitrary phase & rate changes
	_ = svc.InitializeEstimate(1_000_000_000, 999_999_999_000, core.RateFromPpmEstimate(100.0))

	now2 := svc.Now()

	// Assurance interval must remain completely unchanged
	if now1.Interval.Earliest != now2.Interval.Earliest || now1.Interval.Latest != now2.Interval.Latest {
		t.Fatalf("P5 violation: estimate change modified assurance interval: %+v vs %+v",
			now1.Interval, now2.Interval)
	}
}

// P6 AbsolutePropagationContainsSimulatedTrueTime
func TestP6_AbsolutePropagationContainsSimulatedTrueTime(t *testing.T) {
	anchor := &assurance.AssuranceAnchor{
		RawAnchor:         1_000_000_000,
		LowerAtAnchor:     1_700_000_000 * 1_000_000_000 - 10_000,
		UpperAtAnchor:     1_700_000_000 * 1_000_000_000 + 10_000,
		RawScaleLower:     core.RateScale(core.OneQ48 + core.RateFromPpmLower(-50.0)),
		RawScaleUpper:     core.RateScale(core.OneQ48 + core.RateFromPpmUpper(50.0)),
		RawReadBound:      10,
		ContinuityToken:   1,
		ValidUntilRaw:     100_000_000_000,
	}

	trueTime0 := int64(1_700_000_000 * 1_000_000_000)

	// Simulate clock running at +20 ppm drift (well within [-50, +50] ppm envelope)
	driftRate := 20e-6
	for s := int64(1); s <= 30; s++ {
		elapsedRaw := core.RawNanos(s * 1_000_000_000)
		currentRaw := anchor.RawAnchor + elapsedRaw

		// True elapsed SI time with +20 ppm drift
		simulatedTrue := trueTime0 + int64(float64(s*1_000_000_000)*(1.0+driftRate))

		inv, err := assurance.PropagateAnchor(anchor, currentRaw, 10, 1, 0, 0, 32_000_000_000)
		if err != nil {
			t.Fatalf("propagation error at second %d: %v", s, err)
		}

		if core.GstInstant(simulatedTrue) < inv.Earliest || core.GstInstant(simulatedTrue) > inv.Latest {
			t.Fatalf("P6 violation at second %d: simulated true time %d outside [%d, %d]",
				s, simulatedTrue, inv.Earliest, inv.Latest)
		}
	}
}

// P7 PastWatermarkMonotonic: certified lower watermark never decreases
func TestP7_PastWatermarkMonotonic(t *testing.T) {
	ac := assurance.NewAssuranceClock(32_000_000_000)
	scale := core.RateScale(core.OneQ48)

	var lastWm core.GstInstant = -1 << 62
	for i := 1; i <= 10; i++ {
		raw := core.RawNanos(i * 1_000_000_000)
		earliest := core.GstInstant(i * 1_000_000_000)
		hull := core.TimeInterval{Earliest: earliest, Latest: earliest + 10_000}

		_, err := ac.ProcessFullRound(
			raw, hull, 10, scale, scale, 1, 100_000_000_000,
			1, 3, 2, 1, [32]byte{1}, [32]byte{2},
		)
		if err != nil {
			t.Fatalf("round %d failed: %v", i, err)
		}

		currWm := ac.Snapshot().PastWatermark
		if currWm < lastWm {
			t.Fatalf("P7 violation: watermark decreased from %d to %d", lastWm, currWm)
		}
		lastWm = currWm
	}
}

// P8 ReachBitmapCorrectUnderResponsePermutation
func TestP8_ReachBitmapCorrectUnderResponsePermutation(t *testing.T) {
	// Send 3 requests: serials 1, 2, 3
	// Permute responses: 3 then 1 then 2
	rm := ntp.NewReachabilityManager()
	s1 := rm.OnTransmit()
	s2 := rm.OnTransmit()
	s3 := rm.OnTransmit()

	rm.OnResponse(s3)
	rm.OnResponse(s1)
	rm.OnResponse(s2)

	// All 3 should be set: bits 0, 1, 2 -> 0x07
	if rm.Bitmap() != 0x07 {
		t.Fatalf("P8 violation: expected bitmap 0x07, got 0x%02x", rm.Bitmap())
	}
}

// P9 CookieSingleOwnership: Cookies never assigned to two concurrent requests
func TestP9_CookieSingleOwnership(t *testing.T) {
	jar := nts.NewCookieJar()
	var cookies [][]byte
	for i := 0; i < 8; i++ {
		cookies = append(cookies, []byte{byte(i + 1), 2, 3, 4})
	}
	jar.AddCookies(cookies)

	allocated := make(map[[16]byte]bool)
	for i := 0; i < 4; i++ {
		c, err := jar.AcquireForRequest(false)
		if err != nil {
			t.Fatalf("acquire failed: %v", err)
		}
		if allocated[c.ID] {
			t.Fatalf("P9 violation: duplicate cookie allocated: %x", c.ID)
		}
		allocated[c.ID] = true
	}
}

// P10 PublicBiasAndClampIncludedInPublicEpsilon
func TestP10_PublicBiasAndClampIncludedInPublicEpsilon(t *testing.T) {
	inv := &core.TimeInterval{Earliest: 1000, Latest: 2000}
	p := core.GstInstant(2500) // public clock outside interval due to smear/clamp
	readBound := core.ErrorNs(50)

	eps := clock.ComputePublicSymmetricEpsilon(p, inv, readBound)

	// Max distance |2500 - 1000| = 1500. Total eps = 1500 + 50 = 1550
	if eps < 1550 {
		t.Fatalf("P10 violation: epsilon %d does not contain public deviation", eps)
	}
}

// P11 ContinuousLeapAxisNeverRepeats
func TestP11_ContinuousLeapAxisNeverRepeats(t *testing.T) {
	// Leap transition at 78796800
	lh, err := core.NewLeapHistory(10, []core.LeapEntry{
		{TransitionUnixSecond: 78796800, Delta: 1},
	})
	if err != nil {
		t.Fatalf("NewLeapHistory: %v", err)
	}

	// Check 10,000 consecutive nanoseconds across the positive leap second
	baseNanos := int64(78796800-1) * 1_000_000_000 + 999_995_000
	seenInstants := make(map[core.GstInstant]bool)

	for i := int64(0); i < 10_000; i++ {
		inst := core.GstInstant(baseNanos + i)
		if seenInstants[inst] {
			t.Fatalf("P11 violation: duplicate GstInstant %d across leap second", inst)
		}
		seenInstants[inst] = true

		utc := lh.GstInstantToUtc(inst)
		rt, err := lh.UtcToGstInstant(utc)
		if err != nil || rt != inst {
			t.Fatalf("round trip failed for %d (utc=%s): rt=%d err=%v", inst, utc, rt, err)
		}
	}
}

// P12 CommitWaitStrictlyAfterLatest
func TestP12_CommitWaitStrictlyAfterLatest(t *testing.T) {
	raw := clock.NewSimulatedRawClock(1_000_000_000)
	lh, _ := core.NewLeapHistory(10, []core.LeapEntry{})
	var cfgID [32]byte
	cfgID[0] = 1

	svc := gstime.NewClockService(raw, lh, cfgID, 32_000_000_000)

	// Initial round
	hull := core.TimeInterval{Earliest: 5_000_000_000, Latest: 5_000_100_000}
	_ = svc.PublishAssuranceRound(1_000_000_000, hull, 1, 3, 2, 1, 100_000_000_000)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// 1. When commitTs <= current watermark, CommitWait completes immediately
	wm0, _, _ := svc.PastWatermark(svc.Now().AssuranceEpochID, lh.ID, cfgID)
	err := svc.CommitWait(ctx, wm0-1, svc.Now().AssuranceEpochID, lh.ID, cfgID)
	if err != nil {
		t.Fatalf("CommitWait for past timestamp failed: %v", err)
	}

	// 2. When commitTs > current watermark, CommitWait blocks until watermark > commitTs
	commitTs := core.GstInstant(5_000_050_000)
	done := make(chan error, 1)
	go func() {
		done <- svc.CommitWait(ctx, commitTs, svc.Now().AssuranceEpochID, lh.ID, cfgID)
	}()

	// Ensure it hasn't unblocked prematurely
	select {
	case <-done:
		t.Fatal("CommitWait unblocked prematurely before watermark advanced")
	default:
	}

	// Advance watermark strictly past commitTs
	raw.Advance(100_000_000)
	nextHull := core.TimeInterval{Earliest: 5_100_050_000, Latest: 5_100_150_000}
	_ = svc.PublishAssuranceRound(1_100_000_000, nextHull, 1, 3, 2, 1, 100_000_000_000)

	// Wait for CommitWait completion
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("CommitWait failed: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("CommitWait timed out waiting for watermark advance")
	}

	// Verify watermark at completion is strictly greater than commitTs
	wm, _, _ := svc.PastWatermark(svc.Now().AssuranceEpochID, lh.ID, cfgID)
	if wm <= commitTs {
		t.Fatalf("P12 violation: CommitWait completed with watermark %d <= commitTs %d", wm, commitTs)
	}
}

// P13 SnapshotGenerationCoherence
func TestP13_SnapshotGenerationCoherence(t *testing.T) {
	snap1 := &publish.Snapshot{
		RawScaleLower: core.RateScale(core.OneQ48),
		RawScaleUpper: core.RateScale(core.OneQ48),
		Generation:    10,
	}
	snap2 := &publish.Snapshot{
		RawScaleLower: core.RateScale(core.OneQ48),
		RawScaleUpper: core.RateScale(core.OneQ48),
		Generation:    9, // non-monotonic!
	}
	err := publish.ValidatePublication(snap1, snap2)
	if err != publish.ErrGuardGenerationNotMono {
		t.Fatalf("P13 violation: expected ErrGuardGenerationNotMono, got %v", err)
	}
}

// P14 DesyncReturnsNoFiniteAssuredInterval
func TestP14_DesyncReturnsNoFiniteAssuredInterval(t *testing.T) {
	raw := clock.NewSimulatedRawClock(1_000_000_000)
	lh, _ := core.NewLeapHistory(10, []core.LeapEntry{})
	svc := gstime.NewClockService(raw, lh, [32]byte{1}, 32_000_000_000)

	// In UNANCHORED
	nowUnanchored := svc.Now()
	if nowUnanchored.Interval != nil {
		t.Fatalf("P14 violation: UNANCHORED returned finite interval: %+v", nowUnanchored.Interval)
	}

	// Trigger conflict to enter DESYNC
	hull1 := core.TimeInterval{Earliest: 1_000_000_000, Latest: 2_000_000_000}
	_ = svc.PublishAssuranceRound(1_000_000_000, hull1, 1, 3, 2, 1, 100_000_000_000)

	raw.Advance(1_000_000_000)
	// Disjoint hull -> ASSURANCE_CONFLICT -> DESYNC
	hullDisjoint := core.TimeInterval{Earliest: 9_000_000_000, Latest: 10_000_000_000}
	_ = svc.PublishAssuranceRound(2_000_000_000, hullDisjoint, 1, 3, 2, 1, 100_000_000_000)

	nowDesync := svc.Now()
	if nowDesync.Status != core.StatusDesync {
		t.Fatalf("expected StatusDesync, got %s", nowDesync.Status)
	}
	if nowDesync.Interval != nil {
		t.Fatalf("P14 violation: DESYNC returned non-nil interval: %+v", nowDesync.Interval)
	}
	if nowDesync.SymmetricEpsilon != nil {
		t.Fatalf("P14 violation: DESYNC returned non-nil symmetric epsilon")
	}
}
