package gstime_test

import (
	"context"
	"fmt"
	"net"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"gosuda.org/gstime"
	"gosuda.org/gstime/clock"
	"gosuda.org/gstime/config"
	"gosuda.org/gstime/core"
	"gosuda.org/gstime/ntp"
)

type mockQuerier struct {
	queryCount atomic.Int64
	queried    chan struct{}
	results    map[string]*ntp.MeasurementResult
	errs       map[string]error
}

func (m *mockQuerier) QuerySource(ctx context.Context, src config.SourceConfig, rawNow core.RawNanos) (*ntp.MeasurementResult, error) {
	m.queryCount.Add(1)
	if m.queried != nil {
		select {
		case m.queried <- struct{}{}:
		default:
		}
	}
	if err, ok := m.errs[src.Endpoint]; ok && err != nil {
		return nil, err
	}
	if res, ok := m.results[src.Endpoint]; ok {
		return res, nil
	}
	return nil, fmt.Errorf("no mock result for endpoint: %s", src.Endpoint)
}

func newMockMeasurement(center gstime.GstInstant, boundNs int64, rawMid core.RawNanos) *ntp.MeasurementResult {
	return &ntp.MeasurementResult{
		Center:       center,
		NetworkBound: boundNs,
		RawMid:       rawMid,
		HardInterval: core.TimeInterval{
			Earliest: center - gstime.GstInstant(boundNs),
			Latest:   center + gstime.GstInstant(boundNs),
		},
	}
}

