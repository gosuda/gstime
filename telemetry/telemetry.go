package telemetry

import (
	"sync"
	"sync/atomic"

	"gosuda.org/gstime/core"
)

// HealthState reports health across independent subsystems (Section 8.10).
type HealthState struct {
	AcquisitionHealthy bool
	SecurityHealthy    bool
	EstimateHealthy    bool
	AssuranceHealthy   bool
	PublicClockHealthy bool
}

// Metrics tracks runtime counters and gauges (Section 7.18).
type Metrics struct {
	mu                 sync.RWMutex
	NowCallsTotal      atomic.Uint64
	CommitWaitSuccess  atomic.Uint64
	CommitWaitFail     atomic.Uint64
	PublicationGen     atomic.Uint64
	LastPastWatermark  atomic.Int64
	LastSymmetricEpsNs atomic.Uint64
	Health             HealthState
}

// GlobalMetrics is the default shared telemetry instance.
var GlobalMetrics = &Metrics{
	Health: HealthState{
		AcquisitionHealthy: true,
		SecurityHealthy:    true,
		EstimateHealthy:    false,
		AssuranceHealthy:   false,
		PublicClockHealthy: true,
	},
}

// RecordNowCall increments Now call counter.
func (m *Metrics) RecordNowCall() {
	m.NowCallsTotal.Add(1)
}

// UpdateAssuranceWatermark records the newest certified watermark gauge.
func (m *Metrics) UpdateAssuranceWatermark(wm core.GstInstant) {
	m.LastPastWatermark.Store(int64(wm))
}

// UpdateSymmetricEpsilon records the published symmetric epsilon.
func (m *Metrics) UpdateSymmetricEpsilon(eps core.ErrorNs) {
	m.LastSymmetricEpsNs.Store(uint64(eps))
}

// SetHealth updates health state flags.
func (m *Metrics) SetHealth(h HealthState) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Health = h
}

// GetHealth returns current health state.
func (m *Metrics) GetHealth() HealthState {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.Health
}
