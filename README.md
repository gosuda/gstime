# GSTime & GSTimeAssure

Zero-dependency, fault-tolerant time synchronization and certification engine in Go. Provides continuous SI-nanosecond tracking, dual-track separation between statistical estimation and interval certification, RFC 8915 Network Time Security (NTS), bounded leap smearing, and lock-free publication.

## Requirements

Go 1.27.0 or newer. Zero external dependencies.

```bash
go get gosuda.org/gstime
```

## Upstream Sources Configuration

GSTime supports NTS-KE authenticated endpoints and standard NTPv4 pools across independent failure domains ($N \ge 2F+1$):

```go
cfg := config.DefaultConfig()
cfg.Sources = []config.SourceConfig{
    {FaultDomainID: "cloudflare", Endpoint: "time.cloudflare.com:4460", NTS: true},
    // Replace these example endpoints with independent, unsmeared UTC sources.
    {FaultDomainID: "provider-a", Endpoint: "ntp-a.example.net:123"},
    {FaultDomainID: "provider-b", Endpoint: "ntp-b.example.net:123"},
}
cfgID, err := cfg.ConfigID()
if err != nil { log.Fatal(err) }
```

## Assurance assumptions and failure policy

- All source intervals are propagated from their `RawMid` to one selection reading with outward-rounded scale bounds before consensus. Custom `SourceQuerier` implementations must populate `MeasurementResult.RawMidReadBound` with the acquisition raw-coordinate error (zero asserts an exact reference) and acquire fresh evidence within the current round and clock generation; older cached samples without continuity metadata are rejected. Every eligible endpoint in a fault domain is intersected; internally inconsistent domains abstain. Only the remaining domains count toward quorum.
- Built-in NTP/NTS acquisition computes hard intervals directly from the qualified raw scale envelope, not estimate-clock RTT. Its synthetic integer `RawMid` is exact (`RawMidReadBound == 0`); endpoint read errors are already included. Custom hard-only measurements may leave `HasEstimate` false and still vote. Statistical pruning never removes their hard evidence.
- Request origins are bound exactly; NTS additionally authenticates the UID and packet. Exchanges spanning an estimate step are rejected. Acquisition can resolve continuity-preserving changes through the bounded mapping history; evicted history is rejected rather than extrapolated.
- NTP/NTS inputs must use **unsmeared UTC**. Smeared sources (including public services that smear by default) are not interchangeable with this profile. Provision and keep the leap history current. Wire timestamps and local estimates are converted explicitly between POSIX and the GST continuous timeline.
- This implementation conservatively rejects leap announcements (`LI != 0`) and intervals touching or crossing the one-second window on either side of a configured leap transition. It does not guess the branch of a repeated/deleted second. Such sources abstain; this intentionally trades availability for a defined conversion policy.
- A completed failed evidence round changes a valid anchor from `SYNCED` to `HOLDOVER`. Its deadline is the earlier of the existing `MaxAgeNs` horizon and `LastSuccessfulRoundRaw + MaxHoldoverAgeNs`; failed polls never renew it. A zero holdover limit preserves the existing anchor horizon. Expiry or a detected continuity fault yields `DESYNC`. A fresh valid round can recover synchronization. Parent-context cancellation and engine shutdown do not count as failed rounds; per-source timeouts under a live parent do.
- Publication requires the full consensus hull's width to be strictly below the configured maximum, consistently with read-time propagation, and validates its ordering before changing the anchor, watermark, estimate, or snapshot. Signed-limit intervals are checked without overflowing their width or midpoint.
- `PastWatermark` advances from the currently propagated lower bound, so `CommitWait` does not require another successful poll. Both require a valid anchor. Invalidations, expired-anchor recovery, and raw/configuration identity changes create a new assurance epoch with a fresh watermark; an old epoch cannot silently resume a wait or contribute retired evidence.
- After a raw-domain reset, acquisition bootstraps request stamps without certifying wall time, then installs a fresh estimate from the successful source round. Retired mappings are not exposed by the read APIs while the raw domain is invalid.
- NTS authenticates the server and packets; it does not establish the server's time accuracy or failure-domain independence. The KE reader requires a complete validated End-of-Message and a bounded message (64 KiB including record headers), without an independent 16 KiB record restriction. The parser, cookie jar, and packet builder share a cookie-size limit derived from the 65,507-byte UDP payload ceiling. Optional cookie placeholders are capped at seven and budgeted toward a 1,232-byte payload; a larger mandatory cookie can still require IP fragmentation and fail on some paths. Responses exceeding the hard payload limit are rejected rather than processed as a truncated prefix. It honors the negotiated NTP host/UDP port and requires TLS 1.3 with `ntske/1` ALPN.
- The NTS wire profile accepts nonce lengths of 16 bytes for AEAD 15 and 12 bytes for AEAD 30. Unsupported lengths and short ciphertexts are rejected before invoking AEAD decryption.
- `SourceQuerier` implementations must honor context cancellation. `Close` cancels and waits for both background and manually invoked polls; query failures preserve their underlying error causes.
- Simulation and loopback interoperability tests are not a proof of containment on arbitrary hardware, hypervisors, or public time services.

