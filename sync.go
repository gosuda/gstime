package gstime

import (
	"cmp"
	"context"
	"crypto/cipher"
	"crypto/tls"
	"errors"
	"fmt"
	"math"
	"net"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"gosuda.org/gstime/assurance"
	"gosuda.org/gstime/clock"
	"gosuda.org/gstime/config"
	"gosuda.org/gstime/core"
	"gosuda.org/gstime/ntp"
	"gosuda.org/gstime/nts"
	"gosuda.org/gstime/source"
)

// SourceQuerier abstracts network interactions for NTP/NTS queries.
// Implementations must acquire fresh evidence during this round, on the supplied
// service raw clock and its current continuity/scale envelope. Cached results
// from an earlier round or clock generation must not be returned. HardInterval
// describes GST time at RawMid, not at query completion.
type SourceQuerier interface {
	QuerySource(ctx context.Context, src config.SourceConfig, rawNow core.RawNanos) (*ntp.MeasurementResult, error)
}

// SyncOption configures a SyncEngine instance.
type SyncOption func(*SyncEngine)

// WithPollInterval sets the background synchronization polling interval.
func WithPollInterval(d time.Duration) SyncOption {
	return func(e *SyncEngine) {
		if d > 0 {
			e.pollIntvl = d
		}
	}
}

// WithQueryTimeout sets the network query timeout per source.
func WithQueryTimeout(d time.Duration) SyncOption {
	return func(e *SyncEngine) {
		if d > 0 {
			e.queryTimeout = d
		}
	}
}

// WithSourceQuerier overrides the default network querier (useful for mock/test transports).
func WithSourceQuerier(q SourceQuerier) SyncOption {
	return func(e *SyncEngine) {
		if q != nil {
			e.querier = q
		}
	}
}

// SyncEngine coordinates background polling of configured upstream sources and publishes assurance rounds.
type SyncEngine struct {
	cfg          config.Config
	svc          *ClockService
	querier      SourceQuerier
	pollIntvl    time.Duration
	queryTimeout time.Duration

	pollMu  sync.Mutex
	mu      sync.Mutex
	wg      sync.WaitGroup
	cancel  context.CancelFunc
	running atomic.Bool
	closed  atomic.Bool
}

// NewSyncEngine initializes a new SyncEngine for the given configuration and ClockService.
func NewSyncEngine(cfg config.Config, svc *ClockService, opts ...SyncOption) (*SyncEngine, error) {
	if svc == nil {
		return nil, errors.New("clock service cannot be nil")
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}
	if len(cfg.Sources) == 0 {
		return nil, errors.New("configuration has no upstream sources")
	}

	engine := &SyncEngine{
		cfg:          cfg,
		svc:          svc,
		pollIntvl:    2 * time.Second,
		queryTimeout: 1500 * time.Millisecond,
	}

	for _, opt := range opts {
		opt(engine)
	}

	if engine.querier == nil {
		engine.querier = newDefaultSourceQuerier(svc.rawClock, svc.estimateClock, svc.leapHistory, engine.queryTimeout)
	}

	return engine, nil
}

// Start launches background polling under the provided context.
// It is non-blocking and spawns a single worker goroutine managed by the engine.
func (e *SyncEngine) Start(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.closed.Load() {
		return errors.New("sync engine is closed")
	}
	if e.running.Swap(true) {
		return errors.New("sync engine is already running")
	}

	subCtx, cancel := context.WithCancel(ctx)
	e.cancel = cancel

	e.wg.Add(1)
	go e.loop(subCtx)

	return nil
}

// Close gracefully stops the background polling worker, waits for all queries to finish, and closes open resources.
// It guarantees zero goroutine leaks upon return.
func (e *SyncEngine) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.closed.Swap(true) {
		return nil
	}

	if e.cancel != nil {
		e.cancel()
	}

	e.wg.Wait()
	e.running.Store(false)
	return nil
}

// WaitSync blocks until the underlying ClockService achieves StatusSynced, or ctx is done.
func (e *SyncEngine) WaitSync(ctx context.Context) error {
	return e.svc.WaitSync(ctx)
}

func (e *SyncEngine) loop(ctx context.Context) {
	defer e.wg.Done()
	defer e.running.Store(false)

	// Execute initial poll immediately on startup
	_ = e.PollOnce(ctx)

	ticker := time.NewTicker(e.pollIntvl)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = e.PollOnce(ctx)
		}
	}
}

