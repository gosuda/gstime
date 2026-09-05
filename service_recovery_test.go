package gstime_test

import (
	"context"
	"gosuda.org/gstime"
	"gosuda.org/gstime/config"
	"sync"
	"testing"
)

func TestHoldoverLimitAndExplicitInvalidation(t *testing.T) {
	raw, svc := regressionAnchoredService(t)
	cfg := config.DefaultConfig()
	cfg.Assurance.MaxHoldoverAgeNs = 1_000_000_000
	cfg.Sources = []config.SourceConfig{{FaultDomainID: "a"}, {FaultDomainID: "b"}, {FaultDomainID: "c"}}
	engine, err := gstime.NewSyncEngine(cfg, svc, gstime.WithSourceQuerier(regressionUnavailableQuerier{}))
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	raw.Advance(500_000_000)
	if err := engine.PollOnce(context.Background()); err == nil {
		t.Fatal("expected unavailable sources")
	}
	if now := svc.Now(); now.Status != gstime.StatusHoldover {
		t.Fatalf("want holdover: %+v", now)
	}
	raw.Advance(500_000_000)
	if now := svc.Now(); now.Status != gstime.StatusHoldover {
		t.Fatalf("inclusive horizon expired early: %+v", now)
	}
	raw.Advance(1)
	if now := svc.Now(); now.Status != gstime.StatusDesync {
		t.Fatalf("holdover cap was ignored: %+v", now)
	}
	r := raw.Read().Raw
	if err := svc.PublishAssuranceRound(r, gstime.TimeInterval{Earliest: 200_000_000_000, Latest: 200_001_000_000}, 1, 3, 2, 1, r+10_000_000_000); err != nil {
		t.Fatal(err)
	}
	if now := svc.Now(); now.Status != gstime.StatusSynced {
		t.Fatalf("fresh round failed to recover: %+v", now)
	}
	if err := svc.Invalidate(); err != nil {
		t.Fatal(err)
	}
	now := svc.Now()
	if now.Status != gstime.StatusDesync || now.Interval != nil {
		t.Fatalf("explicit invalidation ignored: %+v", now)
	}
	if _, status, err := svc.PastWatermark(now.AssuranceEpochID, now.LeapHistoryID, now.ConfigID); err == nil || status != gstime.StatusDesync {
		t.Fatal("watermark ignores explicit invalidation")
	}
}

func TestConcurrentServicePublication(t *testing.T) {
	raw, svc := regressionAnchoredService(t)
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for range 30 {
				r := raw.Read().Raw
				if err := svc.InitializeEstimate(r, 100_000_000_000, 0); err != nil {
					t.Error(err)
				}
				if err := svc.PublishAssuranceRound(r, gstime.TimeInterval{Earliest: 99_999_000_000, Latest: 100_001_000_000}, 1, 3, 2, 1, r+10_000_000_000); err != nil {
					t.Error(err)
				}
			}
		})
		wg.Go(func() {
			for range 100 {
				now := svc.Now()
				if now.Status != gstime.StatusSynced || now.Interval == nil {
					t.Errorf("incoherent read: %+v", now)
				}
				_, status, err := svc.PastWatermark(now.AssuranceEpochID, now.LeapHistoryID, now.ConfigID)
				if err != nil || status != gstime.StatusSynced {
					t.Errorf("incoherent watermark: %s %v", status, err)
				}
			}
		})
	}
	wg.Wait()
}
