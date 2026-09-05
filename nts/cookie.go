package nts

import (
	"crypto/sha256"
	"sync"
)

type CookieState uint8

const (
	CookieUnused CookieState = iota
	CookieInFlight
	CookieSpent
)

// ManagedCookie tracks an individual cookie and its state.
type ManagedCookie struct {
	ID    [16]byte
	Bytes []byte
	State CookieState
}

// CookieJar manages single-ownership cookie inventory according to Section 3.9.
type CookieJar struct {
	mu            sync.Mutex
	capacity      int
	target        int
	lowWatermark  int
	burstReserve  int
	cookies       []*ManagedCookie
	reusedCount   int
	spentCount    int
	inFlightCount int
}

// NewCookieJar creates a new CookieJar with default normative limits.
func NewCookieJar() *CookieJar {
	return &CookieJar{
		capacity:     8,
		target:       8,
		lowWatermark: 2,
		burstReserve: 1,
		cookies:      make([]*ManagedCookie, 0, 8),
	}
}

// AddCookies adds newly decrypted cookies into the UNUSED inventory up to capacity.
// Empty cookies and cookies larger than MaxCookieSize are not retained.
func (cj *CookieJar) AddCookies(newCookies [][]byte) int {
	cj.mu.Lock()
	defer cj.mu.Unlock()

	added := 0
	for _, raw := range newCookies {
		if !validCookieSize(len(raw)) {
			continue
		}
		// Clean up spent cookies if at capacity
		if len(cj.cookies) >= cj.capacity {
			cj.evictSpentLocked()
		}
		if len(cj.cookies) >= cj.capacity {
			break
		}

		hash := sha256.Sum256(raw)
		var id [16]byte
		copy(id[:], hash[:16])

		c := &ManagedCookie{
			ID:    id,
			Bytes: append([]byte(nil), raw...),
			State: CookieUnused,
		}
		cj.cookies = append(cj.cookies, c)
		added++
	}
	return added
}

func (cj *CookieJar) evictSpentLocked() {
	var active []*ManagedCookie
	for _, c := range cj.cookies {
		if c.State != CookieSpent {
			active = append(active, c)
		}
	}
	cj.cookies = active
}

// Counts returns counts of (unused, inFlight, spent).
func (cj *CookieJar) Counts() (unused, inFlight, spent int) {
	cj.mu.Lock()
	defer cj.mu.Unlock()

	for _, c := range cj.cookies {
		switch c.State {
		case CookieUnused:
			unused++
		case CookieInFlight:
			inFlight++
		case CookieSpent:
			spent++
		}
	}
	return unused, inFlight, spent
}

// NeedsReplenishment returns true if unused cookies are at or below the low watermark.
func (cj *CookieJar) NeedsReplenishment() bool {
	unused, _, _ := cj.Counts()
	return unused <= cj.lowWatermark
}

// AcquireForRequest attempts to consume one UNUSED cookie for a request.
// If isBurst is true, at least burstReserve cookies must remain.
func (cj *CookieJar) AcquireForRequest(isBurst bool) (*ManagedCookie, error) {
	cj.mu.Lock()
	defer cj.mu.Unlock()

	var unused []*ManagedCookie
	for _, c := range cj.cookies {
		if c.State == CookieUnused {
			unused = append(unused, c)
		}
	}

	if len(unused) == 0 {
		return nil, ErrCookieStarvation
	}

	if isBurst && len(unused) <= cj.burstReserve {
		return nil, ErrCookieStarvation
	}

	selected := unused[0]
	selected.State = CookieInFlight
	cj.inFlightCount++

	return selected, nil
}

// MarkSpent atomically marks an IN_FLIGHT cookie as SPENT.
func (cj *CookieJar) MarkSpent(cookieID [16]byte) {
	cj.mu.Lock()
	defer cj.mu.Unlock()

	for _, c := range cj.cookies {
		if c.ID == cookieID && c.State == CookieInFlight {
			c.State = CookieSpent
			cj.inFlightCount--
			cj.spentCount++
			break
		}
	}
}

// CalculatePlaceholderCount calculates how many Cookie Placeholders to request
// so the projected post-response inventory does not exceed COOKIE_TARGET.
// This is an inventory upper bound; BuildProtectedRequest also applies the
// selected cookie's wire-size budget.
func (cj *CookieJar) CalculatePlaceholderCount() int {
	unused, inFlight, _ := cj.Counts()
	projected := unused + inFlight
	needed := cj.target - projected
	if needed < 0 {
		return 0
	}
	return min(needed, MaxPlaceholderCount)
}
