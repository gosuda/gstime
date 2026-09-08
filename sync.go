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

var (
	errNilService          = errors.New("clock service cannot be nil")
	errNoSources           = errors.New("configuration has no upstream sources")
	errEngineClosed        = errors.New("sync engine is closed")
	errEngineRunning       = errors.New("sync engine is already running")
	errRawChanged          = errors.New("raw clock changed during query round")
	errLocalLeap           = errors.New("local estimate is in a leap second")
	errUnsupportedResponse = errors.New("unsupported, unsynchronized, or leap-announcing NTP response")
	errLeapBoundary        = errors.New("NTP interval crosses an ambiguous leap boundary")
	errALPN                = errors.New("NTS-KE ALPN was not negotiated")
	errNoCookies           = errors.New("NTS-KE supplied no usable cookies")
	errOriginMismatch      = errors.New("NTP origin does not match request")
	errMappingChanged      = errors.New("estimate step during NTP exchange")
)

// SourceQuerier abstracts network interactions for NTP/NTS queries.
// Implementations must acquire fresh evidence during this round, on the supplied
// service raw clock and its current continuity/scale envelope. Cached results
// from an earlier round or clock generation must not be returned. HardInterval
// describes GST time at RawMid, not at query completion. RawMidReadBound must
// bound acquisition error in that raw coordinate (zero explicitly means exact).
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

	pollMu           sync.Mutex
	mu               sync.Mutex
	wg               sync.WaitGroup
	cancel           context.CancelFunc
	running          atomic.Bool
	lifetime         context.Context
	shutdown         context.CancelFunc
	done             chan struct{}
	estimates        map[string]*endpointEstimate
	reference        clock.EstimateMapping
	referenceToken   uint64
	referenceStep    core.Generation
	controlTarget    core.GstInstant
	hasControlTarget bool
	closed           atomic.Bool
}

// NewSyncEngine initializes a new SyncEngine for the given configuration and ClockService.
func NewSyncEngine(cfg config.Config, svc *ClockService, opts ...SyncOption) (*SyncEngine, error) {
	if svc == nil {
		return nil, errNilService
	}
	id, err := cfg.ConfigID()
	if err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}
	if len(cfg.Sources) == 0 {
		return nil, errNoSources
	}
	if id != svc.configID || cfg.Assurance.MaxWidthNs != svc.assuranceClock.Snapshot().MaxAssuranceWidthNs {
		return nil, ErrConfigurationMismatch
	}
	cfg.Sources = append([]config.SourceConfig(nil), cfg.Sources...)
	lifetime, shutdown := context.WithCancel(context.Background())

	engine := &SyncEngine{
		cfg:          cfg,
		svc:          svc,
		pollIntvl:    2 * time.Second,
		queryTimeout: 1500 * time.Millisecond,
		lifetime:     lifetime, shutdown: shutdown, done: make(chan struct{}),
	}

	for _, opt := range opts {
		opt(engine)
	}

	if engine.querier == nil {
		querier := newDefaultSourceQuerier(svc.rawClock, svc.estimateClock, svc.leapHistory, engine.queryTimeout)
		querier.service = svc
		engine.querier = querier
	}

	return engine, nil
}