// PollOnce executes a single parallel query round across configured sources and publishes consensus.
func (e *SyncEngine) PollOnce(ctx context.Context) (pollErr error) {
	e.pollMu.Lock()
	defer e.pollMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	defer func() {
		if pollErr != nil {
			pollErr = errors.Join(pollErr, e.svc.markRoundFailure(e.cfg.Assurance.MaxHoldoverAgeNs))
		}
	}()
	startReading := e.svc.rawClock.Read()
	scaleLow, scaleUpp := e.svc.rawClock.ScaleEnvelope()
	if e.closed.Load() {
		return errors.New("sync engine is closed")
	}

	type sourceResult struct {
		domain string
		res    *ntp.MeasurementResult
		err    error
	}

	results := make(chan sourceResult, len(e.cfg.Sources))
	var wg sync.WaitGroup

	for _, src := range e.cfg.Sources {
		wg.Add(1)
		go func(s config.SourceConfig) {
			defer wg.Done()
			pollCtx, cancel := context.WithTimeout(ctx, e.queryTimeout)
			defer cancel()

			tNow := e.svc.rawClock.Read().Raw
			res, err := e.querier.QuerySource(pollCtx, s, tNow)
			results <- sourceResult{domain: s.FaultDomainID, res: res, err: err}
		}(src)
	}

	wg.Wait()
	close(results)

	// Every interval must refer to this same reading, not to its individual RawMid.
	reading := e.svc.rawClock.Read()
	nowRaw := reading.Raw
	currentLow, currentUpp := e.svc.rawClock.ScaleEnvelope()
	if err := ctx.Err(); err != nil {
		return err
	}
	if reading.ContinuityToken != startReading.ContinuityToken || reading.BackendGeneration != startReading.BackendGeneration || nowRaw < startReading.Raw || currentLow != scaleLow || currentUpp != scaleUpp {
		return errors.New("raw clock changed during query round")
	}
	maxAge := e.cfg.Assurance.MaxAgeNs
	if maxAge <= 0 {
		maxAge = 10 * 1_000_000_000
	}
	if uint64(nowRaw) > math.MaxUint64-uint64(maxAge) {
		return core.ErrOverflow
	}
	byDomain := make(map[string][]core.TimeInterval)
	for r := range results {
		if r.err != nil || r.res == nil || r.domain == "" {
			continue
		}
		if r.res.RawMid < startReading.Raw || r.res.RawMid > nowRaw || nowRaw-r.res.RawMid > core.RawNanos(maxAge) {
			continue
		}
		sample := &assurance.AssuranceAnchor{
			RawAnchor: r.res.RawMid, LowerAtAnchor: r.res.HardInterval.Earliest, UpperAtAnchor: r.res.HardInterval.Latest,
			RawScaleLower: scaleLow, RawScaleUpper: scaleUpp, RawReadBound: r.res.RawReadBound,
			ContinuityToken: reading.ContinuityToken, ValidUntilRaw: nowRaw,
		}
		inv, err := assurance.PropagateAnchor(sample, nowRaw, reading.ReadBound, reading.ContinuityToken, 0, 0, e.cfg.Assurance.MaxWidthNs)
		if err != nil {
			continue
		}
		byDomain[r.domain] = append(byDomain[r.domain], *inv)
	}
	var domains []string
	for domain := range byDomain {
		domains = append(domains, domain)
	}
	sort.Strings(domains)
	var intervals []core.TimeInterval
	for _, domain := range domains {
		consolidated := source.ConsolidateDomainIntervals(core.FaultDomainID(domain), byDomain[domain])
		if !consolidated.Inconsistent {
			intervals = append(intervals, consolidated.Interval)
		}
	}

	minDomains := e.cfg.Assurance.MinVotingDomains
	if minDomains <= 0 {
		minDomains = 1
	}

	if len(intervals) < minDomains {
		return fmt.Errorf("insufficient voting domains: got %d, required %d", len(intervals), minDomains)
	}

	faultBudget := e.cfg.Assurance.FaultBudget
	minHonest := e.cfg.Assurance.MinHonestCoverage
	if minHonest <= 0 {
		minHonest = 1
	}

	consensus, err := source.ComputeAssuranceConsensus(
		intervals,
		faultBudget,
		minDomains,
		minHonest,
		0,
		0,
	)
	if err != nil {
		return fmt.Errorf("consensus computation failed: %w", err)
	}

	validUntilRaw := nowRaw + core.RawNanos(maxAge)
	err = e.svc.publishSyncRound(reading, scaleLow, scaleUpp, consensus, validUntilRaw)
	if err != nil {
		return fmt.Errorf("failed to publish assurance round: %w", err)
	}

	return nil
}