## Minimal Examples by Use Case

### Service & Background Sync Initialization

```go
rawClock := clock.NewSystemRawClock()
// leapTableBytes must contain a complete, authoritative GSTL1 leap history.
leapHistory, err := core.ParseLeapHistory(leapTableBytes)
if err != nil { log.Fatal(err) }
svc := gstime.NewClockService(rawClock, leapHistory, cfgID, 32_000_000_000)

// Start background NTP/NTS synchronization engine
engine, err := gstime.NewSyncEngine(cfg, svc)
if err != nil {
	log.Fatal(err)
}

ctx, cancel := context.WithCancel(context.Background())
defer cancel()

_ = engine.Start(ctx)
defer engine.Close() // Best Practice: Gracefully stops background worker with zero goroutine leaks

// Wait until initial synchronization is achieved
_ = engine.WaitSync(ctx)
```

### 1. PublicClock: Monotonic Presentation Time

For logging, APIs, and metrics. The published value is non-decreasing ($P_{k+1} \ge P_k$) while the process and its high-watermark state survive. This includes OS wall-clock steps and raw regressions observed by the process, but **not full VM memory rollback**.

```go
pub := svc.NowPublicAssured()

fmt.Printf("Public Time: %d\n", pub.Center)                 // GstInstant (continuous SI-ns)
fmt.Printf("Symmetric Uncertainty: ±%d ns\n", pub.PublicSymmetricEpsilon)
fmt.Printf("Status: %s\n", pub.Status)                      // SYNCED, HOLDOVER, or DESYNC
```

### 2. GSTimeAssure: Distributed Transactions & CommitWait

For distributed databases requiring external consistency via certified interval and CommitWait.

```go
now := svc.Now()
if now.HasInterval {
	// Certified interval [Earliest, Latest] enclosing true SI time
	fmt.Printf("Certified Range: [%d, %d]\n", now.Interval.Earliest, now.Interval.Latest)
}

// Tri-state causality check: CertainYes, CertainNo, or Unknown
decision, _, _ := svc.After(txTimestamp)

// Commit wait: blocks until certified lower watermark strictly exceeds commitTs
ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
defer cancel()

err := svc.CommitWait(ctx, commitTs, now.AssuranceEpochID, now.LeapHistoryID, now.ConfigID)
if err != nil {
	// Handle ErrDeadlineExceeded, ErrDesynchronized, or ErrConfigurationMismatch
}
```

### 3. Civil UTC with Leap Seconds

For civil calendrical display supporting positive leap seconds (`SecondOfDay = 86400`).

```go
utc, err := svc.NowUtc(leapHistory.ID)
if err != nil { log.Fatal(err) }
if utc.HasEstimate {
	fmt.Printf("UTC: %s\n", utc.Estimate.String())
	fmt.Printf("SecondOfDay: %d\n", utc.Estimate.SecondOfDay)
}
if utc.HasInterval {
	fmt.Printf("UTC range: [%s, %s]\n", utc.Earliest, utc.Latest)
}
```

### 4. POSIX / Unix Projection

For compatibility with legacy Unix millisecond/nanosecond APIs.

```go
proj, err := svc.NowUnixProjection()
fmt.Printf("UnixNanos: %d, InLeapSecond: %v\n", proj.Nanos, proj.IsLeapSecond)
```

### 5. Distributed Lock & Lease Validation (After / Before)

Non-blocking causality check to safely verify whether a distributed lock lease has expired.

```go
decision, status, reason := svc.After(leaseDeadline)
if status != gstime.StatusSynced || decision != gstime.CertainNo {
	// Lease may have expired or clock desynchronized: abort write
}
```

### 6. VM Migration & Discontinuity Fail-Fast

`ClockService` wraps its backend in a `ContinuityGuard`: an observed raw regression (including one still above the anchor), token change, or backend-generation change invalidates the old interval. Expiry also produces `StatusDesync` across `Now`, `NowPublicAssured`, `PastWatermark`, and `CommitWait`.

