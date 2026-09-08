package gstime

import (
	"context"
	"errors"
	"math"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"gosuda.org/gstime/clock"
	"gosuda.org/gstime/config"
	"gosuda.org/gstime/core"
	"gosuda.org/gstime/ntp"
)

func remediationService(t *testing.T, raw clock.RawClock, cfg config.Config) *ClockService {
	t.Helper()
	id, err := cfg.ConfigID()
	if err != nil {
		t.Fatal(err)
	}
	leaps, err := core.NewLeapHistory(10, nil)
	if err != nil {
		t.Fatal(err)
	}
	return NewClockService(raw, leaps, id, cfg.Assurance.MaxWidthNs)
}

func TestAssuredReadsAndReadyCommitWaitAllocateNothing(t *testing.T) {
	svc, _ := reviewService(t, 32_000_000_000)
	if err := svc.InitializeEstimate(1_000_000_000, 100_000_000_000, 0); err != nil {
		t.Fatal(err)
	}
	if err := svc.PublishAssuranceRound(1_000_000_000, TimeInterval{Earliest: 99_999_000_000, Latest: 100_001_000_000}, 1, 3, 2, 1, 11_000_000_000); err != nil {
		t.Fatal(err)
	}
	now := svc.Now()
	utc, err := svc.NowUtc(now.LeapHistoryID)
	if err != nil || !utc.HasEstimate || !utc.HasInterval || utc.Status != StatusSynced {
		t.Fatalf("UTC reading: %+v %v", utc, err)
	}
	for name, read := range map[string]func(){
		"Now":       func() { _ = svc.Now() },
		"Public":    func() { _ = svc.NowPublicAssured() },
		"Utc":       func() { _, _ = svc.NowUtc(now.LeapHistoryID) },
		"Unix":      func() { _, _ = svc.NowUnixProjection() },
		"After":     func() { _, _, _ = svc.After(100_000_000_000) },
		"Before":    func() { _, _, _ = svc.Before(100_000_000_000) },
		"Watermark": func() { _, _, _ = svc.PastWatermark(now.AssuranceEpochID, now.LeapHistoryID, now.ConfigID) },
		"CommitReady": func() {
			if err := svc.CommitWait(context.Background(), 99_000_000_000, now.AssuranceEpochID, now.LeapHistoryID, now.ConfigID); err != nil {
				panic(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			if got := testing.AllocsPerRun(1000, read); got != 0 {
				t.Fatalf("allocs/call=%v", got)
			}
		})
	}
}

func TestCommitWaitAdvancesWithoutAnotherPoll(t *testing.T) {
	raw := clock.NewSimulatedRawClock(1_000_000_000)
	cfg := config.DefaultConfig()
	svc := remediationService(t, raw, cfg)
	if err := svc.InitializeEstimate(raw.Read().Raw, 100_000_000_000, 0); err != nil {
		t.Fatal(err)
	}
	if err := svc.PublishAssuranceRound(raw.Read().Raw, TimeInterval{Earliest: 99_999_999_000, Latest: 100_000_001_000}, 0, 1, 1, 1, 11_000_000_000); err != nil {
		t.Fatal(err)
	}
	before := svc.Now()
	raw.Advance(1_000_000_000)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := svc.CommitWait(ctx, before.Interval.Latest, before.AssuranceEpochID, before.LeapHistoryID, before.ConfigID); err != nil {
		t.Fatal(err)
	}
	if err := svc.Invalidate(); err != nil {
		t.Fatal(err)
	}
	if err := svc.PublishAssuranceRound(raw.Read().Raw, TimeInterval{Earliest: 100_999_999_000, Latest: 101_000_001_000}, 0, 1, 1, 1, 11_000_000_000); err != nil {
		t.Fatal(err)
	}
	if err := svc.CommitWait(ctx, 0, before.AssuranceEpochID, before.LeapHistoryID, before.ConfigID); !errors.Is(err, ErrConfigurationMismatch) {
		t.Fatalf("old epoch wait resumed: %v", err)
	}
}

func TestEngineRejectsDifferentEffectiveConfiguration(t *testing.T) {
	svc, cfg := reviewService(t, 32_000_000_000)
	cfg.Assurance.FaultBudget = 0
	cfg.Assurance.MinVotingDomains = 1
	cfg.Assurance.MinHonestCoverage = 1
	if _, err := NewSyncEngine(cfg, svc); !errors.Is(err, ErrConfigurationMismatch) {
		t.Fatalf("configuration mismatch: %v", err)
	}
}

func TestCloseAwaitsManualPollQueries(t *testing.T) {
	svc, cfg := reviewService(t, 32_000_000_000)
	started := make(chan struct{}, len(cfg.Sources))
	var active atomic.Int32
	q := reviewQueryFunc(func(ctx context.Context, _ config.SourceConfig, _ core.RawNanos) (*ntp.MeasurementResult, error) {
		active.Add(1)
		defer active.Add(-1)
		started <- struct{}{}
		<-ctx.Done()
		return nil, ctx.Err()
	})
	e, err := NewSyncEngine(cfg, svc, WithSourceQuerier(q))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- e.PollOnce(t.Context()) }()
	for range cfg.Sources {
		<-started
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	if got := active.Load(); got != 0 {
		t.Fatalf("Close left %d active queries", got)
	}
	if err := <-done; err == nil {
		t.Fatal("closed poll succeeded")
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
}

const measurementEpoch GstInstant = 1_704_067_200_000_000_000

func newMeasurementPeer(t *testing.T, span RawNanos, change func(*ntp.Packet, *ClockService, *clock.SimulatedRawClock) error) (*ClockService, *SyncEngine) {
	t.Helper()
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	raw := clock.NewSimulatedRawClock(1_000_000_000)
	raw.SetScaleEnvelope(core.RateScale(core.OneQ48), core.RateScale(core.OneQ48+core.RateFromPpmUpper(200)))
	cfg := config.DefaultConfig()
	cfg.Assurance.FaultBudget, cfg.Assurance.MinVotingDomains, cfg.Assurance.MinHonestCoverage = 0, 1, 1
	cfg.Sources = []config.SourceConfig{{FaultDomainID: "one", Endpoint: conn.LocalAddr().String()}}
	svc := remediationService(t, raw, cfg)
	if err := svc.InitializeEstimate(raw.Read().Raw, measurementEpoch, 0); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		buf := make([]byte, 1024)
		if err := conn.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
			done <- err
			return
		}
		n, peer, err := conn.ReadFrom(buf)
		if err != nil {
			done <- err
			return
		}
		req, err := ntp.ParsePacket(buf[:n])
		if err != nil {
			done <- err
			return
		}
		raw.Advance(span)
		stamp := ntp.EncodeTimestamp(uint32(1_704_067_200+ntp.NtpToUnixOffset), 0)
		response := ntp.Packet{Version: 4, Mode: 4, Stratum: 1, OrigTimestamp: req.TxTimestamp, RecvTimestamp: stamp, TxTimestamp: stamp}
		if change != nil {
			if err := change(&response, svc, raw); err != nil {
				done <- err
				return
			}
		}
		_, err = conn.WriteTo(response.Encode(), peer)
		done <- err
	}()
	t.Cleanup(func() {
		_ = conn.Close()
		if err := <-done; err != nil && !errors.Is(err, net.ErrClosed) {
			t.Error(err)
		}
	})
	e, err := NewSyncEngine(cfg, svc)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := e.Close(); err != nil {
			t.Error(err)
		}
	})
	return svc, e
}

