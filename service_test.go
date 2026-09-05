package gstime

import (
	"context"
	"testing"
	"time"

	"gosuda.org/gstime/clock"
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

func TestClockService_DSTSimulator(t *testing.T) {
	raw := clock.NewSimulatedRawClock(10_000_000_000)
	lh, _ := NewLeapHistory(10, []LeapEntry{})
	var cfgID [32]byte
	cfgID[0] = 0x55

	svc := NewClockService(raw, lh, cfgID, 32_000_000_000)

	targetTime := GstInstant(1_700_000_000 * 1_000_000_000)
	_ = svc.InitializeEstimate(10_000_000_000, targetTime, 0)

	hull := TimeInterval{Earliest: targetTime - 1_000, Latest: targetTime + 1_000}
	err := svc.PublishAssuranceRound(10_000_000_000, hull, 1, 3, 2, 1, 100_000_000_000)
	if err != nil {
		t.Fatalf("PublishAssuranceRound failed: %v", err)
	}

	pub0 := svc.NowPublicAssured()
	if pub0.Status != StatusSynced {
		t.Fatalf("expected StatusSynced, got %s", pub0.Status)
	}
	if pub0.Center != targetTime {
		t.Fatalf("expected Center %d, got %d", targetTime, pub0.Center)
	}

	// 1. Simulate clock running ahead by 1 hour (e.g. DST Spring Forward excursion)
	hourNs := int64(3600 * 1_000_000_000)
	rawCur := raw.Read().Raw
	_ = svc.InitializeEstimate(rawCur, targetTime+GstInstant(hourNs), 0)

	pubAhead := svc.NowPublicAssured()
	if pubAhead.Center < targetTime+GstInstant(hourNs) {
		t.Fatalf("expected pubAhead.Center at targetTime+1h, got %d", pubAhead.Center)
	}

	// 2. Simulate DST "Fall Back" (true civil/consensus time is at targetTime, 1 hour behind pubAhead)
	_ = svc.InitializeEstimate(rawCur, targetTime, 0)

	// Invariant: Public clock MUST NOT step backwards despite 1-hour backward shift
	pubClamped := svc.NowPublicAssured()
	if pubClamped.Center < pubAhead.Center {
		t.Fatalf("public clock regressed on DST fall-back: pubAhead=%d pubClamped=%d",
			pubAhead.Center, pubClamped.Center)
	}

	// Because pubClamped.Center is 3600s away from hull at targetTime,
	// public epsilon is ~3600s, exceeding the 16s cap -> returns StatusDesync (Section 7.8)
	if pubClamped.Status != StatusDesync {
		t.Fatalf("expected StatusDesync due to 1-hour DST clamp debt exceeding cap, got %s", pubClamped.Status)
	}
	if pubClamped.Reason != ReasonPublicBoundTooWide {
		t.Fatalf("expected ReasonPublicBoundTooWide, got %s", pubClamped.Reason)
	}

	// 3. Recover from DST step: true time advances by 1 hour, catching up to clamped public clock
	raw.Advance(RawNanos(hourNs + 1_000_000_000))
	rawNew := raw.Read().Raw
	newTarget := targetTime + GstInstant(hourNs+1_000_000_000)
	_ = svc.InitializeEstimate(rawNew, newTarget, 0)
	newHull := TimeInterval{Earliest: newTarget - 1_000, Latest: newTarget + 1_000}
	_ = svc.PublishAssuranceRound(rawNew, newHull, 1, 3, 2, 1, rawNew+100_000_000_000)

	pubRecovered := svc.NowPublicAssured()
	if pubRecovered.Status != StatusSynced {
		t.Fatalf("expected recovery to StatusSynced after true time caught up, got %s (reason=%s)",
			pubRecovered.Status, pubRecovered.Reason)
	}
	if pubRecovered.Center < pubClamped.Center {
		t.Fatalf("monotonicity violated on recovery: pubClamped=%d pubRecovered=%d",
			pubClamped.Center, pubRecovered.Center)
	}
}

func TestClockService_VMSnapshotJump(t *testing.T) {
	raw := clock.NewSimulatedRawClock(10_000_000_000)
	lh, _ := NewLeapHistory(10, []LeapEntry{})
	var cfgID [32]byte
	cfgID[0] = 0x66

	svc := NewClockService(raw, lh, cfgID, 32_000_000_000)

	targetTime := GstInstant(1_700_000_000 * 1_000_000_000)
	_ = svc.InitializeEstimate(10_000_000_000, targetTime, 0)

	hull := TimeInterval{Earliest: targetTime - 50_000, Latest: targetTime + 50_000}
	_ = svc.PublishAssuranceRound(10_000_000_000, hull, 1, 3, 2, 1, 50_000_000_000)

	pPre := svc.NowPublicAssured().Center

	// 1. VM Snapshot Revert: hardware raw clock rewinds and continuity token changes
	raw.SetContinuityToken(2)

	nowRevert := svc.Now()
	if nowRevert.Status != StatusDesync {
		t.Fatalf("expected StatusDesync on continuity token mismatch, got %s", nowRevert.Status)
	}
	if nowRevert.Reason != ReasonRawDiscontinuity {
		t.Fatalf("expected ReasonRawDiscontinuity, got %s", nowRevert.Reason)
	}

	pRevert := svc.NowPublicAssured().Center
	if pRevert < pPre {
		t.Fatalf("public clock regressed on VM snapshot rollback: pPre=%d pRevert=%d", pPre, pRevert)
	}

	// 2. VM Suspend & Resume: time advances far beyond anchor validity horizon
	raw.Advance(7200 * 1_000_000_000) // +2 hours

	nowExpired := svc.Now()
	if nowExpired.Status != StatusDesync {
		t.Fatalf("expected StatusDesync on anchor expiration after VM suspend, got %s", nowExpired.Status)
	}

	pSuspend := svc.NowPublicAssured().Center
	if pSuspend < pRevert {
		t.Fatalf("public clock regressed on VM suspend: pRevert=%d pSuspend=%d", pRevert, pSuspend)
	}

	// 3. VM Resume re-synchronization with fresh consensus round
	rawNow := raw.Read().Raw
	resumeTarget := targetTime + 7200*1_000_000_000
	_ = svc.InitializeEstimate(rawNow, resumeTarget, 0)
	resumeHull := TimeInterval{Earliest: resumeTarget - 100, Latest: resumeTarget + 100}
	err := svc.PublishAssuranceRound(rawNow, resumeHull, 1, 3, 2, 1, rawNow+100_000_000_000)
	if err != nil {
		t.Fatalf("re-synchronization failed: %v", err)
	}

	nowRecovered := svc.Now()
	if nowRecovered.Status != StatusSynced {
		t.Fatalf("expected StatusSynced after VM re-sync, got %s", nowRecovered.Status)
	}
	pubRecovered := svc.NowPublicAssured()
	if pubRecovered.Status != StatusSynced {
		t.Fatalf("expected public StatusSynced after VM re-sync, got %s", pubRecovered.Status)
	}
	if pubRecovered.Center < pSuspend {
		t.Fatalf("public clock regressed after re-sync: pSuspend=%d pubRecovered=%d",
			pSuspend, pubRecovered.Center)
	}
}

func TestClockService_VMSnapshotTimeReversalAndFreeze(t *testing.T) {
	raw := clock.NewSimulatedRawClock(50_000_000_000) // start at 50s
	lh, _ := NewLeapHistory(10, []LeapEntry{})
	var cfgID [32]byte
	cfgID[0] = 0x77

	svc := NewClockService(raw, lh, cfgID, 32_000_000_000)

	targetTime := GstInstant(1_700_000_000 * 1_000_000_000)
	_ = svc.InitializeEstimate(50_000_000_000, targetTime, 0)

	hull := TimeInterval{Earliest: targetTime - 1_000, Latest: targetTime + 1_000}
	err := svc.PublishAssuranceRound(50_000_000_000, hull, 1, 3, 2, 1, 100_000_000_000)
	if err != nil {
		t.Fatalf("PublishAssuranceRound failed: %v", err)
	}

	// Run forward for 20 seconds
	raw.Advance(20_000_000_000) // now at 70s
	pubPreRollback := svc.NowPublicAssured()
	if pubPreRollback.Status != StatusSynced {
		t.Fatalf("expected StatusSynced at 70s, got %s", pubPreRollback.Status)
	}
	highWatermark := pubPreRollback.Center

	// === VM SNAPSHOT TIME REVERSAL ===
	// The hypervisor restores an earlier snapshot: the physical monotonic counter
	// rewinds by 40 seconds (from 70s back to 30s), placing it earlier than anchor raw (50s)!
	raw.Rewind(40_000_000_000) // now at 30s!

	// 1. Assurance must immediately reject the rewound time
	nowReversal := svc.Now()
	if nowReversal.Status != StatusDesync {
		t.Fatalf("expected StatusDesync on raw time reversal, got %s", nowReversal.Status)
	}
	if nowReversal.Reason != ReasonRawDiscontinuity {
		t.Fatalf("expected ReasonRawDiscontinuity, got %s", nowReversal.Reason)
	}
	if nowReversal.Interval != nil {
		t.Fatalf("expected nil interval on raw reversal, got %+v", nowReversal.Interval)
	}

	// 2. Public clock MUST NOT step backward despite 40-second backward raw leap
	pubReversal := svc.NowPublicAssured()
	if pubReversal.Center < highWatermark {
		t.Fatalf("CRITICAL: Public clock jumped backward on VM snapshot reversal: high=%d got=%d (step=%d ns)",
			highWatermark, pubReversal.Center, pubReversal.Center-highWatermark)
	}
	if pubReversal.Status != StatusDesync {
		t.Fatalf("expected StatusDesync on public view, got %s", pubReversal.Status)
	}
	if pubReversal.Reason != ReasonRawDiscontinuity {
		t.Fatalf("expected ReasonRawDiscontinuity, got %s", pubReversal.Reason)
	}

	// 3. Monotonic non-decreasing progression as VM runs forward in rewound timeline
	var lastCenter = pubReversal.Center
	for step := int64(1); step <= 10; step++ {
		raw.Advance(1_000_000_000) // advance 1s each step (31s, 32s, ... 40s)
		pubStep := svc.NowPublicAssured()
		if pubStep.Center < lastCenter {
			t.Fatalf("monotonicity violated in restored timeline at step %d: last=%d curr=%d",
				step, lastCenter, pubStep.Center)
		}
		if pubStep.Center < highWatermark {
			t.Fatalf("public clock slipped below pre-rollback high watermark: high=%d got=%d",
				highWatermark, pubStep.Center)
		}
		lastCenter = pubStep.Center
	}

	// 4. Synchronizer re-anchors to current restored timeline at 40s
	rawRestored := raw.Read().Raw                    // 40s
	newTimelineTarget := targetTime - 10_000_000_000 // true time in restored timeline
	_ = svc.InitializeEstimate(rawRestored, newTimelineTarget, 0)
	newHull := TimeInterval{Earliest: newTimelineTarget - 1_000, Latest: newTimelineTarget + 1_000}
	err = svc.PublishAssuranceRound(rawRestored, newHull, 1, 3, 2, 1, rawRestored+100_000_000_000)
	if err != nil {
		t.Fatalf("failed to re-anchor in restored timeline: %v", err)
	}

	// Core assurance recovers immediately in the restored timeline
	nowRestored := svc.Now()
	if nowRestored.Status != StatusSynced {
		t.Fatalf("expected StatusSynced after re-anchoring, got %s (reason=%s)",
			nowRestored.Status, nowRestored.Reason)
	}

	// Public clock remains frozen/clamped at highWatermark until restored timeline catches up
	pubRestored := svc.NowPublicAssured()
	if pubRestored.Center < highWatermark {
		t.Fatalf("public clock regressed after re-anchoring: high=%d got=%d", highWatermark, pubRestored.Center)
	}

	// 5. When restored timeline finally advances past highWatermark (e.g. 50s later)
	advanceCatchUp := RawNanos(50_000_000_000)
	raw.Advance(advanceCatchUp)
	rawCaughtUp := raw.Read().Raw
	caughtUpTarget := newTimelineTarget + GstInstant(advanceCatchUp)
	_ = svc.InitializeEstimate(rawCaughtUp, caughtUpTarget, 0)
	caughtUpHull := TimeInterval{Earliest: caughtUpTarget - 1_000, Latest: caughtUpTarget + 1_000}
	_ = svc.PublishAssuranceRound(rawCaughtUp, caughtUpHull, 1, 3, 2, 1, rawCaughtUp+100_000_000_000)

	pubCaughtUp := svc.NowPublicAssured()
	if pubCaughtUp.Status != StatusSynced {
		t.Fatalf("expected StatusSynced after catching up, got %s", pubCaughtUp.Status)
	}
	if pubCaughtUp.Center <= highWatermark {
		t.Fatalf("expected public clock to advance past highWatermark after catch up: high=%d got=%d",
			highWatermark, pubCaughtUp.Center)
	}
}
