package gstime_test

import (
	"context"
	"testing"

	"gosuda.org/gstime"
	"gosuda.org/gstime/clock"
	"gosuda.org/gstime/config"
	"gosuda.org/gstime/core"
	"gosuda.org/gstime/ntp"
	"gosuda.org/gstime/source"
)

type regressionDomainQuerier map[string]core.TimeInterval

func (q regressionDomainQuerier) QuerySource(_ context.Context, s config.SourceConfig, _ core.RawNanos) (*ntp.MeasurementResult, error) {
	i := q[s.Endpoint]
	return &ntp.MeasurementResult{RawMid: 1_000_000_000, HardInterval: i,
		Center: i.Earliest + (i.Latest-i.Earliest)/2}, nil
}

func TestRegressionPollMustExcludeInconsistentDomain(t *testing.T) {
	a1 := core.TimeInterval{Earliest: 99_999_999_000, Latest: 100_000_001_000}
	a2 := core.TimeInterval{Earliest: 100_049_999_000, Latest: 100_050_001_000}
	wide := core.TimeInterval{Earliest: 99_000_000_000, Latest: 101_000_000_000}
	merged := source.ConsolidateDomainIntervals("a", []core.TimeInterval{a1, a2})
	if !merged.Inconsistent {
		t.Fatal("setup: lower-level consolidation should reject disjoint intervals")
	}
	raw := clock.NewSimulatedRawClock(1_000_000_000)
	lh, err := core.NewLeapHistory(10, nil)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultConfig()
	cfg.Sources = []config.SourceConfig{
		{FaultDomainID: "a", Endpoint: "a1"}, {FaultDomainID: "a", Endpoint: "a2"},
		{FaultDomainID: "b", Endpoint: "b"}, {FaultDomainID: "c", Endpoint: "c"},
	}
	id, err := cfg.ConfigID()
	if err != nil {
		t.Fatal(err)
	}
	svc := gstime.NewClockService(raw, lh, id, cfg.Assurance.MaxWidthNs)
	engine, err := gstime.NewSyncEngine(cfg, svc,
		gstime.WithSourceQuerier(regressionDomainQuerier{"a1": a1, "a2": a2, "b": wide, "c": wide}))
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	err = engine.PollOnce(context.Background())
	now := svc.Now()
	t.Logf("domain a inconsistent=%v PollOnce=%v status=%s eligible=%d", merged.Inconsistent, err, now.Status, now.EligibleDomains)
	if err == nil {
		t.Fatal("inconsistent domain counted toward MinVotingDomains=3; only b and c should remain")
	}
}