func TestSyncEngine_LifecycleAndZeroGoroutineLeak(t *testing.T) {
	// Settle existing runtime goroutines before measuring baseline
	runtime.GC()
	runtime.Gosched()
	baselineGoroutines := runtime.NumGoroutine()

	raw := clock.NewSimulatedRawClock(10_000_000_000)
	lh, _ := core.NewLeapHistory(10, nil)
	var cfgID [32]byte

	cfg := config.DefaultConfig()
	cfg.Sources = []config.SourceConfig{
		{FaultDomainID: "d1", Endpoint: "10.0.0.1:123", NTS: false},
		{FaultDomainID: "d2", Endpoint: "10.0.0.2:123", NTS: false},
		{FaultDomainID: "d3", Endpoint: "10.0.0.3:123", NTS: false},
	}

	svc := gstime.NewClockService(raw, lh, cfgID, 32_000_000_000)

	targetTime := gstime.GstInstant(1_700_000_000 * 1_000_000_000)
	mock := &mockQuerier{
		queried: make(chan struct{}, 100),
		results: map[string]*ntp.MeasurementResult{
			"10.0.0.1:123": newMockMeasurement(targetTime, 20_000_000, 10_000_000_000),
			"10.0.0.2:123": newMockMeasurement(targetTime, 25_000_000, 10_000_000_000),
			"10.0.0.3:123": newMockMeasurement(targetTime, 22_000_000, 10_000_000_000),
		},
	}

	engine, err := gstime.NewSyncEngine(
		cfg,
		svc,
		gstime.WithPollInterval(20*time.Millisecond),
		gstime.WithQueryTimeout(15*time.Millisecond),
		gstime.WithSourceQuerier(mock),
	)
	if err != nil {
		t.Fatalf("NewSyncEngine failed: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := engine.Start(ctx); err != nil {
		t.Fatalf("engine.Start failed: %v", err)
	}

	// Double start must fail
	if err := engine.Start(ctx); err == nil {
		t.Fatalf("expected error on double Start(), got nil")
	}

	// Wait for multiple background queries using channel notifications
	for i := 0; i < 3; i++ {
		select {
		case <-mock.queried:
		case <-time.After(2 * time.Second):
			t.Fatalf("timeout waiting for background queries, got %d", mock.queryCount.Load())
		}
	}

	// Wait for ClockService to transition to SYNCED
	deadline := time.Now().Add(2 * time.Second)
	for svc.Now().Status != gstime.StatusSynced && time.Now().Before(deadline) {
		runtime.Gosched()
	}

	now := svc.Now()
	if now.Status != gstime.StatusSynced {
		t.Fatalf("expected ClockService to be SYNCED, got %s", now.Status)
	}
	if now.Interval == nil {
		t.Fatalf("expected non-nil interval after sync")
	}

	// Graceful shutdown
	if err := engine.Close(); err != nil {
		t.Fatalf("engine.Close failed: %v", err)
	}

	// Idempotent Close()
	if err := engine.Close(); err != nil {
		t.Fatalf("second engine.Close() failed: %v", err)
	}

	// Start after Close must fail
	if err := engine.Start(ctx); err == nil {
		t.Fatalf("expected error starting closed engine, got nil")
	}

	// Check for goroutine leak
	runtime.GC()
	runtime.Gosched()

	finalGoroutines := runtime.NumGoroutine()
	if finalGoroutines > baselineGoroutines {
		t.Fatalf("goroutine leak detected: baseline=%d, final=%d", baselineGoroutines, finalGoroutines)
	}
}

func TestSyncEngine_PollOnceConsensus(t *testing.T) {
	raw := clock.NewSimulatedRawClock(10_000_000_000)
	lh, _ := core.NewLeapHistory(10, nil)
	var cfgID [32]byte

	cfg := config.DefaultConfig()
	cfg.Assurance.FaultBudget = 1
	cfg.Assurance.MinVotingDomains = 3
	cfg.Assurance.MinHonestCoverage = 2
	cfg.Sources = []config.SourceConfig{
		{FaultDomainID: "d1", Endpoint: "10.0.0.1:123", NTS: false},
		{FaultDomainID: "d2", Endpoint: "10.0.0.2:123", NTS: false},
		{FaultDomainID: "d3", Endpoint: "10.0.0.3:123", NTS: false},
		{FaultDomainID: "d4", Endpoint: "10.0.0.4:123", NTS: false}, // Byzantine faulty
	}

	svc := gstime.NewClockService(raw, lh, cfgID, 32_000_000_000)

	targetTime := gstime.GstInstant(1_700_000_000 * 1_000_000_000)
	byzantineTime := targetTime + gstime.GstInstant(3600*1_000_000_000) // 1 hour ahead

	mock := &mockQuerier{
		results: map[string]*ntp.MeasurementResult{
			"10.0.0.1:123": newMockMeasurement(targetTime-1000, 10_000_000, 10_000_000_000),
			"10.0.0.2:123": newMockMeasurement(targetTime+1000, 15_000_000, 10_000_000_000),
			"10.0.0.3:123": newMockMeasurement(targetTime, 12_000_000, 10_000_000_000),
			"10.0.0.4:123": newMockMeasurement(byzantineTime, 1000, 10_000_000_000),
		},
	}

	engine, err := gstime.NewSyncEngine(cfg, svc, gstime.WithSourceQuerier(mock))
	if err != nil {
		t.Fatalf("NewSyncEngine failed: %v", err)
	}
	defer engine.Close()

	ctx := context.Background()
	if err := engine.PollOnce(ctx); err != nil {
		t.Fatalf("PollOnce failed: %v", err)
	}

	now := svc.Now()
	if now.Status != gstime.StatusSynced {
		t.Fatalf("expected StatusSynced, got %s", now.Status)
	}

	// Consensus must enclose targetTime despite Byzantine outlier
	if now.Interval.Earliest > targetTime || now.Interval.Latest < targetTime {
		t.Fatalf("consensus interval [%d, %d] does not contain ground truth %d",
			now.Interval.Earliest, now.Interval.Latest, targetTime)
	}
}

func TestSyncEngine_InsufficientDomains(t *testing.T) {
	raw := clock.NewSimulatedRawClock(10_000_000_000)
	lh, _ := core.NewLeapHistory(10, nil)
	var cfgID [32]byte

	cfg := config.DefaultConfig()
	cfg.Assurance.MinVotingDomains = 3
	cfg.Sources = []config.SourceConfig{
		{FaultDomainID: "d1", Endpoint: "10.0.0.1:123", NTS: false},
		{FaultDomainID: "d2", Endpoint: "10.0.0.2:123", NTS: false},
	}

	svc := gstime.NewClockService(raw, lh, cfgID, 32_000_000_000)
	mock := &mockQuerier{
		results: map[string]*ntp.MeasurementResult{
			"10.0.0.1:123": newMockMeasurement(1_000_000, 10_000, 10_000_000_000),
		},
	}

	engine, err := gstime.NewSyncEngine(cfg, svc, gstime.WithSourceQuerier(mock))
	if err != nil {
		t.Fatalf("NewSyncEngine failed: %v", err)
	}
	defer engine.Close()

	err = engine.PollOnce(context.Background())
	if err == nil {
		t.Fatalf("expected error on insufficient domains, got nil")
	}
}

func TestSyncEngine_RealLoopbackUDP(t *testing.T) {
	// Start 3 real loopback UDP servers
	var servers []*net.UDPConn
	var endpoints []string

	for i := 0; i < 3; i++ {
		addr, err := net.ResolveUDPAddr("udp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("ResolveUDPAddr failed: %v", err)
		}
		conn, err := net.ListenUDP("udp", addr)
		if err != nil {
			t.Fatalf("ListenUDP failed: %v", err)
		}
		servers = append(servers, conn)
		endpoints = append(endpoints, conn.LocalAddr().String())

		// Server loop responding to NTP requests
		go func(c *net.UDPConn) {
			buf := make([]byte, 1024)
			for {
				n, clientAddr, err := c.ReadFrom(buf)
				if err != nil {
					return // Server closed
				}
				if n >= 48 {
					// Build dummy Stratum 1 response
					resp := ntp.Packet{
						Version: 4,
						Mode:    4, // Server
						Stratum: 1,
						Poll:    6,
					}
					// Put dummy valid timestamps
					sec := uint32(time.Now().Unix() + 2208988800) // NTP epoch
					resp.RecvTimestamp = ntp.EncodeTimestamp(sec, 100)
					resp.TxTimestamp = ntp.EncodeTimestamp(sec, 200)
					_, _ = c.WriteTo(resp.Encode(), clientAddr)
				}
			}
		}(conn)
	}

	defer func() {
		for _, s := range servers {
			_ = s.Close()
		}
	}()

	raw := clock.NewSystemRawClock()
	lh, _ := core.NewLeapHistory(10, nil)
	var cfgID [32]byte

	cfg := config.DefaultConfig()
	cfg.Assurance.MinVotingDomains = 3
	cfg.Assurance.MinHonestCoverage = 2
	cfg.Sources = []config.SourceConfig{
		{FaultDomainID: "loopback-1", Endpoint: endpoints[0], NTS: false},
		{FaultDomainID: "loopback-2", Endpoint: endpoints[1], NTS: false},
		{FaultDomainID: "loopback-3", Endpoint: endpoints[2], NTS: false},
	}

	svc := gstime.NewClockService(raw, lh, cfgID, 32_000_000_000)

	// Use default network querier (testing real UDP network socket I/O)
	engine, err := gstime.NewSyncEngine(
		cfg,
		svc,
		gstime.WithPollInterval(20*time.Millisecond),
		gstime.WithQueryTimeout(100*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("NewSyncEngine failed: %v", err)
	}
	defer engine.Close()

	ctx := context.Background()
	err = engine.PollOnce(ctx)
	if err != nil {
		t.Fatalf("Real UDP PollOnce failed: %v", err)
	}

	now := svc.Now()
	if now.Status != gstime.StatusSynced {
		t.Fatalf("expected StatusSynced after loopback UDP poll, got %s", now.Status)
	}
}