func TestNetworkMeasurementUsesQualifiedScale(t *testing.T) {
	svc, engine := newMeasurementPeer(t, 1_000_000_000, nil)
	if err := engine.PollOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	got := svc.Now()
	truth := measurementEpoch + 1_000_200_000
	if !got.HasInterval || got.Interval.Earliest > truth || got.Interval.Latest < truth {
		t.Fatalf("truth %d outside %+v", truth, got)
	}
}

func TestNetworkMeasurementRejectsWrongOrigin(t *testing.T) {
	_, engine := newMeasurementPeer(t, 1_000_000_000, func(response *ntp.Packet, _ *ClockService, _ *clock.SimulatedRawClock) error {
		response.OrigTimestamp[0] ^= 1
		return nil
	})
	if err := engine.PollOnce(t.Context()); !errors.Is(err, errOriginMismatch) {
		t.Fatalf("origin: %v", err)
	}
}

func TestNetworkMeasurementRejectsMappingStep(t *testing.T) {
	_, engine := newMeasurementPeer(t, 200_000_000, func(_ *ntp.Packet, svc *ClockService, raw *clock.SimulatedRawClock) error {
		return svc.InitializeEstimate(raw.Read().Raw, measurementEpoch, 0)
	})
	if err := engine.PollOnce(t.Context()); !errors.Is(err, errMappingChanged) {
		t.Fatalf("step: %v", err)
	}
}

