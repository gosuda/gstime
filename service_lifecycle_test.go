package gstime_test

import (
	"context"
	"errors"
	"testing"

	"gosuda.org/gstime"
	"gosuda.org/gstime/clock"
	"gosuda.org/gstime/config"
	"gosuda.org/gstime/core"
	"gosuda.org/gstime/ntp"
)

type regressionUnavailableQuerier struct{}

func (regressionUnavailableQuerier) QuerySource(context.Context, config.SourceConfig, core.RawNanos) (*ntp.MeasurementResult, error) {
	return nil, errors.New("fixture: upstream unavailable")
}

func regressionAnchoredService(t *testing.T) (*clock.SimulatedRawClock, *gstime.ClockService) {
	t.Helper()
	raw := clock.NewSimulatedRawClock(1_000_000_000)
	lh, err := core.NewLeapHistory(10, nil)
	if err != nil {
		t.Fatal(err)
	}
	svc := gstime.NewClockService(raw, lh, [32]byte{}, 32_000_000_000)
	if err := svc.InitializeEstimate(1_000_000_000, 100_000_000_000, 0); err != nil {
		t.Fatal(err)
	}
	if err := svc.PublishAssuranceRound(1_000_000_000,
		core.TimeInterval{Earliest: 99_999_000_000, Latest: 100_001_000_000},
		1, 3, 2, 1, 2_000_000_000); err != nil {
		t.Fatal(err)
	}
	return raw, svc
}

func TestRegressionFailedPollMustEnterHoldover(t *testing.T) {
	_, svc := regressionAnchoredService(t)
	cfg := config.DefaultConfig()
	cfg.Sources = []config.SourceConfig{
		{FaultDomainID: "a", Endpoint: "a"}, {FaultDomainID: "b", Endpoint: "b"}, {FaultDomainID: "c", Endpoint: "c"},
	}
	engine, err := gstime.NewSyncEngine(cfg, svc, gstime.WithSourceQuerier(regressionUnavailableQuerier{}))
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	if err := engine.PollOnce(context.Background()); err == nil {
		t.Fatal("expected poll failure")
	}
	now := svc.Now()
	t.Logf("after loss of every source: status=%s reason=%v", now.Status, now.Reason)
	if now.Status != gstime.StatusHoldover {
		t.Fatal("valid anchor must be marked HOLDOVER after failed round")
	}
}

func TestRegressionDesyncMustBeConsistentAcrossAPIs(t *testing.T) {
	for _, failure := range []string{"expired", "continuity_changed"} {
		t.Run(failure, func(t *testing.T) {
			raw, svc := regressionAnchoredService(t)
			before := svc.Now()
			if failure == "expired" {
				raw.Advance(2_000_000_000)
			} else {
				raw.SetContinuityToken(2)
			}
			now := svc.Now()
			if now.Status != gstime.StatusDesync {
				t.Fatalf("setup: want DESYNC, got %s", now.Status)
			}
			wm, status, err := svc.PastWatermark(before.AssuranceEpochID, before.LeapHistoryID, before.ConfigID)
			t.Logf("Now=%s/%v PastWatermark=%d/%s err=%v", now.Status, now.Reason, wm, status, err)
			if err == nil || status != gstime.StatusDesync {
				t.Error("PastWatermark reports SYNCED while Now detects DESYNC")
			}
			err = svc.CommitWait(context.Background(), 99_998_000_000,
				before.AssuranceEpochID, before.LeapHistoryID, before.ConfigID)
			t.Logf("CommitWait after %s: %v", failure, err)
			if err == nil {
				t.Error("CommitWait succeeds despite current DESYNC")
			}
		})
	}
}
