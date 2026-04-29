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
// invariant: ScheduleTrigger.Apply returns no-match on real Matrix events
// (evt != nil). The dispatch path itself does not skip Tickable triggers
// — it walks every route in the room and calls Apply on each — so the
// trigger is the layer keeping incoming messages from accidentally
// driving a clock-scheduled route.
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
		t.Error("schedule trigger fired on a real Matrix event; ScheduleTrigger.Apply must return no-match for non-nil events")
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

// TestSchedulerPersistedTimeEqualsNowIsFuture pins the boundary between
// "persisted time is in the past, catch up immediately" and "persisted
// time is in the future, wait for it." Persisted next-fire times are
// always strictly later than the moment we wrote them, so on restart a
// value exactly equal to the scheduler's `now` is the future end of the
// window, not the past end — it must fire exactly once at that instant
// and then advance to the cron's next natural fire, not double-fire (one
// stale-catch-up, one at the instant) or skip ahead.
func TestSchedulerPersistedTimeEqualsNowIsFuture(t *testing.T) {
	bot, _ := schedulerTestBot(t)
	t0 := time.Date(2026, 4, 28, 9, 0, 0, 0, time.UTC)
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
	wantKey := scheduleID(roomID, "0 9 * * *", "morning")
	// Persisted time exactly equal to t0 — the boundary case.
	if err := saveScheduleStore(path, map[string]time.Time{wantKey: t0}); err != nil {
		t.Fatalf("seed schedule.json: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		runScheduler(ctx, bot, clock, path)
		close(done)
	}()

	// One fire at the persisted instant, no double-fire from a misread
	// boundary.
	waitForCalls(t, h, 1)
	// The next-fire persisted after that single fire must be the cron's
	// next natural instant strictly after t0 — i.e. tomorrow 09:00. If the
	// scheduler had treated the persisted-equal-now as past and "caught
	// up", e.next would still advance to the same place, but the trip
	// through the catch-up branch is the bug we want to keep out of the
	// codebase.
	want := time.Date(2026, 4, 29, 9, 0, 0, 0, time.UTC)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		got, err := loadScheduleStore(path)
		if err == nil && got[wantKey].Equal(want) {
			// Give the loop a moment to spuriously double-fire if
			// e.next was set wrong; if so we'd see a second handler
			// call land here.
			time.Sleep(20 * time.Millisecond)
			if calls := len(h.calls()); calls != 1 {
				t.Fatalf("handler fired %d times, want 1 (persisted-equal-now must produce one fire only)", calls)
			}
			cancel()
			<-done
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	got, _ := loadScheduleStore(path)
	t.Fatalf("schedule.json[%q] = %v, want %v", wantKey, got[wantKey], want)
}

// TestSchedulerFreshKeyWaitsForCronAndPersistsAfterFire pins the
// first-registration contract when schedule.json already exists with
// other entries: a freshly registered route whose key isn't in the file
// MUST wait for the cron's first natural fire (no premature fire on
// startup) AND must NOT appear in schedule.json until after that first
// fire has actually happened. Persisting the next-fire on startup would
// be the wrong moment — it would record an instant the cron didn't ask
// for, which restart logic would then honour as a stale catch-up.
func TestSchedulerFreshKeyWaitsForCronAndPersistsAfterFire(t *testing.T) {
	bot, _ := schedulerTestBot(t)
	t0 := time.Date(2026, 4, 27, 12, 0, 0, 0, time.UTC)
	clock := newFakeClock(t0)

	tr := &ScheduleTrigger{
		Schedule: mustParseCron(t, "5 12 * * *"), // first fire 12:05
		CronExpr: "5 12 * * *",
		Input:    "ping",
	}
	h := &recordingHandler{}
	roomID := id.RoomID("!r:e")
	bot.RouteIn(roomID, tr, h)

	dir := t.TempDir()
	path := filepath.Join(dir, "schedule.json")
	wantKey := scheduleID(roomID, "5 12 * * *", "ping")
	otherKey := "unrelated-key"
	other := time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := saveScheduleStore(path, map[string]time.Time{otherKey: other}); err != nil {
		t.Fatalf("seed schedule.json: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		runScheduler(ctx, bot, clock, path)
		close(done)
	}()

	// Once the loop has armed its sleep timer, the startup phase is
	// complete: any persistence the scheduler wanted to do at startup
	// would already be on disk by now. Reading the file at this point
	// must NOT show our new key — only the seeded unrelated entry.
	waitForTimerArmed(clock, 1)
	got, err := loadScheduleStore(path)
	if err != nil {
		t.Fatalf("loadScheduleStore: %v", err)
	}
	if _, premature := got[wantKey]; premature {
		t.Fatalf("schedule.json contains %q before first fire: %v", wantKey, got)
	}
	if !got[otherKey].Equal(other) {
		t.Errorf("seeded key %q not preserved: got %v, want %v", otherKey, got[otherKey], other)
	}

	// Advance to the cron's first instant and assert the fire happens.
	clock.Advance(5 * time.Minute)
	waitForCalls(t, h, 1)

	// After the first fire the entry must be persisted, advanced to the
	// cron's next instant strictly after the fire we just observed.
	wantNext := time.Date(2026, 4, 28, 12, 5, 0, 0, time.UTC)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		got, err := loadScheduleStore(path)
		if err == nil && got[wantKey].Equal(wantNext) {
			cancel()
			<-done
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	got, _ = loadScheduleStore(path)
	t.Fatalf("after first fire, schedule.json[%q] = %v, want %v", wantKey, got[wantKey], wantNext)
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

// TestStartSchedulerFiresOnTickAfterRegistration pins the wire-up in
// startScheduler: registering a tickable route and calling startScheduler
// directly is enough to drive the schedule on synthetic time, with no
// extra setup from the caller. The fakeClock is injected via the
// unexported field. Run's wiring is tested separately.
func TestStartSchedulerFiresOnTickAfterRegistration(t *testing.T) {
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
