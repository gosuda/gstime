package publish

import (
	"fmt"
	"sync"
	"sync/atomic"
)

// Publisher manages linearizable, race-free snapshot publication (Sections 7.10-7.12).
type Publisher struct {
	writerMu sync.Mutex
	active   atomic.Pointer[Snapshot]
}

// NewPublisher creates a new publisher initialized with an unanchored snapshot.
func NewPublisher(initial *Snapshot) *Publisher {
	p := &Publisher{}
	if initial != nil {
		p.active.Store(initial)
	}
	return p
}

// Publish atomically publishes a new validated snapshot.
func (p *Publisher) Publish(next *Snapshot) error {
	p.writerMu.Lock()
	defer p.writerMu.Unlock()

	curr := p.active.Load()
	if err := ValidatePublication(curr, next); err != nil {
		return fmt.Errorf("publication guard rejected snapshot: %w", err)
	}

	p.active.Store(next)
	return nil
}

// Acquire returns the current immutable snapshot with acquire semantics (fast path).
func (p *Publisher) Acquire() *Snapshot {
	return p.active.Load()
}
