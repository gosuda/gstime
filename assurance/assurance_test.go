package assurance

import (
	"errors"
	"testing"

	"gosuda.org/gstime/core"
)

func TestAssuranceClockTransitionsAndConflict(t *testing.T) {
	ac := NewAssuranceClock(32_000_000_000) // 32s cap

	if ac.Snapshot().Status != core.StatusUnanchored {
		t.Fatalf("expected initial StatusUnanchored")
	}

	scaleLow := core.RateScale(core.OneQ48)
	scaleUpp := core.RateScale(core.OneQ48)

	// Round 1: [1000, 2000] at raw 1_000_000_000
	h1 := core.TimeInterval{Earliest: 1000, Latest: 2000}
	int1, err := ac.ProcessFullRound(
		1_000_000_000, h1, 10, scaleLow, scaleUpp, 1, 10_000_000_000,
		1, 3, 2, 1, [32]byte{1}, [32]byte{2},
	)
	if err != nil {
		t.Fatalf("round 1 failed: %v", err)
	}
	if ac.Snapshot().Status != core.StatusSynced {
		t.Fatalf("expected StatusSynced after round 1")
	}
	if int1.Earliest != 1000 || int1.Latest != 2000 {
		t.Fatalf("round 1 interval mismatch: %+v", int1)
	}

	// Round 2 at raw 2_000_000_000 (1s later).
	// Propagated old bound: 1s elapsed -> [1000 + 1e9, 2000 + 1e9].
	// New hull agrees: [1000 + 1e9 - 100, 2000 + 1e9 + 100].
	// Intersection should narrow to [1000 + 1e9, 2000 + 1e9]!
	h2 := core.TimeInterval{Earliest: 1000 + 1_000_000_000 - 100, Latest: 2000 + 1_000_000_000 + 100}
	int2, err := ac.ProcessFullRound(
		2_000_000_000, h2, 10, scaleLow, scaleUpp, 1, 20_000_000_000,
		1, 3, 2, 1, [32]byte{1}, [32]byte{2},
	)
	if err != nil {
		t.Fatalf("round 2 failed: %v", err)
	}
	if int2.Earliest != 1_000_000_980 || int2.Latest != 1_000_002_020 {
		t.Fatalf("round 2 intersection mismatch: %+v", int2)
	}

	// Round 3: ASSURANCE CONFLICT!
	// Disjoint new hull (e.g. faulty sources or step): [5000, 6000] at raw 3_000_000_000
	h3 := core.TimeInterval{Earliest: 5000, Latest: 6000}
	_, err = ac.ProcessFullRound(
		3_000_000_000, h3, 10, scaleLow, scaleUpp, 1, 30_000_000_000,
		1, 3, 2, 1, [32]byte{1}, [32]byte{2},
	)
	if err != ErrAssuranceConflict {
		t.Fatalf("expected ErrAssuranceConflict, got %v", err)
	}
	if ac.Snapshot().Status != core.StatusDesync {
		t.Fatalf("expected StatusDesync after conflict, got %s", ac.Snapshot().Status)
	}
	if ac.Snapshot().Reason != core.ReasonAssuranceConflict {
		t.Fatalf("expected ReasonAssuranceConflict, got %s", ac.Snapshot().Reason)
	}
}

func TestHoldoverAndWatermarkMonotonicity(t *testing.T) {
	ac := NewAssuranceClock(32_000_000_000)

	scaleLow := core.RateScale(core.OneQ48)
	scaleUpp := core.RateScale(core.OneQ48)

	// Round 1
	h1 := core.TimeInterval{Earliest: 1_000_000_000, Latest: 2_000_000_000}
	_, err := ac.ProcessFullRound(
		1_000_000_000, h1, 10, scaleLow, scaleUpp, 1, 100_000_000_000,
		1, 3, 2, 1, [32]byte{1}, [32]byte{2},
	)
	if err != nil {
		t.Fatalf("round 1 failed: %v", err)
	}

	wm0 := ac.Snapshot().PastWatermark
	if wm0 != 1_000_000_000 {
		t.Fatalf("initial watermark mismatch: got %d want 1_000_000_000", wm0)
	}

	// Transition to holdover
	ac.TransitionToHoldover(core.ReasonInsufficientDomains)
	if ac.Snapshot().Status != core.StatusHoldover {
		t.Fatalf("expected StatusHoldover")
	}

	// Evaluate in holdover 5s later
	invHoldover, status, _, err := ac.EvaluateAt(6_000_000_000, 10, 1)
	if err != nil || status != core.StatusHoldover {
		t.Fatalf("holdover evaluation failed: err=%v status=%s", err, status)
	}

	// In holdover, interval propagates forward: Earliest >= 1_000_000_000 + 5_000_000_000 - 20
	if invHoldover.Earliest < 5_999_999_900 {
		t.Fatalf("expected propagated earliest ~6s, got %d", invHoldover.Earliest)
	}
}

func TestAssuranceClock_VMSnapshotRawTimeReversal(t *testing.T) {
	ac := NewAssuranceClock(32_000_000_000)
	scaleLow := core.RateScale(core.OneQ48)
	scaleUpp := core.RateScale(core.OneQ48)

	// Publish anchor at raw = 50,000,000,000 (50s)
	h := core.TimeInterval{Earliest: 1_700_000_000 * 1_000_000_000, Latest: 1_700_000_000*1_000_000_000 + 10_000}
	_, err := ac.ProcessFullRound(
		50_000_000_000, h, 10, scaleLow, scaleUpp, 1, 100_000_000_000,
		1, 3, 2, 1, [32]byte{1}, [32]byte{2},
	)
	if err != nil {
		t.Fatalf("ProcessFullRound failed: %v", err)
	}

	// Normal forward evaluation at raw = 60,000,000,000 (60s)
	inv60, status60, _, err := ac.EvaluateAt(60_000_000_000, 10, 1)
	if err != nil || status60 != core.StatusSynced || inv60.Earliest == 0 {
		t.Fatalf("normal evaluation failed: %v", err)
	}

	// VM snapshot rollback: raw counter jumps backward to 30,000,000,000 (30s), earlier than anchor (50s)
	inv30, status30, reason30, err := ac.EvaluateAt(30_000_000_000, 10, 1)
	if err == nil {
		t.Fatalf("expected error when raw time jumps earlier than anchor, got nil")
	}
	if !errors.Is(err, ErrRawEarlierThanAnchor) {
		t.Fatalf("expected ErrRawEarlierThanAnchor, got %v", err)
	}
	if status30 != core.StatusDesync {
		t.Fatalf("expected StatusDesync on raw rewind, got %s", status30)
	}
	if reason30 != core.ReasonRawDiscontinuity {
		t.Fatalf("expected ReasonRawDiscontinuity, got %s", reason30)
	}
	if inv30 != (core.TimeInterval{}) {
		t.Fatalf("expected empty interval on raw rewind, got %+v", inv30)
	}
}