// Start launches background polling under the provided context.
// It is non-blocking and spawns a single worker goroutine managed by the engine.
func (e *SyncEngine) Start(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.closed.Load() {
		return errEngineClosed
	}
	if e.running.Swap(true) {
		return errEngineRunning
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
	if e.closed.Swap(true) {
		e.mu.Unlock()
		<-e.done
		return nil
	}
	e.shutdown()
	if e.cancel != nil {
		e.cancel()
	}
	e.mu.Unlock()
	e.wg.Wait()
	e.running.Store(false)
	close(e.done)
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
	if err := ctx.Err(); err != nil {
		return err
	}
	e.mu.Lock()
	if e.closed.Load() {
		e.mu.Unlock()
		return errEngineClosed
	}
	e.wg.Add(1)
	e.mu.Unlock()
	defer e.wg.Done()
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(e.lifetime, cancel)
	defer func() { stop(); cancel() }()
	e.pollMu.Lock()
	defer e.pollMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	defer func() {
		// Parent cancellation or engine shutdown is not a failed evidence round.
		// A per-source timeout with a live parent still counts as a failure.
		if pollErr != nil && ctx.Err() == nil && !e.closed.Load() {
			pollErr = errors.Join(pollErr, e.svc.markRoundFailure(e.cfg.Assurance.MaxHoldoverAgeNs))
		}
	}()
	startReading := e.svc.rawClock.Read()
	scaleLow, scaleUpp := e.svc.rawClock.ScaleEnvelope()

	type sourceResult struct {
		domain string
		source config.SourceConfig
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
			results <- sourceResult{domain: s.FaultDomainID, source: s, res: res, err: err}
		}(src)
	}

	wg.Wait()
	close(results)
	if e.closed.Load() {
		return errEngineClosed
	}

	// Every interval must refer to this same reading, not to its individual RawMid.
	reading := e.svc.rawClock.Read()
	nowRaw := reading.Raw
	currentLow, currentUpp := e.svc.rawClock.ScaleEnvelope()
	if err := ctx.Err(); err != nil {
		return err
	}
	sameDomain := startReading.ContinuityToken != 0 && reading.ContinuityToken == startReading.ContinuityToken && reading.BackendGeneration == startReading.BackendGeneration
	sameScale := currentLow == scaleLow && currentUpp == scaleUpp
	if !sameDomain || !sameScale || nowRaw < startReading.Raw {
		return errRawChanged
	}
	maxAge := e.cfg.Assurance.MaxAgeNs
	if uint64(nowRaw) > math.MaxUint64-uint64(maxAge) {
		return core.ErrOverflow
	}
	byDomain := make(map[string][]core.TimeInterval)
	var samples []roundMeasurement
	var queryErrors []error
	for r := range results {
		if r.err != nil {
			queryErrors = append(queryErrors, fmt.Errorf("query %s: %w", r.source.Endpoint, r.err))
			continue
		}
		if r.res == nil || r.domain == "" {
			continue
		}
		if r.res.RawMid < startReading.Raw || r.res.RawMid > nowRaw || nowRaw-r.res.RawMid > core.RawNanos(maxAge) {
			continue
		}
		sample := &assurance.AssuranceAnchor{
			RawAnchor: r.res.RawMid, LowerAtAnchor: r.res.HardInterval.Earliest, UpperAtAnchor: r.res.HardInterval.Latest,
			RawScaleLower: scaleLow, RawScaleUpper: scaleUpp, RawReadBound: r.res.RawMidReadBound,
			ContinuityToken: reading.ContinuityToken, ValidUntilRaw: nowRaw,
		}
		inv, err := assurance.PropagateAnchor(sample, nowRaw, reading.ReadBound, reading.ContinuityToken, 0, 0, e.cfg.Assurance.MaxWidthNs)
		if err != nil {
			continue
		}
		byDomain[r.domain] = append(byDomain[r.domain], inv)
		samples = append(samples, roundMeasurement{source: r.source, result: r.res})
	}
	var domains []string
	for domain := range byDomain {
		domains = append(domains, domain)
	}
	sort.Strings(domains)
	var intervals []core.TimeInterval
	eligible := make(map[string]bool, len(domains))
	for _, domain := range domains {
		consolidated := source.ConsolidateDomainIntervals(core.FaultDomainID(domain), byDomain[domain])
		if !consolidated.Inconsistent {
			intervals = append(intervals, consolidated.Interval)
			eligible[domain] = true
		}
	}

	minDomains := e.cfg.Assurance.MinVotingDomains
	if minDomains <= 0 {
		minDomains = 1
	}

	if len(intervals) < minDomains {
		return errors.Join(fmt.Errorf("insufficient voting domains: got %d, required %d", len(intervals), minDomains), errors.Join(queryErrors...))
	}

	faultBudget := e.cfg.Assurance.FaultBudget
	minHonest := e.cfg.Assurance.MinHonestCoverage
	if minHonest <= 0 {
		minHonest = 1
	}
	target, rate, err := e.selectControl(reading, intervals, samples, eligible)
	if err != nil {
		return err
	}
	prior := target
	if e.hasControlTarget {
		prior = e.controlTarget
	}

	consensus, err := source.ComputeAssuranceConsensus(
		intervals,
		faultBudget,
		minDomains,
		minHonest,
		prior,
		target,
	)
	if err != nil {
		return fmt.Errorf("consensus computation failed: %w", err)
	}

	validUntilRaw := nowRaw + core.RawNanos(maxAge)
	err = e.svc.publishSyncRound(reading, scaleLow, scaleUpp, consensus, rate, validUntilRaw)
	if err != nil {
		return fmt.Errorf("failed to publish assurance round: %w", err)
	}
	e.controlTarget, e.hasControlTarget = consensus.ControlTarget, true
	e.referenceStep = e.svc.estimateClock.Snapshot().StepGeneration

	return nil
}

