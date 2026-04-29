package matrixbot

import (
	"sort"
	"sync"
	"time"
)

// fakeClock is a deterministic Clock for tests. Now() returns whatever
// Advance() has stepped the clock to, and timers issued via NewTimer fire
// in chronological order whenever Advance crosses their deadline. Safe for
// concurrent use by the scheduler goroutine and the test driver.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
}

func newFakeClock(t time.Time) *fakeClock {
	return &fakeClock{now: t}
}

// Now reports the current synthetic time.
func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// NewTimer creates a timer that fires d after the current synthetic time.
// A non-positive d fires immediately, matching time.NewTimer semantics:
// the runtime delivers a stale timer the next time the receiver checks
// the channel, never blocks the test's Advance step on a zero wait.
func (c *fakeClock) NewTimer(d time.Duration) Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &fakeTimer{
		clock:    c,
		deadline: c.now.Add(d),
		ch:       make(chan time.Time, 1),
	}
	if d <= 0 {
		t.ch <- c.now
		return t
	}
	c.timers = append(c.timers, t)
	return t
}

// Advance steps the synthetic clock forward by d, firing any timers whose
// deadlines fall in the [old, new] window in chronological order. Each
// fire happens before now is updated past that timer's deadline so a
// scheduler that calls Now() inside its handler sees a time consistent
// with the deadline it just woke on.
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	target := c.now.Add(d)
	// Sort pending timers by deadline so a single Advance covering several
	// pending timers fires them in order — otherwise a 10-min timer could
	// fire before a 5-min one when both deadlines fall inside one Advance.
	sort.Slice(c.timers, func(i, j int) bool {
		return c.timers[i].deadline.Before(c.timers[j].deadline)
	})
	var due []*fakeTimer
	var remaining []*fakeTimer
	for _, t := range c.timers {
		if t.stopped {
			continue
		}
		if !t.deadline.After(target) {
			due = append(due, t)
		} else {
			remaining = append(remaining, t)
		}
	}
	c.timers = remaining
	for _, t := range due {
		c.now = t.deadline
		select {
		case t.ch <- t.deadline:
		default:
		}
	}
	c.now = target
	c.mu.Unlock()
}

type fakeTimer struct {
	clock    *fakeClock
	deadline time.Time
	ch       chan time.Time
	stopped  bool
}

func (t *fakeTimer) C() <-chan time.Time { return t.ch }

func (t *fakeTimer) Stop() bool {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	if t.stopped {
		return false
	}
	// "Already fired" is signalled by the timer being absent from the
	// pending slice (Advance moved it into `due` and removed it). A
	// pending timer is still in the slice.
	for _, p := range t.clock.timers {
		if p == t {
			t.stopped = true
			return true
		}
	}
	return false
}
