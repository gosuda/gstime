package gstime

import (
	"context"
	"testing"
	"time"

	"github.com/gosuda/gstime/clock"
)

func TestClockServiceLifecycleAndDecisions(t *testing.T) {
	raw := clock.NewSimulatedRawClock(1_000_000_000)
	lh, _ := NewLeapHistory(10, []LeapEntry{
		{TransitionUnixSecond: 78796800, Delta: 1},
	})
	var cfgID [32]byte
	cfgID[0] = 0xab

	svc := NewClockService(raw, lh, cfgID, 32_000_000_000)

	// 1. Initial state: UNANCHORED
	now0 := svc.Now()
	if now0.Status != StatusUnanchored || now0.Interval != nil {
		t.Fatalf("expected UNANCHORED with nil interval, got status=%s, inv=%v", now0.Status, now0.Interval)
	}

	// Decisions return Unknown in UNANCHORED
	decA, statA, _ := svc.After(100)
	if decA != Unknown || statA != StatusUnanchored {
		t.Fatalf("expected Unknown on After in UNANCHORED, got %s", decA)
	}

	// 2. Initialize estimate and publish full assurance round
	targetTime := GstInstant(1_700_000_000 * 1_000_000_000)
	_ = svc.InitializeEstimate(1_000_000_000, targetTime, 0)

	hull := TimeInterval{
		Earliest: targetTime - 50_000,
		Latest:   targetTime + 50_000,
	}
	err := svc.PublishAssuranceRound(1_000_000_000, hull, 1, 3, 2, 1, 100_000_000_000)
	if err != nil {
		t.Fatalf("PublishAssuranceRound failed: %v", err)
	}

	// 3. Now in SYNCED state
	nowSynced := svc.Now()
	if nowSynced.Status != StatusSynced || nowSynced.Interval == nil {
		t.Fatalf("expected StatusSynced with non-nil interval")
	}
	if nowSynced.SymmetricEpsilon == nil || *nowSynced.SymmetricEpsilon < 50_000 {
		t.Fatalf("expected valid symmetric epsilon, got %v", nowSynced.SymmetricEpsilon)
	}

	// 4. Test Decisions
	// Hull is around targetTime.
	// tPast = targetTime - 1_000_000 (well before Earliest)
	tPast := targetTime - 1_000_000
	decAfterPast, _, _ := svc.After(tPast)
	if decAfterPast != CertainYes {
		t.Fatalf("expected CertainYes for After(tPast), got %s", decAfterPast)
	}

	// tFuture = targetTime + 1_000_000 (well after Latest)
	tFuture := targetTime + 1_000_000
	decAfterFuture, _, _ := svc.After(tFuture)
	if decAfterFuture != CertainNo {
		t.Fatalf("expected CertainNo for After(tFuture), got %s", decAfterFuture)
	}

	decBeforeFuture, _, _ := svc.Before(tFuture)
	if decBeforeFuture != CertainYes {
		t.Fatalf("expected CertainYes for Before(tFuture), got %s", decBeforeFuture)
	}

	// tInside = targetTime (inside interval) -> Unknown
	decInside, _, _ := svc.After(targetTime)
	if decInside != Unknown {
		t.Fatalf("expected Unknown for tInside, got %s", decInside)
	}

	// 5. PastWatermark
	wm, stat, err := svc.PastWatermark(svc.epochID, lh.ID, cfgID)
	if err != nil || stat != StatusSynced {
		t.Fatalf("PastWatermark failed: err=%v, stat=%s", err, stat)
	}
	if wm != hull.Earliest {
		t.Fatalf("watermark mismatch: got %d want %d", wm, hull.Earliest)
	}

	// Mismatched ID rejection
	var wrongCfgID [32]byte
	_, _, err = svc.PastWatermark(svc.epochID, lh.ID, wrongCfgID)
	if err != ErrConfigurationMismatch {
		t.Fatalf("expected ErrConfigurationMismatch on wrong config ID, got %v", err)
	}

	// 6. CommitWait
	// commitTs earlier than wm should return immediately
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	err = svc.CommitWait(ctx, wm-10, svc.epochID, lh.ID, cfgID)
	if err != nil {
		t.Fatalf("CommitWait for past commitTs should succeed: %v", err)
	}

	// commitTs far in the future with short timeout should timeout
	ctxShort, cancelShort := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancelShort()

	err = svc.CommitWait(ctxShort, targetTime+1_000_000_000, svc.epochID, lh.ID, cfgID)
	if err != ErrDeadlineExceeded {
		t.Fatalf("expected ErrDeadlineExceeded, got %v", err)
	}
}
