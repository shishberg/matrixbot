package matrixbot

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

func editEvent(idStr, sender, body, target string, tsMillis int64) *event.Event {
	return &event.Event{
		Type:      event.EventMessage,
		ID:        id.EventID(idStr),
		RoomID:    id.RoomID("!r:e"),
		Sender:    id.UserID(sender),
		Timestamp: tsMillis,
		Content: event.Content{Parsed: &event.MessageEventContent{
			MsgType: event.MsgText,
			Body:    "* " + body,
			NewContent: &event.MessageEventContent{
				MsgType: event.MsgText,
				Body:    body,
			},
			RelatesTo: (&event.RelatesTo{}).SetReplace(id.EventID(target)),
		}},
	}
}

func observedReactionEvent(idStr, sender, target, key string, tsMillis int64) *event.Event {
	return &event.Event{
		Type:      event.EventReaction,
		ID:        id.EventID(idStr),
		RoomID:    id.RoomID("!r:e"),
		Sender:    id.UserID(sender),
		Timestamp: tsMillis,
		Content: event.Content{Parsed: &event.ReactionEventContent{
			RelatesTo: *(&event.RelatesTo{}).SetAnnotation(id.EventID(target), key),
		}},
	}
}

func redactionEvent(idStr, sender, target string, tsMillis int64) *event.Event {
	return &event.Event{
		Type:      event.EventRedaction,
		ID:        id.EventID(idStr),
		RoomID:    id.RoomID("!r:e"),
		Sender:    id.UserID(sender),
		Timestamp: tsMillis,
		Redacts:   id.EventID(target),
	}
}

func redactedMessageEvent(idStr, sender string, tsMillis int64) *event.Event {
	return &event.Event{
		Type:      event.EventMessage,
		ID:        id.EventID(idStr),
		RoomID:    id.RoomID("!r:e"),
		Sender:    id.UserID(sender),
		Timestamp: tsMillis,
		Unsigned: event.Unsigned{
			RedactedBecause: redactionEvent("$redact-"+idStr, "@mod:e", idStr, tsMillis+1),
		},
	}
}

func observedMessageEvent(idStr, sender, body string, tsMillis int64) *event.Event {
	evt := msgEvent(idStr, sender, body, tsMillis)
	evt.RoomID = id.RoomID("!r:e")
	return evt
}

