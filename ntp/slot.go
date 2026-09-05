package ntp

import (
	"sync"

	"gosuda.org/gstime/core"
)

// Stamp captures the single raw clock read and corresponding evaluated estimate.
type Stamp struct {
	Raw                   core.RawNanos
	Estimate              core.GstInstant
	EstimateReadError     core.ErrorNs
	MappingGeneration     core.Generation
	PublicationGeneration core.Generation
}

// RequestSlot holds binding state for an outstanding NTP request.
type RequestSlot struct {
	EndpointID           string
	RequestSerial        uint64
	ReachSerial          uint64
	SourcePortGeneration uint64
	BasicTransmitWire    [8]byte
	LocalSend            Stamp
	NtsUniqueID          []byte
	NtsCookieID          [16]byte
	DeadlineRaw          core.RawNanos
	ResponseAccepted     bool
}

// ReachabilityManager tracks the 8-epoch reachability register for an endpoint.
type ReachabilityManager struct {
	mu                 sync.Mutex
	reach              uint8
	currentReachSerial uint64
}

// NewReachabilityManager initializes a new reachability tracker.
func NewReachabilityManager() *ReachabilityManager {
	return &ReachabilityManager{
		reach:              0,
		currentReachSerial: 0,
	}
}

// OnTransmit is called for each reach-accounted transmitted poll packet.
func (r *ReachabilityManager) OnTransmit() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.currentReachSerial++
	r.reach = (r.reach << 1) & 0xff
	return r.currentReachSerial
}

// OnResponse records a valid response for the given reach serial.
func (r *ReachabilityManager) OnResponse(reachSerial uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if reachSerial > r.currentReachSerial || reachSerial == 0 {
		return
	}
	age := r.currentReachSerial - reachSerial
	if age < 8 {
		r.reach |= (1 << age)
	}
}

// Bitmap returns the current reachability bitmap.
func (r *ReachabilityManager) Bitmap() uint8 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.reach
}