func TestControlUsesPrimaryComponentWithoutDiscardingHull(t *testing.T) {
	svc, cfg := reviewService(t, 32_000_000_000)
	intervals := map[string]TimeInterval{"a": {Earliest: 100_000_000_000, Latest: 102_000_000_000}, "b": {Earliest: 101_000_000_000, Latest: 105_000_000_000}, "c": {Earliest: 104_000_000_000, Latest: 106_000_000_000}}
	q := reviewQueryFunc(func(_ context.Context, src config.SourceConfig, raw core.RawNanos) (*ntp.MeasurementResult, error) {
		return &ntp.MeasurementResult{RawMid: raw, HardInterval: intervals[src.Endpoint]}, nil
	})
	e, err := NewSyncEngine(cfg, svc, WithSourceQuerier(q))
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	if err := e.PollOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	got := svc.Now()
	if !got.HasEstimate || got.Estimate < 101_000_000_000 || got.Estimate > 102_000_001_000 {
		t.Fatalf("estimate not in primary: %+v", got)
	}
	if !got.HasInterval || got.Interval.Earliest > 101_000_000_000 || got.Interval.Latest < 105_000_000_000 {
		t.Fatalf("full hull lost: %+v", got)
	}
	if err := e.PollOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	if next := svc.Now(); next.Estimate != got.Estimate {
		t.Fatalf("poll stepped estimate: %d -> %d", got.Estimate, next.Estimate)
	}
}

func TestControlLearnsSourceRateAndPreservesContinuity(t *testing.T) {
	raw := clock.NewSimulatedRawClock(1_000_000_000)
	raw.SetScaleEnvelope(core.RateScale(core.OneQ48+core.RateFromPpmLower(-200)), core.RateScale(core.OneQ48+core.RateFromPpmUpper(200)))
	cfg := config.DefaultConfig()
	cfg.Sources = []config.SourceConfig{{FaultDomainID: "a", Endpoint: "a"}, {FaultDomainID: "b", Endpoint: "b"}, {FaultDomainID: "c", Endpoint: "c"}}
	svc := remediationService(t, raw, cfg)
	const epoch GstInstant = 1_704_067_200_000_000_000
	truthAt := func(r RawNanos) GstInstant {
		elapsed := int64(r - 1_000_000_000)
		return epoch + GstInstant(elapsed+elapsed/10000)
	}
	q := reviewQueryFunc(func(_ context.Context, _ config.SourceConfig, r RawNanos) (*ntp.MeasurementResult, error) {
		center := truthAt(r)
		return &ntp.MeasurementResult{HasEstimate: true, RawMid: r, Center: center, NetworkBound: 2_000_000, HardInterval: TimeInterval{Earliest: center - 2_000_000, Latest: center + 2_000_000}}, nil
	})
	e, err := NewSyncEngine(cfg, svc, WithSourceQuerier(q))
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	for round := range 3 {
		before := svc.Now()
		if err := e.PollOnce(t.Context()); err != nil {
			t.Fatal(err)
		}
		after := svc.Now()
		if before.HasEstimate && after.Estimate != before.Estimate {
			t.Fatalf("round stepped %d -> %d", before.Estimate, after.Estimate)
		}
		if round < 2 {
			raw.Advance(1_000_000_000)
		}
	}
	raw.Advance(10_000_000_000)
	got := svc.Now()
	want := truthAt(raw.Read().Raw)
	if !got.HasEstimate || math.Abs(float64(got.Estimate-want)) > 10 {
		t.Fatalf("rate learning estimate=%d want=%d", got.Estimate, want)
	}
}

