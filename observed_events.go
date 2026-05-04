package matrixbot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

// ObservedEventKind identifies the Matrix-visible event shape exposed to host
// programs that need to mirror room history.
type ObservedEventKind string

const (
	ObservedEventMessage     ObservedEventKind = "message"
	ObservedEventReplacement ObservedEventKind = "replacement"
	ObservedEventRedaction   ObservedEventKind = "redaction"
	ObservedEventReaction    ObservedEventKind = "reaction"
)

// ObservedEvent is the Matrix-generic form of a bot-visible room event.
type ObservedEvent struct {
	Kind      ObservedEventKind
	RoomID    id.RoomID
	EventID   id.EventID
	Sender    id.UserID
	Timestamp time.Time

	Body            string
	ReplacesEventID id.EventID
	RedactsEventID  id.EventID

	ReactionToEventID id.EventID
	ReactionKey       string
}

// ObservedEventHandler receives bot-visible Matrix events for host-side
// mirroring. Handler errors are logged and do not stop route dispatch.
type ObservedEventHandler interface {
	HandleObservedEvent(context.Context, ObservedEvent) error
}

type ObservedEventHandlerFunc func(context.Context, ObservedEvent) error

func (f ObservedEventHandlerFunc) HandleObservedEvent(ctx context.Context, evt ObservedEvent) error {
	return f(ctx, evt)
}

// ObservedEventPage is one bounded Matrix history page converted into
// ObservedEvent values, plus the homeserver pagination tokens.
type ObservedEventPage struct {
	Start  string
	End    string
	Events []ObservedEvent
}

// ObservedEventFromMatrixEvent converts an already-visible Matrix event into
// the public observation shape. Non-message/reaction/redaction events return
// ok=false.
func ObservedEventFromMatrixEvent(evt *event.Event) (ObservedEvent, bool) {
	if evt == nil {
		return ObservedEvent{}, false
	}
	observed := ObservedEvent{
		RoomID:    evt.RoomID,
		EventID:   evt.ID,
		Sender:    evt.Sender,
		Timestamp: time.UnixMilli(evt.Timestamp),
	}
	if evt.Unsigned.RedactedBecause != nil {
		observed.Kind = ObservedEventRedaction
		observed.RedactsEventID = evt.ID
		return observed, true
	}
	switch evt.Type {
	case event.EventMessage:
		mec, ok := messageContent(evt)
		if !ok {
			return ObservedEvent{}, false
		}
		if target := mec.RelatesTo.GetReplaceID(); target != "" {
			if mec.NewContent == nil || mec.NewContent.Body == "" {
				return ObservedEvent{}, false
			}
			observed.Kind = ObservedEventReplacement
			observed.Body = mec.NewContent.Body
			observed.ReplacesEventID = target
			return observed, true
		}
		if mec.Body == "" {
			return ObservedEvent{}, false
		}
		observed.Kind = ObservedEventMessage
		observed.Body = mec.Body
		return observed, true
	case event.EventReaction:
		rec, ok := reactionContent(evt)
		if !ok {
			return ObservedEvent{}, false
		}
		target := rec.RelatesTo.GetAnnotationID()
		key := rec.RelatesTo.GetAnnotationKey()
		if target == "" || key == "" {
			return ObservedEvent{}, false
		}
		observed.Kind = ObservedEventReaction
		observed.ReactionToEventID = target
		observed.ReactionKey = key
		return observed, true
	case event.EventRedaction:
		target := redactedEventID(evt)
		if target == "" {
			return ObservedEvent{}, false
		}
		observed.Kind = ObservedEventRedaction
		observed.RedactsEventID = target
		return observed, true
	default:
		return ObservedEvent{}, false
	}
}

func (b *Bot) observeEvent(ctx context.Context, evt *event.Event) {
	if b.observedEventHandler == nil {
		return
	}
	observed, ok := ObservedEventFromMatrixEvent(evt)
	if !ok {
		return
	}
	if err := b.observedEventHandler.HandleObservedEvent(ctx, observed); err != nil {
		slog.Warn("matrixbot: observer error", "event", evt.ID, "err", err)
	}
}

// ObservedEventsPage fetches one caller-bounded room history page and returns
// decrypted, bot-visible events in Matrix-generic form.
func (b *Bot) ObservedEventsPage(ctx context.Context, roomID id.RoomID, from, to string, dir mautrix.Direction, limit int) (ObservedEventPage, error) {
	if limit <= 0 {
		return ObservedEventPage{}, nil
	}
	if b.history == nil {
		return ObservedEventPage{}, fmt.Errorf("matrixbot: history source not configured")
	}
	resp, err := b.history.Messages(ctx, roomID, from, to, dir, nil, limit)
	if err != nil {
		return ObservedEventPage{}, fmt.Errorf("matrixbot: fetching observed events: %w", err)
	}
	if resp == nil {
		return ObservedEventPage{}, nil
	}
	page := ObservedEventPage{Start: resp.Start, End: resp.End}
	for _, evt := range resp.Chunk {
		if evt == nil {
			continue
		}
		decrypted, err := b.decrypter.decrypt(ctx, evt)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return ObservedEventPage{}, fmt.Errorf("matrixbot: decrypting observed event %s: %w", evt.ID, err)
			}
			continue
		}
		observed, ok := ObservedEventFromMatrixEvent(decrypted)
		if !ok {
			continue
		}
		page.Events = append(page.Events, observed)
	}
	return page, nil
}

func messageContent(evt *event.Event) (*event.MessageEventContent, bool) {
	if mec, ok := evt.Content.Parsed.(*event.MessageEventContent); ok && mec != nil {
		return mec, true
	}
	if err := evt.Content.ParseRaw(event.EventMessage); err != nil {
		return nil, false
	}
	mec, ok := evt.Content.Parsed.(*event.MessageEventContent)
	return mec, ok && mec != nil
}

func reactionContent(evt *event.Event) (*event.ReactionEventContent, bool) {
	if rec, ok := evt.Content.Parsed.(*event.ReactionEventContent); ok && rec != nil {
		return rec, true
	}
	if err := evt.Content.ParseRaw(event.EventReaction); err != nil {
		return nil, false
	}
	rec, ok := evt.Content.Parsed.(*event.ReactionEventContent)
	return rec, ok && rec != nil
}

func redactedEventID(evt *event.Event) id.EventID {
	if evt.Redacts != "" {
		return evt.Redacts
	}
	if rec, ok := evt.Content.Parsed.(*event.RedactionEventContent); ok && rec != nil {
		return rec.Redacts
	}
	if err := evt.Content.ParseRaw(event.EventRedaction); err != nil {
		return ""
	}
	if rec, ok := evt.Content.Parsed.(*event.RedactionEventContent); ok && rec != nil {
		return rec.Redacts
	}
	return ""
}
