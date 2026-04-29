package matrixbot

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

// recordingHandler captures every Request the scheduler dispatches to it
// so tests can assert on what arrived. Safe for concurrent use.
type recordingHandler struct {
	mu  sync.Mutex
	got []Request
}

func (h *recordingHandler) Handle(_ context.Context, req Request) (Response, error) {
	h.mu.Lock()
	h.got = append(h.got, req)
	h.mu.Unlock()
	return Response{}, nil
}

func (h *recordingHandler) calls() []Request {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]Request, len(h.got))
	copy(out, h.got)
	return out
}

// waitForCalls polls until h has received n calls, failing the test if a
// short deadline elapses first. Polling beats sleeping: tests stay fast
// when the scheduler is fast, and only the failure path waits the full
// timeout.
func waitForCalls(t *testing.T, h *recordingHandler, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(h.calls()) >= n {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("waited 2s for %d handler calls, got %d", n, len(h.calls()))
}

// schedulerTestBot wires up a Bot with a fakeSender so the scheduler can
// dispatch through the regular send path without touching a homeserver.
func schedulerTestBot(t *testing.T) (*Bot, *fakeSender) {
	t.Helper()
	sender := &fakeSender{}
	bot := newTestBot(sender, sender)
	return bot, sender
}

// TestSchedulerFiresAtNextTime: register a single schedule whose first
// fire is 5 minutes away; advancing the fake clock to that instant must
// dispatch the route's handler exactly once with the configured Input,
// the registered RoomID, and Sender == botUserID.
func TestSchedulerFiresAtNextTime(t *testing.T) {
	bot, _ := schedulerTestBot(t)
	t0 := time.Date(2026, 4, 27, 12, 0, 0, 0, time.UTC)
	clock := newFakeClock(t0)

	tr := &ScheduleTrigger{
		Schedule: mustParseCron(t, "5 12 * * *"), // 12:05 daily
		CronExpr: "5 12 * * *",
		Input:    "ping",
	}
	h := &recordingHandler{}
	roomID := id.RoomID("!sched:e")
	bot.RouteIn(roomID, tr, h)

	dir := t.TempDir()
	path := filepath.Join(dir, "schedule.json")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		runScheduler(ctx, bot, clock, path)
		close(done)
	}()

	// Give the scheduler a moment to enter its sleep on the timer it
	// created at startup. The fakeClock's Advance only fires timers
	// already registered, so we need to wait until the goroutine has
	// scheduled itself.
	waitForTimerArmed(clock, 1)

	clock.Advance(5 * time.Minute)
	waitForCalls(t, h, 1)

	got := h.calls()[0]
	if got.Input != "ping" {
		t.Errorf("Input = %q, want %q", got.Input, "ping")
	}
	if got.RoomID != roomID {
		t.Errorf("RoomID = %q, want %q", got.RoomID, roomID)
	}
	if got.Sender != bot.botUserID {
		t.Errorf("Sender = %q, want %q", got.Sender, bot.botUserID)
	}

	cancel()
	<-done
}

// TestSchedulerSkipsTickableTriggersForEvents pins the defence-in-depth
// invariant: when the regular Matrix-event dispatch path calls Apply on a
// ScheduleTrigger with a real event, the trigger MUST return no-match.
// Otherwise an incoming message could double-fire a route that already
// runs on a clock.
func TestSchedulerSkipsTickableTriggersForEvents(t *testing.T) {
	bot, _ := schedulerTestBot(t)
	tr := &ScheduleTrigger{
		Schedule: mustParseCron(t, "0 9 * * *"),
		CronExpr: "0 9 * * *",
		Input:    "should-not-fire",
	}
	called := false
	bot.RouteIn(id.RoomID("!r:e"), tr, HandlerFunc(func(_ context.Context, _ Request) (Response, error) {
		called = true
		return Response{}, nil
	}))
	bot.dispatch(context.Background(), schedulerTestMessageEvent("!r:e", "@u:e"))
	if called {
		t.Error("schedule trigger fired on a real Matrix event; the dispatch path must skip Tickable triggers for events")
	}
}

