package matrixbot

import (
	"testing"
	"time"
)

// fakeClock pins the deterministic-time contract the scheduler relies on:
// Now() returns whatever Advance() has stepped the clock to, and timers
// fire in chronological order on Advance.
func TestFakeClockNowReflectsAdvance(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	c := newFakeClock(t0)
	if !c.Now().Equal(t0) {
		t.Errorf("Now = %v, want %v", c.Now(), t0)
	}
	c.Advance(2 * time.Minute)
	if want := t0.Add(2 * time.Minute); !c.Now().Equal(want) {
		t.Errorf("after Advance: Now = %v, want %v", c.Now(), want)
	}
}

func TestFakeClockTimerFiresOnAdvance(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	c := newFakeClock(t0)
	timer := c.NewTimer(5 * time.Minute)

	// Before the deadline: no fire.
	c.Advance(4 * time.Minute)
	select {
	case <-timer.C():
		t.Fatal("timer fired early")
	default:
	}

	// At the deadline: fire exactly once.
	c.Advance(time.Minute)
	select {
	case got := <-timer.C():
		if want := t0.Add(5 * time.Minute); !got.Equal(want) {
			t.Errorf("timer delivered %v, want %v", got, want)
		}
	case <-time.After(time.Second):
		t.Fatal("timer did not fire after Advance to deadline")
	}
}

// Multiple pending timers fire in chronological order regardless of the
// order they were created in.
func TestFakeClockTimerOrdering(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	c := newFakeClock(t0)
	t1 := c.NewTimer(10 * time.Minute)
	t2 := c.NewTimer(5 * time.Minute)

	c.Advance(5 * time.Minute)
	select {
	case <-t2.C():
	case <-time.After(time.Second):
		t.Fatal("t2 (5min) did not fire")
	}
	select {
	case <-t1.C():
		t.Fatal("t1 (10min) fired prematurely")
	default:
	}

	c.Advance(5 * time.Minute)
	select {
	case <-t1.C():
	case <-time.After(time.Second):
		t.Fatal("t1 (10min) did not fire")
	}
}

// Stop returns false once the timer has already fired.
func TestFakeClockTimerStopAfterFire(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	c := newFakeClock(t0)
	timer := c.NewTimer(time.Minute)
	c.Advance(time.Minute)
	<-timer.C()
	if timer.Stop() {
		t.Error("Stop on fired timer returned true; expected false")
	}
}

// Stop returns true on a pending timer and prevents future fires.
func TestFakeClockTimerStopBeforeFire(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	c := newFakeClock(t0)
	timer := c.NewTimer(time.Minute)
	if !timer.Stop() {
		t.Error("Stop on pending timer returned false; expected true")
	}
	c.Advance(time.Minute)
	select {
	case <-timer.C():
		t.Error("stopped timer fired anyway")
	default:
	}
}
