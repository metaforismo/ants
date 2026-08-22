package storetest

import (
	"sync"
	"time"
)

// AdvancingClock is a ports.Clock whose instant moves only when a test moves
// it, making lease expiry and backoff windows deterministic.
type AdvancingClock struct {
	mu sync.Mutex
	t  time.Time
}

func NewAdvancingClock() *AdvancingClock {
	return &AdvancingClock{t: time.Date(2026, 8, 22, 9, 0, 0, 0, time.UTC)}
}

func (c *AdvancingClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

// Advance moves the clock forward by d.
func (c *AdvancingClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}
