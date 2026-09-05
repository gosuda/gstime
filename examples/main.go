package main

import (
	"context"
	"fmt"
	"time"

	"gosuda.org/gstime"
	"gosuda.org/gstime/clock"
	"gosuda.org/gstime/config"
	"gosuda.org/gstime/core"
)

// ExampleConfiguration sets up 4 independent sources: Cloudflare NTS, Google NTP, Apple NTP, Meta NTP.
func ExampleConfiguration() (config.Config, [32]byte, error) {
	cfg := config.Config{
		Assurance: config.AssuranceConfig{
			FaultBudget:       1,
			MinVotingDomains:  3,
			MinHonestCoverage: 2,
			MaxWidthNs:        32 * 1_000_000_000,
			MaxAgeNs:          3 * 1024 * 1_000_000_000,
			MaxHoldoverAgeNs:  86400 * 1_000_000_000,
		},
		Raw: config.RawConfig{
			BackendProfile: "standard_monotonic",
			ScaleLowerPpm:  -200.0,
			ScaleUpperPpm:  200.0,
			ReadBoundNs:    1000,
		},
		Sources: []config.SourceConfig{
			{FaultDomainID: "cloudflare", Endpoint: "time.cloudflare.com:4460", NTS: true},
			{FaultDomainID: "google", Endpoint: "time.google.com:123", NTS: false},
			{FaultDomainID: "apple", Endpoint: "time.apple.com:123", NTS: false},
			{FaultDomainID: "meta", Endpoint: "time.facebook.com:123", NTS: false},
		},
	}

	cfgID, err := cfg.ConfigID()
	return cfg, cfgID, err
}

func main() {
	rawClock := clock.NewSystemRawClock()
	lh, err := core.NewLeapHistory(10, []core.LeapEntry{})
	if err != nil {
		panic(err)
	}

	cfg, cfgID, err := ExampleConfiguration()
	if err != nil {
		panic(err)
	}

	svc := gstime.NewClockService(rawClock, lh, cfgID, 32_000_000_000)

	// Best Practice: Launch background SyncEngine with context and defer Close()
	engine, err := gstime.NewSyncEngine(cfg, svc)
	if err != nil {
		panic(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	_ = engine.Start(ctx)
	defer engine.Close() // Guarantees zero goroutine leaks on exit

	nowRaw := rawClock.Read().Raw
	nowUnixNs := gstime.GstInstant(time.Now().UnixNano())
	_ = svc.InitializeEstimate(nowRaw, nowUnixNs, 0)
	hull := gstime.TimeInterval{
		Earliest: nowUnixNs - 50_000,
		Latest:   nowUnixNs + 50_000,
	}
	_ = svc.PublishAssuranceRound(nowRaw, hull, 1, 4, 3, 1, nowRaw+100_000_000_000)

	// Use Case 1: PublicClock Monotonic Presentation Time
	pub := svc.NowPublicAssured()
	fmt.Printf("[UseCase 1: PublicClock]\n")
	fmt.Printf("  Center:  %d\n", pub.Center)
	fmt.Printf("  Epsilon: %d ns\n", pub.PublicSymmetricEpsilon)
	fmt.Printf("  Status:  %s\n\n", pub.Status)

	// Use Case 2: GSTimeAssure Interval & CommitWait
	now := svc.Now()
	fmt.Printf("[UseCase 2: GSTimeAssure Interval & CommitWait]\n")
	if now.Interval != nil {
		fmt.Printf("  Interval: [%d, %d]\n", now.Interval.Earliest, now.Interval.Latest)
		fmt.Printf("  Width:    %d ns\n", now.Interval.Latest-now.Interval.Earliest)
	}
	dec, _, _ := svc.After(nowUnixNs - 1_000_000)
	fmt.Printf("  After(t-1ms): %s\n", dec)

	waitCtx, waitCancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer waitCancel()
	wm, _, _ := svc.PastWatermark(now.AssuranceEpochID, now.LeapHistoryID, now.ConfigID)
	err = svc.CommitWait(waitCtx, wm-1_000, now.AssuranceEpochID, now.LeapHistoryID, now.ConfigID)
	fmt.Printf("  CommitWait(commitTs < watermark): err=%v\n\n", err)

	// Use Case 3: Civil UTC with Leap Seconds (secondOfDay: 0..86400)
	earliestUtc, latestUtc, estUtc, _, _ := svc.NowUtc(lh.ID)
	fmt.Printf("[UseCase 3: Civil UTC]\n")
	if estUtc != nil {
		fmt.Printf("  UTC Time: %s (SecondOfDay=%d, Nanos=%d)\n",
			estUtc.String(), estUtc.SecondOfDay, estUtc.Nanos)
	}
	fmt.Printf("  Earliest: %s\n", earliestUtc.String())
	fmt.Printf("  Latest:   %s\n\n", latestUtc.String())

	// Use Case 4: Monotonic Unix/POSIX Projection
	proj, _ := svc.NowUnixProjection()
	fmt.Printf("[UseCase 4: Unix Projection]\n")
	fmt.Printf("  UnixNanos:    %d\n", proj.Nanos)
	fmt.Printf("  IsLeapSecond: %v\n", proj.IsLeapSecond)
}
