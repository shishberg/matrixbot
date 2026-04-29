package matrixbot

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/robfig/cron/v3"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

// mustParseCron is a test helper: a malformed expression in a literal test is
// a programmer error, not a runtime concern, so propagate via t.Fatal rather
// than threading errors through every test setup.
func mustParseCron(t *testing.T, expr string) cron.Schedule {
	t.Helper()
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	sched, err := parser.Parse(expr)
	if err != nil {
		t.Fatalf("parse cron %q: %v", expr, err)
	}
	return sched
}

// A real Matrix message event reaching ScheduleTrigger.Apply must never match.
// The trigger is only meant to fire on synthetic clock ticks (evt == nil) the
// scheduler injects; defending against the dispatch path's call keeps the
// matrix dispatcher and the scheduler from double-firing on the same event.
func TestScheduleTriggerIgnoresMatrixEvents(t *testing.T) {
	tr := &ScheduleTrigger{
		Schedule: mustParseCron(t, "0 9 * * *"),
		Input:    "good morning",
	}
	evt := &event.Event{
		ID:     id.EventID("$e"),
		RoomID: id.RoomID("!r:e"),
		Sender: id.UserID("@u:e"),
		Content: event.Content{Parsed: &event.MessageEventContent{
			Body: "hello",
		}},
	}
	_, ok, err := tr.Apply(context.Background(), evt, nil)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if ok {
		t.Error("ScheduleTrigger.Apply matched a real Matrix event; it must only fire on nil-event synthetic ticks")
	}
}

// On the synthetic tick path (evt == nil) the trigger returns its configured
// Input. Sender and RoomID stay zero — the scheduler fills them from the
// route's registration before invoking the handler.
func TestScheduleTriggerFiresOnTick(t *testing.T) {
	tr := &ScheduleTrigger{
		Schedule: mustParseCron(t, "0 9 * * *"),
		Input:    "good morning",
	}
	req, ok, err := tr.Apply(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !ok {
		t.Fatal("ScheduleTrigger.Apply did not match on synthetic tick")
	}
	if req.Input != "good morning" {
		t.Errorf("Input = %q, want %q", req.Input, "good morning")
	}
	if req.RoomID != "" {
		t.Errorf("RoomID = %q, want empty (scheduler fills it)", req.RoomID)
	}
	if req.Sender != "" {
		t.Errorf("Sender = %q, want empty (scheduler fills it)", req.Sender)
	}
}

// scheduleID is stable across runs: same room+cron+input always hashes to the
// same key. This is the contract the persistence layer relies on, since
// schedule.json is keyed by these IDs.
func TestScheduleIDStableAcrossCalls(t *testing.T) {
	a := scheduleID(id.RoomID("!r:e"), "0 9 * * *", "morning")
	b := scheduleID(id.RoomID("!r:e"), "0 9 * * *", "morning")
	if a != b {
		t.Errorf("scheduleID not stable: %q vs %q", a, b)
	}
}

func TestScheduleIDDifferentiatesInputs(t *testing.T) {
	a := scheduleID(id.RoomID("!r:e"), "0 9 * * *", "morning")
	b := scheduleID(id.RoomID("!r:e"), "0 9 * * *", "evening")
	if a == b {
		t.Error("scheduleID collided across distinct inputs")
	}
}

func TestScheduleIDDifferentiatesRooms(t *testing.T) {
	a := scheduleID(id.RoomID("!r1:e"), "0 9 * * *", "morning")
	b := scheduleID(id.RoomID("!r2:e"), "0 9 * * *", "morning")
	if a == b {
		t.Error("scheduleID collided across distinct rooms")
	}
}

// loadScheduleStore on a missing file is a clean empty map, not an error
// — the first run has no schedule.json yet, and aborting at startup would
// be a worse default than starting fresh.
func TestLoadScheduleStoreMissingReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "schedule.json")
	got, err := loadScheduleStore(path)
	if err != nil {
		t.Fatalf("loadScheduleStore: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %v, want empty map", got)
	}
}

func TestSaveAndLoadScheduleStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "schedule.json")
	want := map[string]time.Time{
		"a": time.Date(2026, 4, 28, 9, 0, 0, 0, time.UTC),
		"b": time.Date(2026, 4, 29, 9, 0, 0, 0, time.UTC),
	}
	if err := saveScheduleStore(path, want); err != nil {
		t.Fatalf("saveScheduleStore: %v", err)
	}
	got, err := loadScheduleStore(path)
	if err != nil {
		t.Fatalf("loadScheduleStore: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d", len(got), len(want))
	}
	for k, v := range want {
		if !got[k].Equal(v) {
			t.Errorf("got[%q] = %v, want %v", k, got[k], v)
		}
	}
}

func TestSaveScheduleStoreMode0600(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "schedule.json")
	if err := saveScheduleStore(path, map[string]time.Time{"k": time.Now().UTC()}); err != nil {
		t.Fatalf("saveScheduleStore: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("mode = %o, want 0600", got)
	}
}

// NextFire delegates to the underlying cron.Schedule. "0 9 * * *" from a
// noon-Monday baseline must land on 09:00 the following day (Tuesday).
func TestScheduleTriggerNextFire(t *testing.T) {
	tr := &ScheduleTrigger{
		Schedule: mustParseCron(t, "0 9 * * *"),
		Input:    "good morning",
	}
	// 2026-04-27 12:00:00 UTC is a Monday afternoon.
	base := time.Date(2026, 4, 27, 12, 0, 0, 0, time.UTC)
	got := tr.NextFire(base)
	want := time.Date(2026, 4, 28, 9, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("NextFire = %v, want %v", got, want)
	}
}