The portable `SystemRawClock` uses `time.Since` and **does not promise to include suspend time** (`IncludesSuspend() == false`). Its rate/read bounds are deployment assumptions, not hardware attestation. Neither it nor the in-memory guard can detect every pause, unobserved regression, or full VM memory restore. Stop polling and call `svc.Invalidate()` from a trusted resume/migration hook before serving time again; obtain a fresh round before resuming assured operations. Use a platform backend with appropriate suspend/continuity support when such hooks are unavailable. Cross-restore monotonicity needs external durable state, not just a process-local clamp.

```go
now := svc.Now()
if now.Status == gstime.StatusDesync {
	// ReasonBoundTooOld: elapsed-time horizon exceeded.
	// ReasonRawDiscontinuity: observed raw/token/generation discontinuity.
}
```

## Running Examples and Tests

Execute all Go Example tests:

```bash
go test -v -run Example .
```

Run the executable with an explicit Config JSON file and authoritative GSTL1 history. It performs a real round and prints no assured time if acquisition fails:

```bash
go run ./examples -config config.json -leaps history.gstl -timeout 5s
```

### Deterministic Simulation Testing (DST)

GSTime includes a deterministic simulation testing (DST) harness (`dst_test.go`) inspired by FoundationDB and Antithesis. It runs discrete time simulation with pseudorandom fault injection across PRNG seeds:
- **Observed Regressions**: Simulated counter rewinds and continuity token changes (verifying fail-fast `StatusDesync` while detector state survives).
- **Hypervisor Freezes / Suspends**: VM pause/migration for 10s–60s across validity horizons.
- **OS Clock Shaking**: Oscillator frequency wander (up to ±180 ppm) and sampling noise.
- **Byzantine Upstreams**: Outlier sources (+1 hour offsets) filtered by Marzullo/Hull consensus.
- **Simulation Invariants**: Checks public clock monotonicity ($P_{k+1} \ge P_k$), true time containment ($P \pm \epsilon$ and $[L, U]$), and 100% bitwise trace reproducibility across identical seeds.

```bash
go test -v -run TestDST .
```

## Package Layout

```
gosuda.org/gstime
├── core/         # Semantic types, Q16.48 fixed-point math, GSTL1 leap codec
├── ntp/          # NTPv4 wire framing, 2036 era unfolding, reachability bitmap
├── nts/          # NTS-KE client, AEAD 15/30 (RFC 5297 / 8452), cookie lifecycle
├── source/       # Weighted regression, DP runs test, N-F consensus sweep (App. B)
├── clock/        # RawClock drivers, EstimateClock, slew planner, smear engine, clamp
├── assurance/    # Absolute anchor propagation, holdover tracking, status machine
├── publish/      # Lock-free atomic snapshot publication, publication guard
├── config/       # Canonical JSON configuration and RFC 8785 SHA-256 config hashing
├── telemetry/    # Atomic metrics collectors (offsets, errors, smear, re-anchors)
├── conformance/  # Conformance test suites (Levels A-F) and property verifications (P1-P14)
└── service.go    # Unified ClockService facade
```

## API migration and allocation contract

- `NowUtc` now returns `(AssuredUtcNow, error)`. Read value fields `Earliest`, `Latest`, and `Estimate` only when their `HasInterval`/`HasEstimate` flag is true. The result carries the source snapshot's assurance, leap-history, and configuration identities. The former pointer-valued tuple has been removed.
- Steady-state `Now`, `NowPublicAssured`, `NowUtc`, `NowUnixProjection`, `After`, `Before`, and `PastWatermark` allocate no heap memory. `CommitWait` also allocates nothing when its validated condition is already satisfied; a blocking wait creates a timer. Formatting returned values into strings is outside this allocation contract.
- `NewSyncEngine` requires its effective configuration ID and width limit to match the service. It owns a copy of the source configuration. One endpoint cannot claim multiple fault domains. `MaxAgeNs` must be positive; zero `MaxHoldoverAgeNs` preserves the existing anchor horizon.
- `Config.CanonicalBytes` uses RFC 8785 encoding with the four nanosecond fields represented as exact decimal strings. Ordinary Config JSON input stays numeric. Recompute all ConfigIDs when upgrading; do not reuse old-ID anchors or waits. Coordinating nodes must use the same canonical representation.
- `EstimateClock.InitializeAnchors` and `EstimateTrack.AddSample` now return errors. `PublicClock.ReadPublicInstant` returns `(value, clampDebt, error)`. Numeric failures preserve previous state and must not be ignored.

## Verification

```bash
go test -v -race -count=1 ./...
go test -v -race -tags integration -count=1 ./... # local TLS/UDP only
go vet ./...
go vet -tags integration ./...
gojgp check
```
