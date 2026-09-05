package gstime_test

import (
	"context"
	"sync/atomic"
	"testing"

	"gosuda.org/gstime"
	"gosuda.org/gstime/clock"
	"gosuda.org/gstime/config"
	"gosuda.org/gstime/core"
	"gosuda.org/gstime/ntp"
)

// Regression fixtures: exact oscillator, bounded samples, independent honest domains.
type regressionReferenceQuerier struct {
	raw           *clock.SimulatedRawClock
	calls         atomic.Int32
	measurements  map[string]*ntp.MeasurementResult
	advanceOnLast core.RawNanos
}

func (q *regressionReferenceQuerier) QuerySource(_ context.Context, src config.SourceConfig, _ core.RawNanos) (*ntp.MeasurementResult, error) {
	m := q.measurements[src.Endpoint]
	if q.calls.Add(1) == int32(len(q.measurements)) {
		q.raw.Advance(q.advanceOnLast)
	}
	return m, nil
}

func TestRegressionPollOnceMustNormalizeSampleTimes(t *testing.T) {
	for _, tc := range []struct {
		name             string
		initialRaw       core.RawNanos
		advance          core.RawNanos
		mids             []core.RawNanos
		centers          []core.GstInstant
		truthAtSelection core.GstInstant
	}{
		{"post_sample_delay", 1_000_000_000, 100_000_000,
			[]core.RawNanos{1_000_000_000, 1_000_000_000, 1_000_000_000},
			[]core.GstInstant{100_000_000_000, 100_000_000_000, 100_000_000_000}, 100_100_000_000},
		{"different_midpoints", 1_000_000_000, 100_000_000,
			[]core.RawNanos{1_000_000_000, 1_050_000_000, 1_100_000_000},
			[]core.GstInstant{100_000_000_000, 100_050_000_000, 100_100_000_000}, 100_100_000_000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := clock.NewSimulatedRawClock(tc.initialRaw)
			raw.SetScaleEnvelope(core.RateScale(core.OneQ48), core.RateScale(core.OneQ48))
			lh, err := core.NewLeapHistory(10, nil)
			if err != nil {
				t.Fatal(err)
			}
			cfg := config.DefaultConfig()
			cfg.Sources = []config.SourceConfig{
				{FaultDomainID: "a", Endpoint: "a"},
				{FaultDomainID: "b", Endpoint: "b"},
				{FaultDomainID: "c", Endpoint: "c"},
			}
			id, err := cfg.ConfigID()
			if err != nil {
				t.Fatal(err)
			}
			svc := gstime.NewClockService(raw, lh, id, cfg.Assurance.MaxWidthNs)
			q := &regressionReferenceQuerier{raw: raw, advanceOnLast: tc.advance, measurements: make(map[string]*ntp.MeasurementResult)}
			for i, src := range cfg.Sources {
				q.measurements[src.Endpoint] = &ntp.MeasurementResult{
					RawMid: tc.mids[i], Center: tc.centers[i], NetworkBound: 1_000,
					HardInterval: core.TimeInterval{Earliest: tc.centers[i] - 1_000, Latest: tc.centers[i] + 1_000},
				}
			}
			engine, err := gstime.NewSyncEngine(cfg, svc, gstime.WithSourceQuerier(q))
			if err != nil {
				t.Fatal(err)
			}
			defer engine.Close()
			if err := engine.PollOnce(context.Background()); err != nil {
				t.Fatalf("honest samples on one exact timeline must agree after propagation: %v", err)
			}
			now := svc.Now()
			t.Logf("status=%s interval=%+v true_time=%d", now.Status, now.Interval, tc.truthAtSelection)
			if now.Interval == nil || now.Interval.Earliest > tc.truthAtSelection || now.Interval.Latest < tc.truthAtSelection {
				t.Fatal("certified interval excludes true time at selection")
			}
		})
	}
}

func TestPollRejectsCachedSampleFromBeforeRound(t *testing.T) {
	raw := clock.NewSimulatedRawClock(1_000_000_000)
	lh, _ := core.NewLeapHistory(10, nil)
	cfg := config.DefaultConfig()
	cfg.Sources = []config.SourceConfig{{FaultDomainID: "a", Endpoint: "a"}, {FaultDomainID: "b", Endpoint: "b"}, {FaultDomainID: "c", Endpoint: "c"}}
	id, _ := cfg.ConfigID()
	svc := gstime.NewClockService(raw, lh, id, cfg.Assurance.MaxWidthNs)
	q := &regressionReferenceQuerier{raw: raw, measurements: map[string]*ntp.MeasurementResult{}}
	for _, src := range cfg.Sources {
		q.measurements[src.Endpoint] = &ntp.MeasurementResult{RawMid: 999_000_000, HardInterval: core.TimeInterval{Earliest: 99_999_000_000, Latest: 100_001_000_000}}
	}
	e, err := gstime.NewSyncEngine(cfg, svc, gstime.WithSourceQuerier(q))
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	if err = e.PollOnce(context.Background()); err == nil {
		t.Fatal("cached evidence without a continuity identity was accepted")
	}
}
