package gstime_test

import (
	"context"
	"fmt"
	"time"

	"gosuda.org/gstime"
	"gosuda.org/gstime/clock"
	"gosuda.org/gstime/config"
	"gosuda.org/gstime/core"
	"gosuda.org/gstime/ntp"
)

func newTestService(rawNow core.RawNanos, target gstime.GstInstant) *gstime.ClockService {
	rawClock := clock.NewSimulatedRawClock(rawNow)
	lh, _ := core.NewLeapHistory(10, nil)
	var cfgID [32]byte
	cfgID[0] = 0x01

	svc := gstime.NewClockService(rawClock, lh, cfgID, 32_000_000_000)
	_ = svc.InitializeEstimate(rawNow, target, 0)
	hull := core.TimeInterval{Earliest: target - 50_000, Latest: target + 50_000}
	_ = svc.PublishAssuranceRound(rawNow, hull, 1, 4, 3, 1, rawNow+100_000_000_000)
	return svc
}

// Example_configuration demonstrates defining multi-domain NTS and NTP sources.
func Example_configuration() {
	cfg := config.DefaultConfig()
	cfg.Sources = []config.SourceConfig{
		{FaultDomainID: "cloudflare", Endpoint: "time.cloudflare.com:4460", NTS: true},
		{FaultDomainID: "google", Endpoint: "time.google.com:123", NTS: false},
		{FaultDomainID: "apple", Endpoint: "time.apple.com:123", NTS: false},
		{FaultDomainID: "meta", Endpoint: "time.facebook.com:123", NTS: false},
	}

	cfgID, _ := cfg.ConfigID()
	fmt.Printf("Sources: %d, HasConfigID: %t\n", len(cfg.Sources), len(cfgID) == 32)
	// Output:
	// Sources: 4, HasConfigID: true
}

// ExampleClockService_NowPublicAssured demonstrates querying monotonic presentation time.
func ExampleClockService_NowPublicAssured() {
	svc := newTestService(1_000_000_000, 1_700_000_000_000_000_000)

	pub := svc.NowPublicAssured()
	fmt.Printf("Status: %s, HasEpsilon: %t\n", pub.Status, pub.PublicSymmetricEpsilon > 0)
	// Output:
	// Status: SYNCED, HasEpsilon: true
}

// ExampleClockService_Now demonstrates obtaining a certified GSTimeAssure true-time interval.
func ExampleClockService_Now() {
	svc := newTestService(1_000_000_000, 1_700_000_000_000_000_000)

	now := svc.Now()
	fmt.Printf("Status: %s, HasInterval: %t\n", now.Status, now.Interval != nil)
	// Output:
	// Status: SYNCED, HasInterval: true
}

// ExampleClockService_CommitWait demonstrates external consistency commit wait.
func ExampleClockService_CommitWait() {
	svc := newTestService(1_000_000_000, 1_700_000_000_000_000_000)
	now := svc.Now()

	// Commit timestamp selected within the certified interval
	commitTs := now.Interval.Earliest - 1000

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	err := svc.CommitWait(ctx, commitTs, now.AssuranceEpochID, now.LeapHistoryID, now.ConfigID)
	fmt.Printf("CommitWait: %v\n", err)
	// Output:
	// CommitWait: <nil>
}

// ExampleClockService_After demonstrates non-blocking tri-state causality and lock lease validation.
func ExampleClockService_After() {
	svc := newTestService(1_000_000_000, 1_700_000_000_000_000_000)

	// Past timestamp check
	pastTime := gstime.GstInstant(1_700_000_000_000_000_000 - 1_000_000)
	decision, status, _ := svc.After(pastTime)

	fmt.Printf("After(past): %s (status: %s)\n", decision, status)
	// Output:
	// After(past): CertainYes (status: SYNCED)
}

