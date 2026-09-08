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

func newSimulatedExampleService(rawNow core.RawNanos, target gstime.GstInstant) *gstime.ClockService {
	rawClock := clock.NewSimulatedRawClock(rawNow)
	lh, err := core.NewLeapHistory(10, nil)
	if err != nil {
		panic(err)
	}
	cfg := simulatedExampleConfig()
	cfgID, err := cfg.ConfigID()
	if err != nil {
		panic(err)
	}

	svc := gstime.NewClockService(rawClock, lh, cfgID, cfg.Assurance.MaxWidthNs)
	if err := svc.InitializeEstimate(rawNow, target, 0); err != nil {
		panic(err)
	}
	hull := core.TimeInterval{Earliest: target - 50_000, Latest: target + 50_000}
	if err := svc.PublishAssuranceRound(rawNow, hull, 1, 3, 2, 1, rawNow+100_000_000_000); err != nil {
		panic(err)
	}
	return svc
}

func simulatedExampleConfig() config.Config {
	cfg := config.DefaultConfig()
	cfg.Sources = []config.SourceConfig{
		{FaultDomainID: "a", Endpoint: "a.example:123"},
		{FaultDomainID: "b", Endpoint: "b.example:123"},
		{FaultDomainID: "c", Endpoint: "c.example:123"},
	}
	return cfg
}

// Example_configuration uses placeholder endpoints; operators must select unsmeared sources and independent fault domains.
func Example_configuration() {
	cfg := config.DefaultConfig()
	cfg.Sources = []config.SourceConfig{
		{FaultDomainID: "a", Endpoint: "a.example:4460", NTS: true},
		{FaultDomainID: "b", Endpoint: "b.example:123"},
		{FaultDomainID: "c", Endpoint: "c.example:123"},
		{FaultDomainID: "d", Endpoint: "d.example:123"},
	}

	cfgID, err := cfg.ConfigID()
	if err != nil {
		panic(err)
	}
	fmt.Printf("Sources: %d, HasConfigID: %t\n", len(cfg.Sources), len(cfgID) == 32)
	// Output:
	// Sources: 4, HasConfigID: true
}

// ExampleClockService_NowPublicAssured demonstrates querying monotonic presentation time.
func ExampleClockService_NowPublicAssured() {
	svc := newSimulatedExampleService(1_000_000_000, 1_700_000_000_000_000_000)

	pub := svc.NowPublicAssured()
	fmt.Printf("Status: %s, HasEpsilon: %t\n", pub.Status, pub.PublicSymmetricEpsilon > 0)
	// Output:
	// Status: SYNCED, HasEpsilon: true
}

// ExampleClockService_Now reads an assured interval from simulated evidence.
func ExampleClockService_Now() {
	svc := newSimulatedExampleService(1_000_000_000, 1_700_000_000_000_000_000)

	now := svc.Now()
	fmt.Printf("Status: %s, HasInterval: %t\n", now.Status, now.HasInterval)
	// Output:
	// Status: SYNCED, HasInterval: true
}

// ExampleClockService_CommitWait demonstrates external consistency commit wait.
func ExampleClockService_CommitWait() {
	svc := newSimulatedExampleService(1_000_000_000, 1_700_000_000_000_000_000)
	now := svc.Now()

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
	svc := newSimulatedExampleService(1_000_000_000, 1_700_000_000_000_000_000)

	pastTime := gstime.GstInstant(1_700_000_000_000_000_000 - 1_000_000)
	decision, status, _ := svc.After(pastTime)

	fmt.Printf("After(past): %s (status: %s)\n", decision, status)
	// Output:
	// After(past): CertainYes (status: SYNCED)
}