func TestEngineOwnsItsSourceConfiguration(t *testing.T) {
	svc, cfg := reviewService(t, 32_000_000_000)
	query := reviewQueryFunc(func(_ context.Context, _ config.SourceConfig, raw core.RawNanos) (*ntp.MeasurementResult, error) {
		return &ntp.MeasurementResult{RawMid: raw, HardInterval: TimeInterval{Earliest: 100_000_000_000, Latest: 100_001_000_000}}, nil
	})
	engine, err := NewSyncEngine(cfg, svc, WithSourceQuerier(query))
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	cfg.Sources[1].FaultDomainID = cfg.Sources[0].FaultDomainID
	cfg.Sources[2].FaultDomainID = cfg.Sources[0].FaultDomainID
	if err := engine.PollOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := svc.Now(); got.EligibleDomains != 3 || got.Status != StatusSynced {
		t.Fatalf("caller mutation changed the running policy: %+v", got)
	}
}

func TestNetworkEngineRecoversAfterRawReset(t *testing.T) {
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		buf := make([]byte, 1024)
		if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
			done <- err
			return
		}
		for round := range 2 {
			n, peer, err := conn.ReadFrom(buf)
			if err != nil {
				done <- err
				return
			}
			req, err := ntp.ParsePacket(buf[:n])
			if err != nil {
				done <- err
				return
			}
			stamp := ntp.EncodeTimestamp(uint32(1_704_067_200+int64(round)+ntp.NtpToUnixOffset), 0)
			response := ntp.Packet{Version: 4, Mode: 4, Stratum: 1, OrigTimestamp: req.TxTimestamp, RecvTimestamp: stamp, TxTimestamp: stamp}
			if _, err := conn.WriteTo(response.Encode(), peer); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	t.Cleanup(func() {
		_ = conn.Close()
		if err := <-done; err != nil && !errors.Is(err, net.ErrClosed) {
			t.Error(err)
		}
	})
	raw := clock.NewSimulatedRawClock(10_000_000_000)
	cfg := config.DefaultConfig()
	cfg.Assurance.FaultBudget, cfg.Assurance.MinVotingDomains, cfg.Assurance.MinHonestCoverage = 0, 1, 1
	cfg.Sources = []config.SourceConfig{{FaultDomainID: "one", Endpoint: conn.LocalAddr().String()}}
	svc := remediationService(t, raw, cfg)
	engine, err := NewSyncEngine(cfg, svc)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	if err := engine.PollOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	before := svc.Now()
	raw.SetRaw(1_000_000_000)
	if got := svc.Now(); got.Status != StatusDesync {
		t.Fatalf("raw reset not detected: %+v", got)
	}
	if err := engine.PollOnce(t.Context()); err != nil {
		t.Fatalf("fresh evidence failed to recover: %v", err)
	}
	got := svc.Now()
	truth := measurementEpoch + 1_000_000_000
	if !got.HasInterval || got.Interval.Earliest > truth || got.Interval.Latest < truth {
		t.Fatalf("recovered interval: %+v", got)
	}
	if !got.HasEstimate || math.Abs(float64(got.Estimate-truth)) > 10_000 {
		t.Fatalf("retired raw mapping remained active: %+v", got)
	}
	if before.AssuranceEpochID == got.AssuranceEpochID {
		t.Fatal("raw reset did not retire old assurance epoch")
	}
}