// TestSchedulerPersistsNextFire: after a fire, schedule.json contains the
// new next-fire time keyed by the same scheduleID the registration uses.
func TestSchedulerPersistsNextFire(t *testing.T) {
	bot, _ := schedulerTestBot(t)
	t0 := time.Date(2026, 4, 27, 12, 0, 0, 0, time.UTC)
	clock := newFakeClock(t0)

	tr := &ScheduleTrigger{
		Schedule: mustParseCron(t, "5 12 * * *"),
		CronExpr: "5 12 * * *",
		Input:    "ping",
	}
	h := &recordingHandler{}
	roomID := id.RoomID("!r:e")
	bot.RouteIn(roomID, tr, h)

	dir := t.TempDir()
	path := filepath.Join(dir, "schedule.json")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		runScheduler(ctx, bot, clock, path)
		close(done)
	}()

	waitForTimerArmed(clock, 1)
	clock.Advance(5 * time.Minute)
	waitForCalls(t, h, 1)

	// The scheduler persists after firing; the next iteration's timer
	// being armed means the persistence call has returned. Wait for that
	// before asserting on the file.
	waitForTimerArmed(clock, 1)
	// One extra poll to give the persistence write a moment to land.
	deadline := time.Now().Add(2 * time.Second)
	wantKey := scheduleID(roomID, "5 12 * * *", "ping")
	for time.Now().Before(deadline) {
		got, err := loadScheduleStore(path)
		if err == nil {
			if persisted, ok := got[wantKey]; ok {
				want := time.Date(2026, 4, 28, 12, 5, 0, 0, time.UTC)
				if !persisted.Equal(want) {
					t.Fatalf("next-fire = %v, want %v", persisted, want)
				}
				cancel()
				<-done
				return
			}
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("schedule.json never gained key %q", wantKey)
}

// TestSchedulerCatchUpOnRestart pre-populates schedule.json with a
// next-fire timestamp that is now in the past, then starts the scheduler.
// The handler must fire once at startup (catch-up) and the next persisted
// fire time must be the cron's next instant strictly after `now` — not
// the stale time from disk repeated.
func TestSchedulerCatchUpOnRestart(t *testing.T) {
	bot, _ := schedulerTestBot(t)
	// Clock starts at noon Tuesday; the persisted "next fire" is yesterday's
	// 09:00, so the catch-up should trigger immediately and the next fire
	// should be 09:00 Wednesday.
	t0 := time.Date(2026, 4, 28, 12, 0, 0, 0, time.UTC)
	clock := newFakeClock(t0)

	tr := &ScheduleTrigger{
		Schedule: mustParseCron(t, "0 9 * * *"),
		CronExpr: "0 9 * * *",
		Input:    "morning",
	}
	h := &recordingHandler{}
	roomID := id.RoomID("!r:e")
	bot.RouteIn(roomID, tr, h)

	dir := t.TempDir()
	path := filepath.Join(dir, "schedule.json")
	stale := time.Date(2026, 4, 27, 9, 0, 0, 0, time.UTC) // yesterday morning
	wantKey := scheduleID(roomID, "0 9 * * *", "morning")
	if err := saveScheduleStore(path, map[string]time.Time{wantKey: stale}); err != nil {
		t.Fatalf("seed schedule.json: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		runScheduler(ctx, bot, clock, path)
		close(done)
	}()

	waitForCalls(t, h, 1)

	// After the catch-up fire, the persisted next-fire must advance to
	// the cron's next instant strictly after t0 — i.e. tomorrow 09:00.
	want := time.Date(2026, 4, 29, 9, 0, 0, 0, time.UTC)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		got, err := loadScheduleStore(path)
		if err == nil && got[wantKey].Equal(want) {
			cancel()
			<-done
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	got, _ := loadScheduleStore(path)
	t.Fatalf("after catch-up, schedule.json[%q] = %v, want %v", wantKey, got[wantKey], want)
}

// TestSchedulerSleepsUntilEarliest: with two schedules at different
// future times, the scheduler must fire them in chronological order and
// only after the clock crosses each's deadline.
func TestSchedulerSleepsUntilEarliest(t *testing.T) {
	bot, _ := schedulerTestBot(t)
	t0 := time.Date(2026, 4, 27, 12, 0, 0, 0, time.UTC)
	clock := newFakeClock(t0)

	earlier := &ScheduleTrigger{
		Schedule: mustParseCron(t, "5 12 * * *"), // first fire 12:05
		CronExpr: "5 12 * * *",
		Input:    "earlier",
	}
	later := &ScheduleTrigger{
		Schedule: mustParseCron(t, "10 12 * * *"), // first fire 12:10
		CronExpr: "10 12 * * *",
		Input:    "later",
	}
	h := &recordingHandler{}
	bot.RouteIn(id.RoomID("!r:e"), earlier, h)
	bot.RouteIn(id.RoomID("!r:e"), later, h)

	dir := t.TempDir()
	path := filepath.Join(dir, "schedule.json")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		runScheduler(ctx, bot, clock, path)
		close(done)
	}()

	waitForTimerArmed(clock, 1)
	clock.Advance(5 * time.Minute) // 12:05 → earlier fires
	waitForCalls(t, h, 1)
	waitForTimerArmed(clock, 1)
	clock.Advance(5 * time.Minute) // 12:10 → later fires
	waitForCalls(t, h, 2)

	got := h.calls()
	if got[0].Input != "earlier" {
		t.Errorf("first call Input = %q, want %q", got[0].Input, "earlier")
	}
	if got[1].Input != "later" {
		t.Errorf("second call Input = %q, want %q", got[1].Input, "later")
	}

	cancel()
	<-done
}

// TestSchedulerMultipleScheduleSameKey: two registrations with identical
// (room, cron, input) share an ID and only fire once per tick.
func TestSchedulerMultipleScheduleSameKey(t *testing.T) {
	bot, _ := schedulerTestBot(t)
	t0 := time.Date(2026, 4, 27, 12, 0, 0, 0, time.UTC)
	clock := newFakeClock(t0)

	mk := func() *ScheduleTrigger {
		return &ScheduleTrigger{
			Schedule: mustParseCron(t, "5 12 * * *"),
			CronExpr: "5 12 * * *",
			Input:    "dup",
		}
	}
	h := &recordingHandler{}
	roomID := id.RoomID("!r:e")
	bot.RouteIn(roomID, mk(), h)
	bot.RouteIn(roomID, mk(), h)

	dir := t.TempDir()
	path := filepath.Join(dir, "schedule.json")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		runScheduler(ctx, bot, clock, path)
		close(done)
	}()

	waitForTimerArmed(clock, 1)
	clock.Advance(5 * time.Minute)
	waitForCalls(t, h, 1)

	// Give the scheduler time to spuriously double-fire if it's broken.
	// The fake clock isn't going to deliver another fire on its own, so
	// at most one more handler call could come from a duplicate
	// registration sharing the same instant — wait then assert.
	time.Sleep(20 * time.Millisecond)
	if got := len(h.calls()); got != 1 {
		t.Errorf("handler fired %d times, want 1 (duplicates must dedupe)", got)
	}

	cancel()
	<-done
}

// TestBotRunStartsSchedulerWhenScheduleRouteRegistered pins the wire-up
// in Bot.Run: registering a tickable route must cause the scheduler
// goroutine to fire it on tick, with no extra setup from the caller. We
// inject a fakeClock via the unexported field so Run's scheduler walks
// synthetic time instead of real time.
func TestBotRunStartsSchedulerWhenScheduleRouteRegistered(t *testing.T) {
	bot, _ := schedulerTestBot(t)
	t0 := time.Date(2026, 4, 27, 12, 0, 0, 0, time.UTC)
	clock := newFakeClock(t0)
	bot.clock = clock

	dir := t.TempDir()
	bot.dataDir = DataDir(dir)

	tr := &ScheduleTrigger{
		Schedule: mustParseCron(t, "5 12 * * *"),
		CronExpr: "5 12 * * *",
		Input:    "ping",
	}
	h := &recordingHandler{}
	roomID := id.RoomID("!sched:e")
	bot.RouteIn(roomID, tr, h)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		bot.startScheduler(ctx)
		close(done)
	}()

	waitForTimerArmed(clock, 1)
	clock.Advance(5 * time.Minute)
	waitForCalls(t, h, 1)

	cancel()
	<-done
}