type defaultSourceQuerier struct {
	rawClock    clock.RawClock
	estClock    *clock.EstimateClock
	leapHistory *core.LeapHistory
	timeout     time.Duration
	ntsMu       sync.Mutex
	ntsState    map[string]*ntsSession
}

type ntsSession struct {
	udpEndpoint string
	c2sKey      []byte
	s2cKey      []byte
	c2sAead     cipher.AEAD
	s2cAead     cipher.AEAD
	jar         *nts.CookieJar
}

func newDefaultSourceQuerier(rc clock.RawClock, ec *clock.EstimateClock, lh *core.LeapHistory, timeout time.Duration) *defaultSourceQuerier {
	return &defaultSourceQuerier{
		rawClock:    rc,
		estClock:    ec,
		leapHistory: lh,
		timeout:     timeout,
		ntsState:    make(map[string]*ntsSession),
	}
}

func (q *defaultSourceQuerier) QuerySource(ctx context.Context, src config.SourceConfig, rawNow core.RawNanos) (*ntp.MeasurementResult, error) {
	if src.NTS {
		return q.queryNTS(ctx, src)
	}
	return q.queryNTP(ctx, src.Endpoint)
}

func (q *defaultSourceQuerier) queryNTP(ctx context.Context, endpoint string) (*ntp.MeasurementResult, error) {
	d := net.Dialer{Timeout: q.timeout}
	conn, err := d.DialContext(ctx, "udp", endpoint)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	} else {
		_ = conn.SetDeadline(time.Now().Add(q.timeout))
	}

	req := ntp.Packet{
		Version: 4,
		Mode:    3,
		Poll:    6,
	}
	reqBytes := req.Encode()

	r1 := q.rawClock.Read()
	t1Raw := r1.Raw
	var t1Unix int64
	if q.estClock != nil {
		snap := q.estClock.Snapshot()
		if est, err := snap.Evaluate(t1Raw); err == nil && est > 0 {
			projection := q.leapHistory.GstInstantToUnixProjection(est)
			if projection.IsLeapSecond {
				return nil, errors.New("local estimate is in a leap second")
			}
			t1Unix = int64(projection.Nanos)
		}
	}
	t1Unix = cmp.Or(t1Unix, time.Now().UnixNano())

	_, err = conn.Write(reqBytes)
	if err != nil {
		return nil, err
	}

	buf := make([]byte, 1024)
	n, err := conn.Read(buf)
	if err != nil {
		return nil, err
	}

	r4 := q.rawClock.Read()
	t4Raw := r4.Raw
	var t4Unix int64
	if q.estClock != nil {
		snap := q.estClock.Snapshot()
		if est, err := snap.Evaluate(t4Raw); err == nil && est > 0 {
			projection := q.leapHistory.GstInstantToUnixProjection(est)
			if projection.IsLeapSecond {
				return nil, errors.New("local estimate is in a leap second")
			}
			t4Unix = int64(projection.Nanos)
		}
	}
	t4Unix = cmp.Or(t4Unix, time.Now().UnixNano())

	resp, err := ntp.ParsePacket(buf[:n])
	if err != nil {
		return nil, err
	}

	if isKoD, code := ntp.IsKissOfDeath(resp); isKoD {
		return nil, fmt.Errorf("kiss-of-death received: %s", code)
	}

	return q.measureResponse(resp, r1, r4, t1Unix, t4Unix)
}

