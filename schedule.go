package matrixbot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"github.com/robfig/cron/v3"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

// Tickable is the optional interface a Trigger implements when its match
// criterion is "the clock advanced past a configured time" rather than
// "this Matrix event arrived". The scheduler goroutine type-asserts on it
// at startup to discover which routes need clock-driven dispatch.
//
// NextFire returns the first scheduled fire time strictly after `after`.
// It must be deterministic with respect to its input — the scheduler's
// catch-up logic and persistence both rely on calling it with a known
// instant and getting the same answer back.
type Tickable interface {
	NextFire(after time.Time) time.Time
}

// ScheduleTrigger fires on a cron expression. Apply only matches when the
// scheduler injects a synthetic tick (evt == nil); a real Matrix event
// reaching it through the regular dispatch loop is a non-match. That keeps
// ordinary message/reaction traffic from accidentally driving the schedule.
//
// CronExpr is the original textual cron expression. The scheduler hashes
// it (with the room ID and Input) into a stable persistence key, so the
// expression must round-trip from config to schedule.json by string —
// rebuilt cron.Schedule values aren't guaranteed to fmt back to the same
// thing.
type ScheduleTrigger struct {
	Schedule cron.Schedule
	CronExpr string
	Input    string
}

// Apply implements Trigger. See type doc.
func (s *ScheduleTrigger) Apply(_ context.Context, evt *event.Event, _ EventFetcher) (Request, bool, error) {
	if evt != nil {
		return Request{}, false, nil
	}
	return Request{Input: s.Input}, true, nil
}

// NextFire implements Tickable.
func (s *ScheduleTrigger) NextFire(after time.Time) time.Time {
	return s.Schedule.Next(after)
}

// scheduleID returns a stable hex digest of (roomID, cron, input) for
// use as a schedule.json key.
func scheduleID(roomID id.RoomID, cronExpr, input string) string {
	h := sha256.New()
	h.Write([]byte(roomID))
	h.Write([]byte{0})
	h.Write([]byte(cronExpr))
	h.Write([]byte{0})
	h.Write([]byte(input))
	return hex.EncodeToString(h.Sum(nil))
}

// loadScheduleStore reads schedule.json. A missing file returns an empty
// map (a fresh data dir has no persisted schedules yet); other errors —
// permission denied, malformed JSON — surface so the operator notices.
func loadScheduleStore(path string) (map[string]time.Time, error) {
	out := map[string]time.Time{}
	if err := readJSON(path, &out); err != nil {
		// readJSON wraps os.ErrNotExist with ErrNotInitialized; for the
		// scheduler that's just "no schedules persisted yet", not an error.
		if errors.Is(err, ErrNotInitialized) {
			return map[string]time.Time{}, nil
		}
		return nil, err
	}
	return out, nil
}

// saveScheduleStore atomically replaces schedule.json with the given map.
// Uses the same write-temp-then-rename pattern as the rest of the data dir
// so a crash mid-write leaves the prior file intact.
func saveScheduleStore(path string, m map[string]time.Time) error {
	return writeJSON(path, m)
}