type defaultSourceQuerier struct {
	rawClock    clock.RawClock
	estClock    *clock.EstimateClock
	leapHistory *core.LeapHistory
	timeout     time.Duration
	ntsMu       sync.Mutex
	ntsState    map[string]*ntsSession
	service     *ClockService
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

// localUnixEstimate applies the same leap policy and wall-clock fallback to
// send and receive readings in both acquisition transports.
func (q *defaultSourceQuerier) localUnixEstimate(raw core.RawNanos) (int64, clock.EstimateMapping, error) {
	var mapping clock.EstimateMapping
	if q.estClock != nil {
		var err error
		mapping, err = q.estClock.MappingAt(raw)
		if err != nil && !errors.Is(err, core.ErrUnanchored) {
			return 0, mapping, err
		}
		if err == nil {
			estimate, err := mapping.Evaluate(raw)
			if err != nil {
				return 0, mapping, err
			}
			projection := q.leapHistory.GstInstantToUnixProjection(estimate)
			if projection.IsLeapSecond {
				return 0, mapping, errLocalLeap
			}
			return int64(projection.Nanos), mapping, nil
		}
		mapping = q.estClock.Snapshot()
		if mapping.Anchored {
			return 0, mapping, errMappingChanged
		}
	}
	return time.Now().UnixNano(), mapping, nil
}

func (q *defaultSourceQuerier) queryNTP(ctx context.Context, endpoint string) (result *ntp.MeasurementResult, queryErr error) {
	d := net.Dialer{Timeout: q.timeout}
	address := endpoint
	if _, _, err := net.SplitHostPort(address); err != nil {
		address = net.JoinHostPort(address, "123")
	}
	conn, err := d.DialContext(ctx, "udp", address)
	if err != nil {
		return nil, err
	}
	defer func() { queryErr = closeQuery(ctx, conn, queryErr) }()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	if err := setQueryDeadline(ctx, conn, q.timeout); err != nil {
		return nil, err
	}
	req, err := newNTPRequest()
	if err != nil {
		return nil, err
	}
	request := req.Encode()
	send, err := q.captureStamp()
	if err != nil {
		return nil, err
	}
	slot := ntp.RequestSlot{EndpointID: endpoint, BasicTransmitWire: req.TxTimestamp, LocalSend: send}
	if _, err := conn.Write(request); err != nil {
		return nil, err
	}
	buf := make([]byte, nts.MaxPacketSize+1)
	n, err := conn.Read(buf)
	if err != nil {
		return nil, err
	}
	received, err := q.captureStamp()
	if err != nil {
		return nil, err
	}
	if n > nts.MaxPacketSize {
		return nil, nts.ErrPacketTooLarge
	}
	response, err := ntp.ParsePacket(buf[:n])
	if err != nil {
		return nil, err
	}
	if response.OrigTimestamp != slot.BasicTransmitWire {
		return nil, errOriginMismatch
	}
	if isKoD, code := ntp.IsKissOfDeath(response); isKoD {
		return nil, fmt.Errorf("kiss-of-death received: %s", code)
	}
	return q.measureResponse(response, slot.LocalSend, received)
}

func (q *defaultSourceQuerier) queryNTS(ctx context.Context, src config.SourceConfig) (result *ntp.MeasurementResult, queryErr error) {
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
	defer sess.jar.MarkSpent(cookie.ID)
	d := net.Dialer{Timeout: q.timeout}
	conn, err := d.DialContext(ctx, "udp", sess.udpEndpoint)
	if err != nil {
		return nil, err
	}
	defer func() { queryErr = closeQuery(ctx, conn, queryErr) }()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	if err := setQueryDeadline(ctx, conn, q.timeout); err != nil {
		return nil, err
	}
	req, err := newNTPRequest()
	if err != nil {
		return nil, err
	}
	header := req.Encode()
	extensions, uid, err := nts.BuildProtectedRequest(header, cookie.Bytes, sess.jar.CalculatePlaceholderCount(), sess.c2sAead, sess.c2sKey)
	if err != nil {
		return nil, fmt.Errorf("build protected NTS request: %w", err)
	}
	request := append(header, extensions...)
	send, err := q.captureStamp()
	if err != nil {
		return nil, err
	}
	slot := ntp.RequestSlot{EndpointID: sess.udpEndpoint, BasicTransmitWire: req.TxTimestamp, LocalSend: send, NtsUniqueID: uid, NtsCookieID: cookie.ID}
	if _, err := conn.Write(request); err != nil {
		return nil, err
	}
	buf := make([]byte, nts.MaxPacketSize+1)
	n, err := conn.Read(buf)
	if err != nil {
		return nil, err
	}
	received, err := q.captureStamp()
	if err != nil {
		return nil, err
	}
	if n > nts.MaxPacketSize {
		return nil, nts.ErrPacketTooLarge
	}
	response, err := ntp.ParsePacket(buf[:n])
	if err != nil {
		return nil, err
	}
	if response.OrigTimestamp != slot.BasicTransmitWire {
		return nil, errOriginMismatch
	}
	fields, trailing, err := ntp.ParseExtensionFields(buf[ntp.HeaderSize:n])
	if err != nil {
		return nil, err
	}
	if len(trailing) != 0 {
		return nil, ntp.ErrMalformedExtension
	}
	cookies, err := nts.VerifyAndDecryptResponse(buf[:ntp.HeaderSize], fields, slot.NtsUniqueID, sess.s2cAead)
	if err != nil {
		return nil, fmt.Errorf("NTS response verification failed: %w", err)
	}
	sess.jar.MarkSpent(cookie.ID)
	sess.jar.AddCookies(cookies)
	return q.measureResponse(response, slot.LocalSend, received)
}

func (q *defaultSourceQuerier) measureResponse(resp *ntp.Packet, send, received ntp.Stamp) (*ntp.MeasurementResult, error) {
	if resp.Version != 4 || resp.Mode != 4 || resp.Stratum < 1 || resp.Stratum > 15 || resp.LeapIndicator != 0 {
		return nil, errUnsupportedResponse
	}
	sameDomain := send.ContinuityToken != 0 && send.ContinuityToken == received.ContinuityToken && send.BackendGeneration == received.BackendGeneration
	sameScale := send.RawScaleLower == received.RawScaleLower && send.RawScaleUpper == received.RawScaleUpper
	if !sameDomain || !sameScale || received.Raw < send.Raw {
		return nil, errRawChanged
	}
	if send.StepGeneration != received.StepGeneration || send.EstimateAnchored != received.EstimateAnchored {
		return nil, errMappingChanged
	}
	coarse := q.leapHistory.GstInstantToUnixProjection(send.Estimate)
	coarseSec := core.FloorDiv(int64(coarse.Nanos), 1_000_000_000)
	t2Unix, err := ntp.UnfoldTimestamp(resp.RecvTimestamp, coarseSec)
	if err != nil {
		return nil, err
	}
	t3Unix, err := ntp.UnfoldTimestamp(resp.TxTimestamp, coarseSec)
	if err != nil {
		return nil, err
	}
	if err := q.checkUnixInterval(t2Unix, t3Unix); err != nil {
		return nil, err
	}
	t2, err := q.leapHistory.UnixNanosToGstInstant(core.UnixNanos(t2Unix))
	if err != nil {
		return nil, err
	}
	t3, err := q.leapHistory.UnixNanosToGstInstant(core.UnixNanos(t3Unix))
	if err != nil {
		return nil, err
	}
	mid := send.Raw + (received.Raw-send.Raw)/2
	var estimateAtMid core.GstInstant
	if send.EstimateAnchored {
		mapping, err := q.estClock.MappingAt(mid)
		if err != nil || mapping.StepGeneration != send.StepGeneration {
			return nil, errors.Join(errMappingChanged, err)
		}
		estimateAtMid, err = mapping.Evaluate(mid)
		if err != nil {
			return nil, err
		}
	} else {
		if received.Raw-send.Raw > math.MaxInt64 {
			return nil, core.ErrOverflow
		}
		estimateAtMid, err = core.CheckedInstantAdd(send.Estimate, int64(mid-send.Raw))
		if err != nil {
			return nil, err
		}
		received.Estimate, err = core.CheckedInstantAdd(send.Estimate, int64(received.Raw-send.Raw))
		if err != nil {
			return nil, err
		}
	}
	m, err := ntp.ComputeMeasurement(ntp.MeasurementInput{
		LocalSendRaw: send.Raw, LocalRecvRaw: received.Raw,
		RawScaleLower: send.RawScaleLower, RawScaleUpper: send.RawScaleUpper,
		T1LocalSendEstimate: send.Estimate, T2ServerRecv: t2, T3ServerTx: t3, T4LocalRecvEstimate: received.Estimate,
		LocalEstimateAtMid: estimateAtMid,
		ServerRootDelayNs:  resp.RootDelayNanoseconds(), ServerRootDispersionNs: resp.RootDispersionNanoseconds(),
		LocalSendReadError: send.RawReadBound, LocalRecvReadError: received.RawReadBound,
		RemoteTimestampQuantization: 1000, ProtocolConversionError: 1,
		LocalMappingIntegrationErr: send.EstimateReadError + received.EstimateReadError,
		PrecisionFloorNs:           1000, MaxServerTurnaroundNs: 2_000_000_000, MaxRootDistanceNs: 16_000_000_000,
	})
	if err != nil {
		return nil, err
	}
	lo := q.leapHistory.GstInstantToUnixProjection(m.HardInterval.Earliest)
	hi := q.leapHistory.GstInstantToUnixProjection(m.HardInterval.Latest)
	if lo.IsLeapSecond || hi.IsLeapSecond {
		return nil, errLeapBoundary
	}
	if err := q.checkUnixInterval(int64(lo.Nanos), int64(hi.Nanos)); err != nil {
		return nil, err
	}
	m.HasEstimate = send.EstimateAnchored
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
			return errLeapBoundary
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
		return nil, errALPN
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
	if jar.AddCookies(response.Cookies) == 0 {
		return nil, errNoCookies
	}
	return &ntsSession{c2sKey: c2sKey, s2cKey: s2cKey, c2sAead: c2sAead, s2cAead: s2cAead, jar: jar,
		udpEndpoint: net.JoinHostPort(udpHost, strconv.Itoa(int(response.Port)))}, nil
}