func (q *defaultSourceQuerier) queryNTS(ctx context.Context, src config.SourceConfig) (*ntp.MeasurementResult, error) {
	q.ntsMu.Lock()
	sess, ok := q.ntsState[src.Endpoint]
	if !ok || sess.jar.NeedsReplenishment() {
		newSess, err := q.negotiateNTSKE(ctx, src.Endpoint)
		if err != nil {
			q.ntsMu.Unlock()
			return nil, fmt.Errorf("NTS-KE negotiation failed for %s: %w", src.Endpoint, err)
		}
		sess = newSess
		q.ntsState[src.Endpoint] = sess
	}
	q.ntsMu.Unlock()

	cookie, err := sess.jar.AcquireForRequest(false)
	if err != nil {
		return nil, fmt.Errorf("failed to acquire NTS cookie: %w", err)
	}

	d := net.Dialer{Timeout: q.timeout}
	udpEndpoint := sess.udpEndpoint

	conn, err := d.DialContext(ctx, "udp", udpEndpoint)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	} else {
		_ = conn.SetDeadline(time.Now().Add(q.timeout))
	}

	reqHeader := ntp.Packet{
		Version: 4,
		Mode:    3,
		Poll:    6,
	}
	headerBytes := reqHeader.Encode()

	placeholders := sess.jar.CalculatePlaceholderCount()
	extBytes, uniqueID, err := nts.BuildProtectedRequest(headerBytes, cookie.Bytes, placeholders, sess.c2sAead, sess.c2sKey)
	if err != nil {
		return nil, fmt.Errorf("failed to build NTS protected request: %w", err)
	}

	fullPacket := append(headerBytes, extBytes...)

	r1 := q.rawClock.Read()
	t1Raw := r1.Raw
	var t1Unix int64
	if q.estClock != nil {
		snap := q.estClock.Snapshot()
		if est, err := snap.Evaluate(t1Raw); err == nil && est > 0 {
			projection := q.leapHistory.GstInstantToUnixProjection(est)
			if projection.IsLeapSecond {
				return nil, errors.New("local estimate is in a leap second")
			}
			t1Unix = int64(projection.Nanos)
		}
	}
	t1Unix = cmp.Or(t1Unix, time.Now().UnixNano())

	_, err = conn.Write(fullPacket)
	if err != nil {
		return nil, err
	}

	buf := make([]byte, 2048)
	n, err := conn.Read(buf)
	if err != nil {
		return nil, err
	}

	if n < 48 {
		return nil, errors.New("response packet too short for NTP header")
	}

	r4 := q.rawClock.Read()
	t4Raw := r4.Raw
	var t4Unix int64
	if q.estClock != nil {
		snap := q.estClock.Snapshot()
		if est, err := snap.Evaluate(t4Raw); err == nil && est > 0 {
			projection := q.leapHistory.GstInstantToUnixProjection(est)
			if projection.IsLeapSecond {
				return nil, errors.New("local estimate is in a leap second")
			}
			t4Unix = int64(projection.Nanos)
		}
	}
	t4Unix = cmp.Or(t4Unix, time.Now().UnixNano())

	resp, err := ntp.ParsePacket(buf[:n])
	if err != nil {
		return nil, err
	}

	extFields, _, err := ntp.ParseExtensionFields(buf[48:n])
	if err != nil {
		return nil, fmt.Errorf("failed to parse extension fields: %w", err)
	}

	newCookies, err := nts.VerifyAndDecryptResponse(buf[:48], extFields, uniqueID, sess.s2cAead)
	if err != nil {
		return nil, fmt.Errorf("NTS response verification failed: %w", err)
	}
	sess.jar.MarkSpent(cookie.ID)
	if len(newCookies) > 0 {
		sess.jar.AddCookies(newCookies)
	}

	return q.measureResponse(resp, r1, r4, t1Unix, t4Unix)
}

func (q *defaultSourceQuerier) measureResponse(resp *ntp.Packet, r1, r4 clock.RawReading, t1Unix, t4Unix int64) (*ntp.MeasurementResult, error) {
	t1Raw, t4Raw := r1.Raw, r4.Raw
	if resp.Version != 4 || resp.Mode != 4 || resp.Stratum < 1 || resp.Stratum > 15 || resp.LeapIndicator != 0 {
		return nil, errors.New("unsupported, unsynchronized, or leap-announcing NTP response")
	}
	coarseSec := time.Now().Unix()
	t2Unix, err := ntp.UnfoldTimestamp(resp.RecvTimestamp, coarseSec)
	if err != nil {
		return nil, fmt.Errorf("failed to unfold T2: %w", err)
	}
	t3Unix, err := ntp.UnfoldTimestamp(resp.TxTimestamp, coarseSec)
	if err != nil {
		return nil, fmt.Errorf("failed to unfold T3: %w", err)
	}

	if err := q.checkUnixInterval(t2Unix, t3Unix); err != nil {
		return nil, err
	}
	measIn := ntp.MeasurementInput{
		LocalSendRaw:                t1Raw,
		LocalRecvRaw:                t4Raw,
		T1LocalSendEstimate:         core.GstInstant(t1Unix),
		T2ServerRecv:                core.GstInstant(t2Unix),
		T3ServerTx:                  core.GstInstant(t3Unix),
		T4LocalRecvEstimate:         core.GstInstant(t4Unix),
		LocalEstimateAtMid:          core.GstInstant(t1Unix + (t4Unix-t1Unix)/2),
		ServerRootDelayNs:           resp.RootDelayNanoseconds(),
		ServerRootDispersionNs:      resp.RootDispersionNanoseconds(),
		LocalSendReadError:          core.ErrorNs(r1.ReadBound),
		LocalRecvReadError:          core.ErrorNs(r4.ReadBound),
		RemoteTimestampQuantization: 1000,
		LocalMappingIntegrationErr:  1000,
		StaticAsymmetryCorrection:   0,
		StaticAsymmetryUncertainty:  0,
		PrecisionFloorNs:            1000,
		MaxServerTurnaroundNs:       2_000_000_000,
		MaxRootDistanceNs:           16_000_000_000,
	}

	m, err := ntp.ComputeMeasurement(measIn)
	if err != nil {
		return nil, err
	}
	if err := q.checkUnixInterval(int64(m.HardInterval.Earliest), int64(m.HardInterval.Latest)); err != nil {
		return nil, err
	}
	lo, err := q.leapHistory.UnixNanosToGstInstant(core.UnixNanos(m.HardInterval.Earliest))
	if err != nil {
		return nil, err
	}
	hi, err := q.leapHistory.UnixNanosToGstInstant(core.UnixNanos(m.HardInterval.Latest))
	if err != nil {
		return nil, err
	}
	center, err := q.leapHistory.UnixNanosToGstInstant(core.UnixNanos(m.Center))
	if err != nil {
		return nil, err
	}
	m.HardInterval = core.TimeInterval{Earliest: lo, Latest: hi}
	m.Center = center
	return m, nil
}