func TestObservedEventFromMatrixEventConvertsStructuredEvents(t *testing.T) {
	tests := []struct {
		name string
		evt  *event.Event
		want ObservedEvent
	}{
		{
			name: "message",
			evt:  observedMessageEvent("$m1", "@u:e", "hello", 1000),
			want: ObservedEvent{
				Kind:      ObservedEventMessage,
				EventID:   id.EventID("$m1"),
				Sender:    id.UserID("@u:e"),
				Timestamp: time.UnixMilli(1000),
				Body:      "hello",
			},
		},
		{
			name: "edit",
			evt:  editEvent("$edit", "@u:e", "updated", "$m1", 2000),
			want: ObservedEvent{
				Kind:            ObservedEventReplacement,
				EventID:         id.EventID("$edit"),
				RoomID:          id.RoomID("!r:e"),
				Sender:          id.UserID("@u:e"),
				Timestamp:       time.UnixMilli(2000),
				Body:            "updated",
				ReplacesEventID: id.EventID("$m1"),
			},
		},
		{
			name: "reaction",
			evt:  observedReactionEvent("$react", "@u:e", "$m1", "👍", 3000),
			want: ObservedEvent{
				Kind:              ObservedEventReaction,
				EventID:           id.EventID("$react"),
				RoomID:            id.RoomID("!r:e"),
				Sender:            id.UserID("@u:e"),
				Timestamp:         time.UnixMilli(3000),
				ReactionToEventID: id.EventID("$m1"),
				ReactionKey:       "👍",
			},
		},
		{
			name: "redaction",
			evt:  redactionEvent("$redact", "@mod:e", "$m1", 4000),
			want: ObservedEvent{
				Kind:           ObservedEventRedaction,
				EventID:        id.EventID("$redact"),
				RoomID:         id.RoomID("!r:e"),
				Sender:         id.UserID("@mod:e"),
				Timestamp:      time.UnixMilli(4000),
				RedactsEventID: id.EventID("$m1"),
			},
		},
		{
			name: "redacted original event",
			evt:  redactedMessageEvent("$redacted", "@u:e", 5000),
			want: ObservedEvent{
				Kind:           ObservedEventRedaction,
				EventID:        id.EventID("$redacted"),
				RoomID:         id.RoomID("!r:e"),
				Sender:         id.UserID("@u:e"),
				Timestamp:      time.UnixMilli(5000),
				RedactsEventID: id.EventID("$redacted"),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ObservedEventFromMatrixEvent(tt.evt)
			if !ok {
				t.Fatal("ObservedEventFromMatrixEvent did not convert event")
			}
			if tt.want.RoomID == "" {
				tt.want.RoomID = tt.evt.RoomID
			}
			if got != tt.want {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestBotObserveFiresWithoutRoutesAndWhenNoRouteMatches(t *testing.T) {
	sender := &fakeSender{}
	bot := newTestBot(sender, sender)
	var observed []ObservedEvent
	bot.observedEventHandler = ObservedEventHandlerFunc(func(_ context.Context, evt ObservedEvent) error {
		observed = append(observed, evt)
		return nil
	})

	bot.observeEvent(context.Background(), observedMessageEvent("$noroutes", "@u:e", "hello", 1000))
	bot.RouteIn(id.RoomID("!r:e"),
		TriggerFunc(func(context.Context, *event.Event, EventFetcher) (Request, bool, error) {
			return Request{}, false, nil
		}),
		HandlerFunc(func(context.Context, Request) (Response, error) {
			t.Fatal("handler must not run")
			return Response{}, nil
		}),
	)
	noMatch := observedMessageEvent("$nomatch", "@u:e", "still visible", 2000)
	bot.observeEvent(context.Background(), noMatch)
	bot.dispatch(context.Background(), noMatch)

	if len(observed) != 2 {
		t.Fatalf("observed %d events, want 2: %+v", len(observed), observed)
	}
	if observed[0].EventID != id.EventID("$noroutes") || observed[1].EventID != id.EventID("$nomatch") {
		t.Fatalf("observed event IDs = %q/%q", observed[0].EventID, observed[1].EventID)
	}
}

func TestBotObserverFailureIsLoggedAndDispatchStillRuns(t *testing.T) {
	sender := &fakeSender{}
	bot := newTestBot(sender, sender)
	bot.observedEventHandler = ObservedEventHandlerFunc(func(context.Context, ObservedEvent) error {
		return errors.New("indexer offline")
	})
	bot.RouteIn(id.RoomID("!r:e"),
		TriggerFunc(func(context.Context, *event.Event, EventFetcher) (Request, bool, error) {
			return Request{Input: "x"}, true, nil
		}),
		HandlerFunc(func(context.Context, Request) (Response, error) {
			return Response{Reply: "handled"}, nil
		}),
	)

	buf := captureSlog(t)
	evt := observedMessageEvent("$m1", "@u:e", "hello", 1000)
	bot.observeEvent(context.Background(), evt)
	bot.dispatch(context.Background(), evt)

	if len(sender.sent) != 1 || sender.sent[0] != "handled" {
		t.Fatalf("sent = %v, want handled reply", sender.sent)
	}
	if !strings.Contains(buf.String(), "observer error") || !strings.Contains(buf.String(), "indexer offline") {
		t.Fatalf("observer failure was not logged: %q", buf.String())
	}
}

func TestObservedEventsPageFetchesSingleBoundedPage(t *testing.T) {
	src := &fakeHistorySource{
		pages: []historyPage{{
			from:   "from-token",
			to:     "to-token",
			chunk:  []*event.Event{msgEvent("$m1", "@u:e", "hello", 1000)},
			endTok: "end-token",
		}},
	}
	bot := newHistoryBot(src, noopDecrypter{})

	got, err := bot.ObservedEventsPage(context.Background(), id.RoomID("!r:e"), "from-token", "to-token", mautrix.DirectionBackward, 12)
	if err != nil {
		t.Fatalf("ObservedEventsPage: %v", err)
	}
	if src.messagesN != 1 {
		t.Fatalf("Messages called %d times, want 1", src.messagesN)
	}
	if len(got.Events) != 1 || got.Events[0].Body != "hello" {
		t.Fatalf("events = %+v, want one observed message", got.Events)
	}
	if got.Start != "from-token" || got.End != "end-token" {
		t.Fatalf("tokens = %q/%q, want from-token/end-token", got.Start, got.End)
	}
	if src.gotMessagesFrom[0] != "from-token" || src.gotMessagesTo[0] != "to-token" || src.gotMessagesDir[0] != mautrix.DirectionBackward || src.gotMessagesLimit[0] != 12 {
		t.Fatalf("Messages args from=%v to=%v dir=%v limit=%v", src.gotMessagesFrom, src.gotMessagesTo, src.gotMessagesDir, src.gotMessagesLimit)
	}
}

func TestObservedEventsPageDecryptsAndSkipsUndecryptableEvents(t *testing.T) {
	src := &fakeHistorySource{
		pages: []historyPage{{
			from: "tok",
			chunk: []*event.Event{
				encryptedEvent("$ok", "@u:e", 1000),
				encryptedEvent("$bad", "@u:e", 2000),
				observedReactionEvent("$r1", "@u:e", "$ok", "✅", 3000),
			},
			endTok: "next",
		}},
	}
	dec := stubDecrypter{
		plaintexts: map[id.EventID]*event.Event{
			"$ok": msgEvent("$ok", "@u:e", "decrypted", 1000),
		},
		errs: map[id.EventID]error{
			"$bad": errors.New("missing session"),
		},
	}
	bot := newHistoryBot(src, dec)

	got, err := bot.ObservedEventsPage(context.Background(), id.RoomID("!r:e"), "tok", "", mautrix.DirectionForward, 10)
	if err != nil {
		t.Fatalf("ObservedEventsPage: %v", err)
	}
	if len(got.Events) != 2 {
		t.Fatalf("events = %+v, want decrypted message plus reaction", got.Events)
	}
	if got.Events[0].Body != "decrypted" || got.Events[1].ReactionKey != "✅" {
		t.Fatalf("events = %+v", got.Events)
	}
}

func TestObservedEventsPagePreservesDecryptContextErrors(t *testing.T) {
	for _, wantErr := range []error{context.Canceled, context.DeadlineExceeded} {
		t.Run(wantErr.Error(), func(t *testing.T) {
			src := &fakeHistorySource{
				pages: []historyPage{{
					from:  "tok",
					chunk: []*event.Event{encryptedEvent("$bad", "@u:e", 1000)},
				}},
			}
			bot := newHistoryBot(src, stubDecrypter{errs: map[id.EventID]error{"$bad": wantErr}})

			got, err := bot.ObservedEventsPage(context.Background(), id.RoomID("!r:e"), "tok", "", mautrix.DirectionForward, 10)
			if !errors.Is(err, wantErr) {
				t.Fatalf("ObservedEventsPage error = %v, want wrapping %v", err, wantErr)
			}
			if got.Events != nil {
				t.Fatalf("events = %+v, want nil on cancellation", got.Events)
			}
		})
	}
}
