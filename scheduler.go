package matrixbot

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"maunium.net/go/mautrix/id"
)

// scheduledEntry is one tickable route the scheduler is tracking. Apart
// from the route itself it remembers the room (the route table is keyed
// by room, but we flatten it here so the run loop can sort by next-fire
// without re-walking the map) and a stable id for persistence.
type scheduledEntry struct {
	id       string
	roomID   id.RoomID
	tickable Tickable
	route    Route
	next     time.Time
}

// runScheduler walks the bot's route table for tickable triggers and, in
// chronological order, fires each one as the clock crosses its next-fire
// time. It blocks until ctx is cancelled; returns immediately if no
// tickable routes are registered.
//
// Restart behaviour: a next-fire time persisted to schedulePath in a
// previous run is honoured if it's still in the future. A persisted time
// in the past — i.e. the bot was offline when it should have fired —
// fires once at startup so the user can't tell the bot was bounced,
// then advances to the cron's next natural instant. A route that has
// never been registered before (no entry in schedule.json) waits for
// its cron's first instant rather than firing immediately on startup —
// the cron expression should drive every fire, not the act of starting.
//
// Persistence is best-effort: a write error is logged but the loop keeps
// running. Losing the next-fire times only costs at most one extra
// catch-up tick per schedule on the next restart, which is by design.
func runScheduler(ctx context.Context, b *Bot, clock Clock, schedulePath string) {
	entries := collectScheduledEntries(b)
	if len(entries) == 0 {
		return
	}

	persisted := map[string]time.Time{}
	if schedulePath != "" {
		loaded, err := loadScheduleStore(schedulePath)
		if err != nil {
			slog.Warn("matrixbot: loading schedule store", "err", err, "path", schedulePath)
		} else {
			persisted = loaded
		}
	}

	now := clock.Now()
	for i := range entries {
		e := &entries[i]
		t, persistedHere := persisted[e.id]
		switch {
		case persistedHere && !t.Before(now):
			// We were offline less than one cycle; honour the persisted
			// instant so a quick bounce produces no visible change. A
			// persisted time equal to now is the future end of the window
			// — we always write strictly future-of-fire-time — so wait
			// for it, don't catch-up fire.
			e.next = t
		case persistedHere:
			// Persisted time is in the past — the bot was offline when it
			// should have fired. Catch up immediately, then advance to
			// the cron's next instant strictly after now.
			e.next = now
		default:
			// First time this schedule has ever been registered. Wait
			// for the cron's first natural firing — restarting the bot
			// must not invent a fire that the cron didn't ask for.
			e.next = e.tickable.NextFire(now)
		}
	}

	for {
		// Earliest pending fire wins. Re-find it every iteration: an
		// entry just fired and re-scheduled itself further out.
		nextIdx := earliestEntry(entries)
		wait := entries[nextIdx].next.Sub(clock.Now())
		if wait < 0 {
			wait = 0
		}
		timer := clock.NewTimer(wait)

		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C():
		}

		e := &entries[nextIdx]
		fireScheduledEntry(ctx, b, e)

		// Advance the entry's next-fire to the cron's first instant
		// strictly after the time it just fired, then persist. An empty
		// schedulePath skips persistence — the in-memory state still
		// drives the loop, but nothing survives a restart.
		e.next = e.tickable.NextFire(clock.Now())
		if schedulePath != "" {
			if err := saveScheduleStore(schedulePath, snapshotNextFires(entries)); err != nil {
				slog.Warn("matrixbot: persisting schedule store", "err", err, "path", schedulePath)
			}
		}
	}
}

// collectScheduledEntries walks routesByRoom for triggers that satisfy
// the Tickable interface. Two routes registered with identical
// (roomID, cron, input) configuration share a schedule ID; this function
// keeps only the first such registration and discards later duplicates,
// so the schedule fires once per tick rather than once per duplicate
// registration.
func collectScheduledEntries(b *Bot) []scheduledEntry {
	var entries []scheduledEntry
	seen := map[string]bool{}
	for roomID, routes := range b.routesByRoom {
		for _, r := range routes {
			tickable, ok := r.Trigger.(Tickable)
			if !ok {
				continue
			}
			eid := scheduleEntryID(roomID, r.Trigger)
			if seen[eid] {
				continue
			}
			seen[eid] = true
			entries = append(entries, scheduledEntry{
				id:       eid,
				roomID:   roomID,
				tickable: tickable,
				route:    r,
			})
		}
	}
	return entries
}

// scheduleEntryID computes the persistence key for a route. ScheduleTrigger
// gets a (room, cron, input) hash so two identically-configured routes
// share an id; any other Tickable falls back to a type-name + room key,
// which keeps things working without committing matrixbot to introspecting
// every implementor.
func scheduleEntryID(roomID id.RoomID, t Trigger) string {
	if st, ok := t.(*ScheduleTrigger); ok && st != nil {
		return scheduleID(roomID, st.CronExpr, st.Input)
	}
	// The %T fallback is stable within a single binary build but changes
	// if the trigger type is renamed or moved between packages. The
	// worst case is one spurious catch-up fire on the next restart (the
	// old key is treated as gone, the new key as fresh) — not data
	// corruption.
	return scheduleID(roomID, fmt.Sprintf("%T", t), "")
}

func earliestEntry(entries []scheduledEntry) int {
	best := 0
	for i := 1; i < len(entries); i++ {
		if entries[i].next.Before(entries[best].next) {
			best = i
		}
	}
	return best
}

func snapshotNextFires(entries []scheduledEntry) map[string]time.Time {
	out := make(map[string]time.Time, len(entries))
	for _, e := range entries {
		out[e.id] = e.next
	}
	return out
}

// fireScheduledEntry invokes the route's trigger and handler the same way
// dispatch does for a real event, but with a synthetic nil event. The
// trigger fills in Input; we layer on the registered RoomID and Sender
// (the bot itself, since no Matrix user is "sending" the tick).
func fireScheduledEntry(ctx context.Context, b *Bot, e *scheduledEntry) {
	req, matched, err := e.route.Trigger.Apply(ctx, nil, b.fetcher)
	if err != nil {
		slog.Warn("matrixbot: schedule trigger error", "err", err, "id", e.id)
		return
	}
	if !matched {
		// A Tickable that returns no-match on a synthetic tick is the
		// caller's bug, not ours; log loud enough to be findable.
		slog.Warn("matrixbot: schedule trigger did not match on tick", "id", e.id)
		return
	}
	req.RoomID = e.roomID
	req.Sender = b.botUserID
	slog.Info("matrixbot: schedule fired", "id", e.id, "room", e.roomID)
	resp, err := e.route.Handler.Handle(ctx, req)
	if err != nil {
		slog.Warn("matrixbot: schedule handler error", "err", err, "id", e.id)
		b.send(ctx, e.roomID, "Sorry, I hit an error: "+err.Error())
		return
	}
	if resp.Reply != "" {
		b.send(ctx, e.roomID, resp.Reply)
	}
}