// Only unsmeared UTC/NTP sources are supported. Reject intervals crossing the
// repeated/deleted second or the first second of a recorded transition; the wire
// timestamps and LI field do not establish a unique SI interpretation there.
func (q *defaultSourceQuerier) checkUnixInterval(lo, hi int64) error {
	if lo > hi {
		return core.ErrInvalidRange
	}
	first, last := core.FloorDiv(lo, 1_000_000_000), core.FloorDiv(hi, 1_000_000_000)
	for _, e := range q.leapHistory.Entries {
		if e.TransitionUnixSecond != math.MinInt64 && first <= e.TransitionUnixSecond && last >= e.TransitionUnixSecond-1 {
			return errors.New("NTP interval crosses an ambiguous leap boundary")
		}
	}
	return nil
}

func (q *defaultSourceQuerier) negotiateNTSKE(ctx context.Context, endpoint string) (*ntsSession, error) {
	host, port, _ := net.SplitHostPort(endpoint)
	host = cmp.Or(host, endpoint)
	port = cmp.Or(port, "4460")
	timeout := q.timeout
	if timeout <= 0 {
		timeout = 1500 * time.Millisecond
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	dialer := &tls.Dialer{NetDialer: &net.Dialer{Timeout: timeout}, Config: &tls.Config{
		MinVersion: tls.VersionTLS13, NextProtos: []string{"ntske/1"}, ServerName: host,
	}}
	connection, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(host, port))
	if err != nil {
		return nil, err
	}
	defer connection.Close()
	stop := context.AfterFunc(ctx, func() { _ = connection.Close() })
	defer stop()
	if deadline, ok := ctx.Deadline(); ok {
		_ = connection.SetDeadline(deadline)
	}
	conn := connection.(*tls.Conn)
	cs := conn.ConnectionState()
	if cs.NegotiatedProtocol != "ntske/1" {
		return nil, errors.New("NTS-KE ALPN was not negotiated")
	}
	offered := []uint16{15, 30}
	if _, err := conn.Write(nts.BuildClientRequest(offered, true)); err != nil {
		return nil, err
	}
	response, err := nts.ReadServerResponse(conn, offered)
	if err != nil {
		return nil, err
	}
	c2sKey, s2cKey, err := nts.ExportDirectionalKeys(cs.ExportKeyingMaterial, 0, response.AEAD)
	if err != nil {
		return nil, fmt.Errorf("failed to export NTS directional keys: %w", err)
	}
	c2sAead, err := nts.NewAEAD(response.AEAD, c2sKey)
	if err != nil {
		return nil, err
	}
	s2cAead, err := nts.NewAEAD(response.AEAD, s2cKey)
	if err != nil {
		return nil, err
	}
	udpHost := response.ServerName
	if udpHost == "" {
		udpHost, _, err = net.SplitHostPort(conn.RemoteAddr().String())
		if err != nil {
			return nil, err
		}
	}
	jar := nts.NewCookieJar()
	jar.AddCookies(response.Cookies)
	return &ntsSession{c2sKey: c2sKey, s2cKey: s2cKey, c2sAead: c2sAead, s2cAead: s2cAead, jar: jar,
		udpEndpoint: net.JoinHostPort(udpHost, strconv.Itoa(int(response.Port)))}, nil
}