// TestSchedulerExitsOnContextCancel: cancelling ctx returns the loop
// promptly so the bot's Run can shut down cleanly.
func TestSchedulerExitsOnContextCancel(t *testing.T) {
	bot, _ := schedulerTestBot(t)
	t0 := time.Date(2026, 4, 27, 12, 0, 0, 0, time.UTC)
	clock := newFakeClock(t0)

	tr := &ScheduleTrigger{
		Schedule: mustParseCron(t, "0 9 * * *"),
		CronExpr: "0 9 * * *",
		Input:    "x",
	}
	bot.RouteIn(id.RoomID("!r:e"), tr, HandlerFunc(func(_ context.Context, _ Request) (Response, error) {
		return Response{}, nil
	}))

	dir := t.TempDir()
	path := filepath.Join(dir, "schedule.json")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		runScheduler(ctx, bot, clock, path)
		close(done)
	}()

	waitForTimerArmed(clock, 1)
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("scheduler did not exit within 2s of cancel")
	}
}

// schedulerTestMessageEvent builds a synthetic message event for the
// dispatch-path tests. Kept short because none of the message details
// matter — the test only cares that ScheduleTrigger.Apply rejects it.
func schedulerTestMessageEvent(roomID, sender string) *event.Event {
	return &event.Event{
		ID:      id.EventID("$e"),
		RoomID:  id.RoomID(roomID),
		Sender:  id.UserID(sender),
		Content: event.Content{Parsed: &event.MessageEventContent{Body: "hello"}},
	}
}

// waitForTimerArmed polls the fake clock until at least n pending timers
// exist, so a test driver can be sure the scheduler goroutine has reached
// its sleep before Advance() is called. Without this, Advance can race
// the goroutine's startup and fire on an empty timer set.
func waitForTimerArmed(c *fakeClock, n int) {
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		count := 0
		for _, t := range c.timers {
			if !t.stopped {
				count++
			}
		}
		c.mu.Unlock()
		if count >= n {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
}
