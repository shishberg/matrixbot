package matrixbot

import (
	"context"
	"errors"
	"fmt"
	"time"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

// maxHistoryPages caps the scrollback loop. A homeserver returning empty
// pages with fresh tokens forever (buggy or hostile) must not burn the
// dispatch goroutine. The value is a small multiple of the largest limit
// callers reasonably want, so well-behaved servers always converge first.
const maxHistoryPages = 8

// Message is a decrypted m.room.message event in the form callers actually
// use: who sent it, what it said, when, and the event ID for cross-reference.
// PreviousMessages returns these strictly in oldest-first order.
type Message struct {
	EventID   id.EventID
	Sender    id.UserID
	Body      string
	Timestamp time.Time
}

// historySource is the slice of *mautrix.Client that PreviousMessages needs.
// Splitting it out keeps the bot unit-testable without a homeserver.
type historySource interface {
	Context(ctx context.Context, roomID id.RoomID, eventID id.EventID, filter *mautrix.FilterPart, limit int) (*mautrix.RespContext, error)
	Messages(ctx context.Context, roomID id.RoomID, from, to string, dir mautrix.Direction, filter *mautrix.FilterPart, limit int) (*mautrix.RespMessages, error)
}

// eventDecrypter turns an encrypted event into its plaintext form, using
// whatever crypto state the host has set up. Plain events pass through. Both
// decryptingFetcher (HTTP path) and any future test double satisfy this.
type eventDecrypter interface {
	decrypt(ctx context.Context, evt *event.Event) (*event.Event, error)
}

// PreviousMessages returns up to `limit` m.room.message events from `roomID`
// that occurred strictly before `before`, oldest first. The event identified
// by `before` is never included in the result — when a handler is processing
// a mention, the mention itself is the current input, not prior context.
//
// Encrypted events are decrypted using the same path as parent-event
// lookups. Individual encrypted events that cannot be decrypted are skipped;
// successfully decrypted messages are returned with plaintext bodies.
// Non-message events (joins, reactions, redactions, state) are filtered out
// after pagination so that an active room with lots of state noise still
// returns up to `limit` actual messages.
//
// If `limit` is 0 or negative the call is a no-op: returns nil with no error
// and makes no API requests. The underlying scrollback loop is bounded by
// maxHistoryPages so a malformed homeserver response can't burn forever; if
// the timeline is shorter than `limit`, the caller gets whatever exists.
func (b *Bot) PreviousMessages(ctx context.Context, roomID id.RoomID, before id.EventID, limit int) ([]Message, error) {
	if limit <= 0 {
		return nil, nil
	}
	if b.history == nil {
		return nil, fmt.Errorf("matrixbot: history source not configured")
	}

	resp, err := b.history.Context(ctx, roomID, before, nil, limit)
	if err != nil {
		return nil, fmt.Errorf("matrixbot: fetching context for %s: %w", before, err)
	}
	if resp == nil {
		return nil, nil
	}

	// EventsBefore is newest-first; we accumulate everything in that order
	// and reverse at the end, so the pagination loop only ever appends.
	newestFirst := make([]Message, 0, limit)
	consume := func(events []*event.Event) error {
		for _, evt := range events {
			if evt == nil {
				continue
			}
			// Strict-before: drop the target event if the homeserver
			// included it (some implementations do; the spec is loose).
			if evt.ID == before {
				continue
			}
			decrypted, err := b.decrypter.decrypt(ctx, evt)
			if err != nil {
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					return fmt.Errorf("matrixbot: decrypting previous message %s: %w", evt.ID, err)
				}
				continue
			}
			if decrypted == nil || decrypted.Type != event.EventMessage {
				continue
			}
			body := parentEventBody(decrypted)
			if body == "" {
				continue
			}
			newestFirst = append(newestFirst, Message{
				EventID:   decrypted.ID,
				Sender:    decrypted.Sender,
				Body:      body,
				Timestamp: time.UnixMilli(decrypted.Timestamp),
			})
			if len(newestFirst) >= limit {
				return nil
			}
		}
		return nil
	}

	if err := consume(resp.EventsBefore); err != nil {
		return nil, err
	}

	from := resp.Start
	for pages := 0; len(newestFirst) < limit && from != "" && pages < maxHistoryPages; pages++ {
		page, err := b.history.Messages(ctx, roomID, from, "", mautrix.DirectionBackward, nil, limit)
		if err != nil {
			return nil, fmt.Errorf("matrixbot: paginating before %s: %w", before, err)
		}
		if page == nil {
			break
		}
		if err := consume(page.Chunk); err != nil {
			return nil, err
		}
		// End == "" or End == from both signal the timeline is exhausted;
		// either way, stop. Anything else, advance.
		if page.End == "" || page.End == from {
			break
		}
		from = page.End
	}

	// Reverse to oldest-first. Most callers format chronologically; matching
	// natural reading order means they don't have to reverse at the call
	// site.
	for i, j := 0, len(newestFirst)-1; i < j; i, j = i+1, j-1 {
		newestFirst[i], newestFirst[j] = newestFirst[j], newestFirst[i]
	}
	return newestFirst, nil
}