// ExampleClockService_NowUtc demonstrates civil calendrical time formatting.
func ExampleClockService_NowUtc() {
	svc := newSimulatedExampleService(1_000_000_000, 1_700_000_000_000_000_000)
	now := svc.Now()

	utc, err := svc.NowUtc(now.LeapHistoryID)
	if err != nil {
		panic(err)
	}
	fmt.Printf("Status: %s, ValidDate: %t\n", utc.Status, utc.HasEstimate && utc.Estimate.Date.Year > 2020)
	// Output:
	// Status: SYNCED, ValidDate: true
}

// ExampleClockService_NowUnixProjection demonstrates legacy POSIX timestamp projection.
func ExampleClockService_NowUnixProjection() {
	svc := newSimulatedExampleService(1_000_000_000, 1_700_000_000_000_000_000)

	proj, err := svc.NowUnixProjection()
	fmt.Printf("Err: %v, IsLeapSecond: %t\n", err, proj.IsLeapSecond)
	// Output:
	// Err: <nil>, IsLeapSecond: false
}

// Example_vmMigrationLockDetection demonstrates fail-fast detection of VM pause / anchor expiration.
func Example_vmMigrationLockDetection() {
	raw := clock.NewSimulatedRawClock(1_000_000_000)
	lh, err := core.NewLeapHistory(10, nil)
	if err != nil {
		panic(err)
	}
	cfg := simulatedExampleConfig()
	cfgID, err := cfg.ConfigID()
	if err != nil {
		panic(err)
	}
	svc := gstime.NewClockService(raw, lh, cfgID, cfg.Assurance.MaxWidthNs)

	nowRaw := raw.Read().Raw
	target := gstime.GstInstant(1_700_000_000_000_000_000)
	if err := svc.InitializeEstimate(nowRaw, target, 0); err != nil {
		panic(err)
	}
	hull := core.TimeInterval{Earliest: target - 100, Latest: target + 100}

	// Anchor valid for only 5 seconds of raw time
	if err := svc.PublishAssuranceRound(nowRaw, hull, 1, 3, 2, 1, nowRaw+5_000_000_000); err != nil {
		panic(err)
	}

	// Simulate VM suspend/migration: raw time jumps forward by 2 hours
	raw.Advance(7200 * 1_000_000_000)

	now := svc.Now()
	fmt.Printf("AfterVMSuspend: %s, Reason: %s\n", now.Status, now.Reason)
	// Output:
	// AfterVMSuspend: DESYNC, Reason: BOUND_TOO_OLD
}

// Example_syncEngineBackgroundPolling uses simulated measurements, not production acquisition.
func Example_syncEngineBackgroundPolling() {
	raw := clock.NewSimulatedRawClock(10_000_000_000)
	lh, err := core.NewLeapHistory(10, nil)
	if err != nil {
		panic(err)
	}
	cfg := simulatedExampleConfig()
	cfgID, err := cfg.ConfigID()
	if err != nil {
		panic(err)
	}
	svc := gstime.NewClockService(raw, lh, cfgID, cfg.Assurance.MaxWidthNs)

	targetTime := gstime.GstInstant(1_700_000_000 * 1_000_000_000)
	mock := &mockQuerier{
		results: map[string]*ntp.MeasurementResult{
			"a.example:123": newMockMeasurement(targetTime, 10_000_000, 10_000_000_000),
			"b.example:123": newMockMeasurement(targetTime, 12_000_000, 10_000_000_000),
			"c.example:123": newMockMeasurement(targetTime, 15_000_000, 10_000_000_000),
		},
	}

	engine, err := gstime.NewSyncEngine(cfg, svc, gstime.WithSourceQuerier(mock))
	if err != nil {
		panic(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	defer func() {
		if err := engine.Close(); err != nil {
			panic(err)
		}
	}()
	if err := engine.Start(ctx); err != nil {
		panic(err)
	}

	if err := engine.WaitSync(ctx); err != nil {
		panic(err)
	}

	now := svc.Now()
	fmt.Printf("EngineRunning: true, Status: %s\n", now.Status)
	// Output:
	// EngineRunning: true, Status: SYNCED
}