// ExampleClockService_NowUtc demonstrates civil calendrical time formatting.
func ExampleClockService_NowUtc() {
	svc := newTestService(1_000_000_000, 1_700_000_000_000_000_000)
	now := svc.Now()

	_, _, est, status, _ := svc.NowUtc(now.LeapHistoryID)
	fmt.Printf("Status: %s, ValidDate: %t\n", status, est.Date.Year > 2020)
	// Output:
	// Status: SYNCED, ValidDate: true
}

// ExampleClockService_NowUnixProjection demonstrates legacy POSIX timestamp projection.
func ExampleClockService_NowUnixProjection() {
	svc := newTestService(1_000_000_000, 1_700_000_000_000_000_000)

	proj, err := svc.NowUnixProjection()
	fmt.Printf("Err: %v, IsLeapSecond: %t\n", err, proj.IsLeapSecond)
	// Output:
	// Err: <nil>, IsLeapSecond: false
}

// Example_vmMigrationLockDetection demonstrates fail-fast detection of VM pause / anchor expiration.
func Example_vmMigrationLockDetection() {
	raw := clock.NewSimulatedRawClock(1_000_000_000)
	lh, _ := core.NewLeapHistory(10, nil)
	var cfgID [32]byte
	svc := gstime.NewClockService(raw, lh, cfgID, 32_000_000_000)

	nowRaw := raw.Read().Raw
	target := gstime.GstInstant(1_700_000_000_000_000_000)
	_ = svc.InitializeEstimate(nowRaw, target, 0)
	hull := core.TimeInterval{Earliest: target - 100, Latest: target + 100}

	// Anchor valid for only 5 seconds of raw time
	_ = svc.PublishAssuranceRound(nowRaw, hull, 1, 3, 2, 1, nowRaw+5_000_000_000)

	// Simulate VM suspend/migration: raw time jumps forward by 2 hours
	raw.Advance(7200 * 1_000_000_000)

	// Immediate fail-fast detection: anchor expired
	now := svc.Now()
	fmt.Printf("AfterVMSuspend: %s, Reason: %s\n", now.Status, now.Reason)
	// Output:
	// AfterVMSuspend: DESYNC, Reason: BOUND_TOO_OLD
}

// Example_syncEngineBackgroundPolling demonstrates background NTP polling with leak-free lifecycle management.
func Example_syncEngineBackgroundPolling() {
	raw := clock.NewSimulatedRawClock(10_000_000_000)
	lh, _ := core.NewLeapHistory(10, nil)
	var cfgID [32]byte

	cfg := config.DefaultConfig()
	cfg.Sources = []config.SourceConfig{
		{FaultDomainID: "cloudflare", Endpoint: "time.cloudflare.com:123", NTS: false},
		{FaultDomainID: "google", Endpoint: "time.google.com:123", NTS: false},
		{FaultDomainID: "apple", Endpoint: "time.apple.com:123", NTS: false},
	}

	svc := gstime.NewClockService(raw, lh, cfgID, 32_000_000_000)

	targetTime := gstime.GstInstant(1_700_000_000 * 1_000_000_000)
	mock := &mockQuerier{
		results: map[string]*ntp.MeasurementResult{
			"time.cloudflare.com:123": newMockMeasurement(targetTime, 10_000_000, 10_000_000_000),
			"time.google.com:123":     newMockMeasurement(targetTime, 12_000_000, 10_000_000_000),
			"time.apple.com:123":      newMockMeasurement(targetTime, 15_000_000, 10_000_000_000),
		},
	}

	engine, err := gstime.NewSyncEngine(cfg, svc, gstime.WithSourceQuerier(mock))
	if err != nil {
		panic(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start background polling daemon
	_ = engine.Start(ctx)

	// Best Practice: Always defer engine.Close() to stop polling goroutine and prevent goroutine leaks
	defer engine.Close()

	// WaitSync blocks until initial synchronization is achieved
	if err := engine.WaitSync(ctx); err != nil {
		panic(err)
	}

	now := svc.Now()
	fmt.Printf("EngineRunning: true, Status: %s\n", now.Status)
	// Output:
	// EngineRunning: true, Status: SYNCED
}
