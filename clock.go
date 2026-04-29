package matrixbot

import "time"

// Clock is the production seam between the scheduler and `time.Now` /
// `time.NewTimer`. Tests inject a deterministic implementation that lets
// them advance time on demand instead of sleeping.
type Clock interface {
	// Now reports the current instant.
	Now() time.Time
	// NewTimer returns a Timer that fires once after d has elapsed,
	// measured against this clock.
	NewTimer(d time.Duration) Timer
}

// Timer is a Clock-issued timer. It mirrors only the part of time.Timer's
// API the scheduler uses — a channel that delivers one fire time, plus
// Stop. Anything else (Reset, in particular) is intentionally absent.
type Timer interface {
	// C returns the channel on which the timer's fire time is delivered.
	C() <-chan time.Time
	// Stop prevents the timer from firing. Returns true if the call stops
	// the timer, false if the timer has already expired or been stopped.
	Stop() bool
}

// realClock is the production Clock. It does no buffering — every call
// goes straight to the standard library.
type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

func (realClock) NewTimer(d time.Duration) Timer {
	return &realTimer{t: time.NewTimer(d)}
}

type realTimer struct {
	t *time.Timer
}

func (r *realTimer) C() <-chan time.Time { return r.t.C }
func (r *realTimer) Stop() bool          { return r.t.Stop() }
