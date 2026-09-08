package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"time"

	"gosuda.org/gstime"
	"gosuda.org/gstime/clock"
	"gosuda.org/gstime/config"
	"gosuda.org/gstime/core"
)

var errUsage = errors.New("provide -config <Config JSON> and -leaps <GSTL1 history> with a positive -timeout and no positional arguments")

func run(ctx context.Context, args []string) (utc gstime.AssuredUtcNow, err error) {
	flags := flag.NewFlagSet("gstime-example", flag.ContinueOnError)
	configPath := flags.String("config", "", "Config JSON file containing unsmeared UTC/NTP sources and fault-domain policy")
	leapsPath := flags.String("leaps", "", "canonical GSTL1 leap-history file")
	timeout := flags.Duration("timeout", 5*time.Second, "maximum duration of the synchronization round")
	if err := flags.Parse(args); err != nil {
		return utc, err
	}
	if *configPath == "" || *leapsPath == "" || *timeout <= 0 || flags.NArg() != 0 {
		return utc, errUsage
	}

	configBytes, err := os.ReadFile(*configPath)
	if err != nil {
		return utc, fmt.Errorf("read configuration: %w", err)
	}
	var cfg config.Config
	if err := json.Unmarshal(configBytes, &cfg); err != nil {
		return utc, fmt.Errorf("decode configuration: %w", err)
	}
	cfgID, err := cfg.ConfigID()
	if err != nil {
		return utc, err
	}
	if cfg.Raw != config.DefaultConfig().Raw {
		return utc, fmt.Errorf("%w: this example requires the standard_monotonic backend with [-200, 200] ppm scale bounds and a 1000 ns read bound", config.ErrInvalidConfig)
	}

	leapBytes, err := os.ReadFile(*leapsPath)
	if err != nil {
		return utc, fmt.Errorf("read leap history: %w", err)
	}
	lh, err := core.ParseLeapHistory(leapBytes)
	if err != nil {
		return utc, fmt.Errorf("parse leap history: %w", err)
	}

	svc := gstime.NewClockService(clock.NewSystemRawClock(), lh, cfgID, cfg.Assurance.MaxWidthNs)
	engine, err := gstime.NewSyncEngine(cfg, svc, gstime.WithQueryTimeout(*timeout))
	if err != nil {
		return utc, err
	}
	defer func() {
		err = errors.Join(err, engine.Close())
	}()

	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	if err := engine.PollOnce(ctx); err != nil {
		return utc, fmt.Errorf("synchronize: %w", err)
	}
	utc, err = svc.NowUtc(lh.ID)
	if err != nil {
		return utc, err
	}
	if utc.Status != gstime.StatusSynced || !utc.HasInterval || !utc.HasEstimate {
		return utc, fmt.Errorf("%w: status=%s reason=%s", gstime.ErrDesynchronized, utc.Status, utc.Reason)
	}
	return utc, nil
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	utc, err := run(ctx, os.Args[1:])
	if errors.Is(err, flag.ErrHelp) {
		return
	}
	if err == nil {
		_, err = fmt.Printf("Status: %s\nUTC interval: [%s, %s]\nUTC estimate: %s\n",
			utc.Status, utc.Earliest, utc.Latest, utc.Estimate)
	}
	if err != nil {
		log.Print(err)
		os.Exit(1)
	}
}
