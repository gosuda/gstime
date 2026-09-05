package gstime_test

import (
	"testing"

	"gosuda.org/gstime"
	"gosuda.org/gstime/clock"
	"gosuda.org/gstime/core"
)

func TestRegressionRollbackAboveAnchorWithoutTokenChange(t *testing.T) {
	// A deterministic counter-rewind model, NOT an actual VM restore test.
	// The production SystemRawClock also supplies a constant continuity token.
	raw := clock.NewSimulatedRawClock(1_000_000_000)
	raw.SetScaleEnvelope(core.RateScale(core.OneQ48), core.RateScale(core.OneQ48))
	lh, err := core.NewLeapHistory(10, nil)
	if err != nil {
		t.Fatal(err)
	}
	svc := gstime.NewClockService(raw, lh, [32]byte{}, 32_000_000_000)
	if err := svc.InitializeEstimate(1_000_000_000, 100_000_000_000, 0); err != nil {
		t.Fatal(err)
	}
	if err := svc.PublishAssuranceRound(1_000_000_000,
		core.TimeInterval{Earliest: 99_999_999_000, Latest: 100_000_001_000},
		1, 3, 2, 1, 30_000_000_000); err != nil {
		t.Fatal(err)
	}
	raw.Advance(10_000_000_000)
	pre := svc.Now()
	if pre.Status != gstime.StatusSynced {
		t.Fatalf("setup: got %s", pre.Status)
	}
	// Raw moves from 11s to 6s, still after the 1s anchor. Real SI time does
	// not move backwards. No token notification is injected by the fixture.
	raw.Rewind(5_000_000_000)
	now := svc.Now()
	t.Logf("pre=%+v post=%+v status=%s token=%d", pre.Interval, now.Interval, now.Status, raw.Read().ContinuityToken)
	if now.Status != gstime.StatusDesync {
		t.Fatal("README's unconditional rollback fail-fast guarantee requires detection absent from this path")
	}
}
