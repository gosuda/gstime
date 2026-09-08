package assurance

import (
	"errors"
	"math"
	"testing"

	"gosuda.org/gstime/core"
)

func TestWatermarkIncludesEntireSignedTimeline(t *testing.T) {
	ac := NewAssuranceClock(10_000)
	hull := core.TimeInterval{Earliest: -5_000_000_000_000_001_000, Latest: -4_999_999_999_999_999_000}
	_, err := ac.ProcessFullRound(1000, hull, 0, core.RateScale(core.OneQ48), core.RateScale(core.OneQ48), 1, 10000, 0, 1, 1, 1, [32]byte{}, [32]byte{})
	if err != nil {
		t.Fatal(err)
	}
	state := ac.Snapshot()
	if got := state.PastWatermark(); got != hull.Earliest {
		t.Fatalf("watermark=%d, want first certified lower=%d", got, hull.Earliest)
	}
	if got := state.AdvanceWatermark(hull.Earliest - 1); got != hull.Earliest {
		t.Fatalf("older evidence decreased watermark: %d", got)
	}
}

func TestExpiredRoundCannotCreateWatermark(t *testing.T) {
	ac := NewAssuranceClock(10000)
	_, err := ac.ProcessFullRound(1000, core.TimeInterval{Earliest: 10000, Latest: 11000}, 0, core.RateScale(core.OneQ48), core.RateScale(core.OneQ48), 1, 999, 0, 1, 1, 1, [32]byte{}, [32]byte{})
	if !errors.Is(err, ErrAnchorExpired) {
		t.Fatalf("expired round: %v", err)
	}
	if got := ac.Snapshot().PastWatermark(); got != math.MinInt64 {
		t.Fatalf("rejected evidence raised watermark to %d", got)
	}
}

func TestOldEpochReadersCannotChangeRecoveredWatermark(t *testing.T) {
	ac := NewAssuranceClock(10000)
	publish := func(lo core.GstInstant) {
		t.Helper()
		_, err := ac.ProcessFullRound(1000, core.TimeInterval{Earliest: lo, Latest: lo + 1000}, 0, core.RateScale(core.OneQ48), core.RateScale(core.OneQ48), 1, 10000, 0, 1, 1, 1, [32]byte{}, [32]byte{})
		if err != nil {
			t.Fatal(err)
		}
	}
	publish(10000)
	old := ac.Snapshot()
	ac.TransitionToDesync(core.ReasonOperatorInvalidation)
	publish(1000)
	current := ac.Snapshot()
	if old.AssuranceEpochID == current.AssuranceEpochID {
		t.Fatal("recovery retained the invalidated certainty identity")
	}
	old.AdvanceWatermark(1_000_000)
	if got := current.PastWatermark(); got != 1000 {
		t.Fatalf("stale reader changed recovered watermark: %d", got)
	}
}
