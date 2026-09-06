package gstime

import (
	"context"
	"math"
	"reflect"
	"testing"

	"gosuda.org/gstime/clock"
	"gosuda.org/gstime/config"
	"gosuda.org/gstime/core"
	"gosuda.org/gstime/ntp"
	"gosuda.org/gstime/source"
)

type reviewQueryFunc func(context.Context, config.SourceConfig, core.RawNanos) (*ntp.MeasurementResult, error)

func (f reviewQueryFunc) QuerySource(c context.Context, s config.SourceConfig, r core.RawNanos) (*ntp.MeasurementResult, error) {
	return f(c, s, r)
}

func reviewService(t *testing.T, width int64) (*ClockService, config.Config) {
	t.Helper()
	raw := clock.NewSimulatedRawClock(1_000_000_000)
	raw.SetScaleEnvelope(core.RateScale(core.OneQ48), core.RateScale(core.OneQ48))
	lh, err := core.NewLeapHistory(10, nil)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultConfig()
	cfg.Sources = []config.SourceConfig{{FaultDomainID: "a", Endpoint: "a"}, {FaultDomainID: "b", Endpoint: "b"}, {FaultDomainID: "c", Endpoint: "c"}}
	cfg.Assurance.MaxWidthNs = width
	id, err := cfg.ConfigID()
	if err != nil {
		t.Fatal(err)
	}
	return NewClockService(raw, lh, id, width), cfg
}

func TestPollLifecyclePreservesAssurance(t *testing.T) {
	for _, mode := range []string{"closed", "cancelled", "inflight_cancel", "background_close", "source_timeout"} {
		t.Run(mode, func(t *testing.T) {
			svc, cfg := reviewService(t, 32_000_000_000)
			if err := svc.PublishAssuranceRound(1_000_000_000, TimeInterval{Earliest: 99_999_000_000, Latest: 100_001_000_000}, 1, 3, 2, 1, 11_000_000_000); err != nil {
				t.Fatal(err)
			}
			before := svc.publisher.Acquire()
			started := make(chan struct{}, 3)
			q := reviewQueryFunc(func(ctx context.Context, _ config.SourceConfig, _ core.RawNanos) (*ntp.MeasurementResult, error) {
				started <- struct{}{}
				if mode == "source_timeout" {
					return nil, context.DeadlineExceeded
				}
				<-ctx.Done()
				return nil, ctx.Err()
			})
			e, err := NewSyncEngine(cfg, svc, WithSourceQuerier(q))
			if err != nil {
				t.Fatal(err)
			}
			defer e.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch mode {
			case "closed":
				if err = e.Close(); err != nil {
					t.Fatal(err)
				}
				err = e.PollOnce(ctx)
			case "cancelled":
				cancel()
				err = e.PollOnce(ctx)
			case "background_close":
				if err = e.Start(ctx); err != nil {
					t.Fatal(err)
				}
				<-started
				if err = e.Close(); err != nil {
					t.Fatal(err)
				}
			case "inflight_cancel":
				done := make(chan error, 1)
				go func() { done <- e.PollOnce(ctx) }()
				<-started
				cancel()
				err = <-done
			default:
				err = e.PollOnce(ctx)
			}
			if mode != "background_close" && err == nil {
				t.Fatal("expected polling error")
			}
			if mode == "source_timeout" {
				if svc.Now().Status != StatusHoldover {
					t.Fatal("completed failed round must enter HOLDOVER")
				}
			} else if !reflect.DeepEqual(before, svc.publisher.Acquire()) {
				t.Fatalf("lifecycle event mutated snapshot: status=%v", svc.Now().Status)
			}
		})
	}
}

func TestPublishRejectsInvalidHullBeforeMutation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		width int64
		hull  TimeInterval
	}{
		{"oversize", 10, TimeInterval{Earliest: 0, Latest: 100}},
		{"reversed", 1000, TimeInterval{Earliest: 100, Latest: 0}},
		{"signed_span", math.MaxInt64, TimeInterval{Earliest: math.MinInt64, Latest: math.MaxInt64}},
	} {
		for _, public := range []bool{false, true} {
			t.Run(tc.name+map[bool]string{true: "/public", false: "/sync"}[public], func(t *testing.T) {
				svc, _ := reviewService(t, tc.width)
				before := svc.publisher.Acquire()
				est := svc.estimateClock.Snapshot()
				ass := svc.assuranceClock.Snapshot()
				var err error
				if public {
					err = svc.PublishAssuranceRound(1_000_000_000, tc.hull, 1, 3, 2, 1, 2_000_000_000)
				} else {
					r := svc.rawClock.Read()
					lo, hi := svc.rawClock.ScaleEnvelope()
					err = svc.publishSyncRound(r, lo, hi, &source.AssuranceConsensusResult{Hull: tc.hull}, 2_000_000_000)
				}
				if err == nil {
					t.Fatal("invalid hull accepted")
				}
				if !reflect.DeepEqual(est, svc.estimateClock.Snapshot()) || !reflect.DeepEqual(ass, svc.assuranceClock.Snapshot()) || !reflect.DeepEqual(before, svc.publisher.Acquire()) {
					t.Fatal("invalid hull mutated clock/publication state")
				}
			})
		}
	}
}

// An exchange reports RawMid=1s with up to 1ms coordinate error. Its true
// midpoint may precede the exact selection by 1ms even with equal raw labels.
// Constant-rate truth must remain enclosed; the pre-query 100ns read is irrelevant.
func TestPollUsesAcquisitionMidpointBound(t *testing.T) {
	svc, cfg := reviewService(t, 32_000_000_000)
	q := reviewQueryFunc(func(_ context.Context, _ config.SourceConfig, r core.RawNanos) (*ntp.MeasurementResult, error) {
		return &ntp.MeasurementResult{RawMid: r, RawMidReadBound: 1_000_000, HardInterval: core.TimeInterval{Earliest: 100_000_000_000, Latest: 100_000_000_000}}, nil
	})
	e, err := NewSyncEngine(cfg, svc, WithSourceQuerier(q))
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	if err = e.PollOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := svc.Now()
	truth := core.GstInstant(100_001_000_000)
	if !got.HasInterval || got.Interval.Latest < truth || got.Interval.Earliest > truth {
		t.Fatalf("acquisition uncertainty lost: %+v, truth=%d", got.Interval, truth)
	}
}

func TestPublishValidMidpointAtSignedEdges(t *testing.T) {
	for _, hull := range []TimeInterval{{Earliest: math.MinInt64, Latest: math.MinInt64 + 1000}, {Earliest: math.MaxInt64 - 1000, Latest: math.MaxInt64}, {Earliest: -500, Latest: 500}} {
		svc, _ := reviewService(t, 2000)
		r := svc.rawClock.Read()
		lo, hi := svc.rawClock.ScaleEnvelope()
		if err := svc.publishSyncRound(r, lo, hi, &source.AssuranceConsensusResult{Hull: hull}, 2_000_000_000); err != nil {
			t.Fatal(err)
		}
		mapping := svc.estimateClock.Snapshot()
		got, err := mapping.Evaluate(r.Raw)
		if err != nil {
			t.Fatal(err)
		}
		if got != hull.Earliest+500 {
			t.Fatalf("midpoint %d for %+v", got, hull)
		}
	}
}
