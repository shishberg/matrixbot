package matrixbot

import (
	"context"
	"errors"
	"testing"
	"time"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

// fakeHistorySource fakes mautrix.Client's Context+Messages calls. The fake
// is page-based: Context returns the (Start, End) tokens of page 0, and each
// subsequent Messages call with `from` matching pages[i].End advances to page
// i+1. A page whose end token matches no further page is the end of history.
type fakeHistorySource struct {
	contextResp *mautrix.RespContext
	contextErr  error
	contextN    int

	pages       []historyPage
	messagesN   int
	messagesErr error

	gotContextRoom    id.RoomID
	gotContextEventID id.EventID
	gotMessagesFrom   []string
}

type historyPage struct {
	from   string
	to     string
	chunk  []*event.Event
	endTok string
}

func (f *fakeHistorySource) Context(_ context.Context, roomID id.RoomID, eventID id.EventID, _ *mautrix.FilterPart, _ int) (*mautrix.RespContext, error) {
	f.contextN++
	f.gotContextRoom = roomID
	f.gotContextEventID = eventID
	if f.contextErr != nil {
		return nil, f.contextErr
	}
	return f.contextResp, nil
}

func (f *fakeHistorySource) Messages(_ context.Context, _ id.RoomID, from, _ string, _ mautrix.Direction, _ *mautrix.FilterPart, _ int) (*mautrix.RespMessages, error) {
	f.messagesN++
	f.gotMessagesFrom = append(f.gotMessagesFrom, from)
	if f.messagesErr != nil {
		return nil, f.messagesErr
	}
	for _, p := range f.pages {
		if p.from == from {
			return &mautrix.RespMessages{Start: from, End: p.endTok, Chunk: p.chunk}, nil
		}
	}
	// No matching page: timeline exhausted. Return an empty chunk and an
	// empty End token so the caller stops paginating.
	return &mautrix.RespMessages{Start: from, End: "", Chunk: nil}, nil
}

// noopDecrypter passes events through unchanged. The history layer treats
// decryption as opaque, so a fake that doesn't actually decrypt is enough to
// drive most tests; only the encryption-specific test installs a real-ish one.
type noopDecrypter struct{}

func (noopDecrypter) decrypt(_ context.Context, evt *event.Event) (*event.Event, error) {
	return evt, nil
}

// stubDecrypter rewrites encrypted events into the canned plaintext keyed
// by event ID. Plain events pass through.
type stubDecrypter struct {
	plaintexts map[id.EventID]*event.Event
	errs       map[id.EventID]error
}

func (s stubDecrypter) decrypt(_ context.Context, evt *event.Event) (*event.Event, error) {
	if evt == nil || evt.Type != event.EventEncrypted {
		return evt, nil
	}
	if err, ok := s.errs[evt.ID]; ok {
		return nil, err
	}
	if pt, ok := s.plaintexts[evt.ID]; ok {
		return pt, nil
	}
	return nil, errors.New("no plaintext")
}

func msgEvent(idStr, sender, body string, tsMillis int64) *event.Event {
	return &event.Event{
		Type:      event.EventMessage,
		ID:        id.EventID(idStr),
		Sender:    id.UserID(sender),
		Timestamp: tsMillis,
		Content:   event.Content{Parsed: &event.MessageEventContent{MsgType: event.MsgText, Body: body}},
	}
}

func reactionEvent(idStr, sender string, tsMillis int64) *event.Event {
	return &event.Event{
		Type:      event.EventReaction,
		ID:        id.EventID(idStr),
		Sender:    id.UserID(sender),
		Timestamp: tsMillis,
	}
}

func memberJoinEvent(idStr, sender string, tsMillis int64) *event.Event {
	sk := sender
	return &event.Event{
		Type:      event.StateMember,
		ID:        id.EventID(idStr),
		Sender:    id.UserID(sender),
		StateKey:  &sk,
		Timestamp: tsMillis,
		Content:   event.Content{Parsed: &event.MemberEventContent{Membership: event.MembershipJoin}},
	}
}

func encryptedEvent(idStr, sender string, tsMillis int64) *event.Event {
	return &event.Event{
		Type:      event.EventEncrypted,
		ID:        id.EventID(idStr),
		Sender:    id.UserID(sender),
		Timestamp: tsMillis,
	}
}

func newHistoryBot(src historySource, dec eventDecrypter) *Bot {
	return &Bot{
		botUserID: id.UserID("@bot:e"),
		history:   src,
		decrypter: dec,
	}
}

// TestPreviousMessagesZeroLimitMakesNoCall pins the trivial fast path.
// Callers asking for zero context shouldn't pay for a round trip — and an
// `ok` callsite shouldn't have to special-case the zero before calling.
func TestPreviousMessagesZeroLimitMakesNoCall(t *testing.T) {
	src := &fakeHistorySource{}
	bot := newHistoryBot(src, noopDecrypter{})

	got, err := bot.PreviousMessages(context.Background(), id.RoomID("!r:e"), id.EventID("$x"), 0)
	if err != nil {
		t.Fatalf("PreviousMessages: %v", err)
	}
	if got != nil {
		t.Errorf("got %v, want nil", got)
	}
	if src.contextN != 0 || src.messagesN != 0 {
		t.Errorf("API calls = (Context=%d, Messages=%d), want 0/0", src.contextN, src.messagesN)
	}
}

// TestPreviousMessagesNegativeLimitMakesNoCall pins symmetric handling for
// arithmetic that produces a negative limit. We treat it the same as zero.
func TestPreviousMessagesNegativeLimitMakesNoCall(t *testing.T) {
	src := &fakeHistorySource{}
	bot := newHistoryBot(src, noopDecrypter{})

	got, err := bot.PreviousMessages(context.Background(), id.RoomID("!r:e"), id.EventID("$x"), -3)
	if err != nil {
		t.Fatalf("PreviousMessages: %v", err)
	}
	if got != nil {
		t.Errorf("got %v, want nil", got)
	}
	if src.contextN != 0 || src.messagesN != 0 {
		t.Errorf("API calls = (Context=%d, Messages=%d), want 0/0", src.contextN, src.messagesN)
	}
}

// TestPreviousMessagesEmptyRoomReturnsEmpty pins the no-history case: a
// room whose Context call returns no preceding events and no token must
// yield an empty slice (no error, no infinite loop).
func TestPreviousMessagesEmptyRoomReturnsEmpty(t *testing.T) {
	src := &fakeHistorySource{
		contextResp: &mautrix.RespContext{Start: "", EventsBefore: nil},
	}
	bot := newHistoryBot(src, noopDecrypter{})

	got, err := bot.PreviousMessages(context.Background(), id.RoomID("!r:e"), id.EventID("$x"), 5)
	if err != nil {
		t.Fatalf("PreviousMessages: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %v, want empty", got)
	}
}

// TestPreviousMessagesReturnsBeforeEventsOldestFirst pins ordering.
// Context returns EventsBefore newest-first; the API surface promises
// oldest-first because callers will format chronologically.
func TestPreviousMessagesReturnsBeforeEventsOldestFirst(t *testing.T) {
	// EventsBefore is newest-first per Matrix spec.
	src := &fakeHistorySource{
		contextResp: &mautrix.RespContext{
			Start: "tok-end",
			EventsBefore: []*event.Event{
				msgEvent("$3", "@u:e", "third", 3000),
				msgEvent("$2", "@u:e", "second", 2000),
				msgEvent("$1", "@u:e", "first", 1000),
			},
		},
	}
	bot := newHistoryBot(src, noopDecrypter{})

	got, err := bot.PreviousMessages(context.Background(), id.RoomID("!r:e"), id.EventID("$cur"), 5)
	if err != nil {
		t.Fatalf("PreviousMessages: %v", err)
	}
	want := []string{"first", "second", "third"}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d (got=%+v)", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i].Body != w {
			t.Errorf("[%d] Body = %q, want %q", i, got[i].Body, w)
		}
	}
	if got[0].EventID != id.EventID("$1") || got[2].EventID != id.EventID("$3") {
		t.Errorf("EventIDs out of order: %+v", got)
	}
	if got[0].Sender != id.UserID("@u:e") {
		t.Errorf("Sender = %q, want @u:e", got[0].Sender)
	}
	if !got[0].Timestamp.Equal(time.UnixMilli(1000)) {
		t.Errorf("Timestamp = %v, want %v", got[0].Timestamp, time.UnixMilli(1000))
	}
	if src.gotContextRoom != id.RoomID("!r:e") || src.gotContextEventID != id.EventID("$cur") {
		t.Errorf("Context called with (%q,%q), want (!r:e,$cur)", src.gotContextRoom, src.gotContextEventID)
	}
}

// TestPreviousMessagesExcludesBeforeEventItself pins the strict-before
// contract: when the bot is handling a mention event, the mention itself
// must not appear in the prior context (it is the current input).
func TestPreviousMessagesExcludesBeforeEventItself(t *testing.T) {
	src := &fakeHistorySource{
		contextResp: &mautrix.RespContext{
			// Some homeservers include the target event in EventsBefore;
			// the API contract is "strictly before".
			EventsBefore: []*event.Event{
				msgEvent("$cur", "@u:e", "current", 4000),
				msgEvent("$2", "@u:e", "second", 2000),
				msgEvent("$1", "@u:e", "first", 1000),
			},
		},
	}
	bot := newHistoryBot(src, noopDecrypter{})

	got, err := bot.PreviousMessages(context.Background(), id.RoomID("!r:e"), id.EventID("$cur"), 5)
	if err != nil {
		t.Fatalf("PreviousMessages: %v", err)
	}
	for _, m := range got {
		if m.EventID == id.EventID("$cur") {
			t.Fatalf("returned the before event itself: %+v", got)
		}
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
}

// TestPreviousMessagesFiltersNonMessageEvents pins that joins, reactions,
// and other non-`m.room.message` events are dropped — and crucially, the
// drop happens after pagination, so an active room with lots of state
// noise still returns up to `limit` actual messages.
func TestPreviousMessagesFiltersNonMessageEvents(t *testing.T) {
	src := &fakeHistorySource{
		contextResp: &mautrix.RespContext{
			EventsBefore: []*event.Event{
				reactionEvent("$r1", "@u:e", 5000),
				msgEvent("$m2", "@u:e", "second", 4000),
				memberJoinEvent("$j1", "@u:e", 3000),
				msgEvent("$m1", "@u:e", "first", 1000),
			},
		},
	}
	bot := newHistoryBot(src, noopDecrypter{})

	got, err := bot.PreviousMessages(context.Background(), id.RoomID("!r:e"), id.EventID("$cur"), 10)
	if err != nil {
		t.Fatalf("PreviousMessages: %v", err)
	}
	want := []string{"first", "second"}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d (got=%+v)", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i].Body != w {
			t.Errorf("[%d] Body = %q, want %q", i, got[i].Body, w)
		}
	}
}

// TestPreviousMessagesDecryptsEncryptedEvents pins that encrypted events
// from the timeline are decrypted via the same path as parent-event
// lookups, so callers see plaintext bodies regardless of room E2EE state.
func TestPreviousMessagesDecryptsEncryptedEvents(t *testing.T) {
	src := &fakeHistorySource{
		contextResp: &mautrix.RespContext{
			EventsBefore: []*event.Event{
				encryptedEvent("$e2", "@u:e", 4000),
				encryptedEvent("$e1", "@u:e", 1000),
			},
		},
	}
	dec := stubDecrypter{plaintexts: map[id.EventID]*event.Event{
		"$e1": msgEvent("$e1", "@u:e", "decrypted-1", 1000),
		"$e2": msgEvent("$e2", "@u:e", "decrypted-2", 4000),
	}}
	bot := newHistoryBot(src, dec)

	got, err := bot.PreviousMessages(context.Background(), id.RoomID("!r:e"), id.EventID("$cur"), 5)
	if err != nil {
		t.Fatalf("PreviousMessages: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].Body != "decrypted-1" || got[1].Body != "decrypted-2" {
		t.Errorf("bodies = %q/%q, want decrypted-1/decrypted-2", got[0].Body, got[1].Body)
	}
}

func TestPreviousMessagesSkipsDecryptFailuresAndKeepsPaginating(t *testing.T) {
	src := &fakeHistorySource{
		contextResp: &mautrix.RespContext{
			Start: "tok1",
			EventsBefore: []*event.Event{
				msgEvent("$m3", "@u:e", "three", 3000),
				encryptedEvent("$bad", "@u:e", 2500),
			},
		},
		pages: []historyPage{
			{
				from:   "tok1",
				endTok: "tok2",
				chunk: []*event.Event{
					encryptedEvent("$e2", "@u:e", 2000),
					msgEvent("$m1", "@u:e", "one", 1000),
				},
			},
		},
	}
	dec := stubDecrypter{
		plaintexts: map[id.EventID]*event.Event{
			"$e2": msgEvent("$e2", "@u:e", "two", 2000),
		},
		errs: map[id.EventID]error{
			"$bad": errors.New("unsupported event encryption algorithm"),
		},
	}
	bot := newHistoryBot(src, dec)

	got, err := bot.PreviousMessages(context.Background(), id.RoomID("!r:e"), id.EventID("$cur"), 3)
	if err != nil {
		t.Fatalf("PreviousMessages: %v", err)
	}
	want := []string{"one", "two", "three"}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d (got=%+v)", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i].Body != w {
			t.Errorf("[%d] Body = %q, want %q", i, got[i].Body, w)
		}
	}
	if src.messagesN != 1 {
		t.Errorf("Messages called %d times, want 1", src.messagesN)
	}
}

// TestPreviousMessagesPaginatesUntilLimit pins that one page returning
// fewer messages than requested triggers a follow-up Messages call using
// the End token, and so on until limit is reached.
func TestPreviousMessagesPaginatesUntilLimit(t *testing.T) {
	src := &fakeHistorySource{
		contextResp: &mautrix.RespContext{
			Start: "tok1",
			EventsBefore: []*event.Event{
				msgEvent("$3", "@u:e", "three", 3000),
			},
		},
		pages: []historyPage{
			{
				from:   "tok1",
				endTok: "tok2",
				chunk: []*event.Event{
					msgEvent("$2", "@u:e", "two", 2000),
				},
			},
			{
				from:   "tok2",
				endTok: "tok3",
				chunk: []*event.Event{
					msgEvent("$1", "@u:e", "one", 1000),
				},
			},
		},
	}
	bot := newHistoryBot(src, noopDecrypter{})

	got, err := bot.PreviousMessages(context.Background(), id.RoomID("!r:e"), id.EventID("$cur"), 3)
	if err != nil {
		t.Fatalf("PreviousMessages: %v", err)
	}
	want := []string{"one", "two", "three"}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d (got=%+v)", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i].Body != w {
			t.Errorf("[%d] Body = %q, want %q", i, got[i].Body, w)
		}
	}
}

// TestPreviousMessagesPaginationSkipsNoiseToReachLimit pins the interaction
// between pagination and filtering: if a page contains only filterable
// events (reactions, joins) we keep paginating until we have `limit`
// messages or the timeline ends.
func TestPreviousMessagesPaginationSkipsNoiseToReachLimit(t *testing.T) {
	src := &fakeHistorySource{
		contextResp: &mautrix.RespContext{
			Start:        "tok1",
			EventsBefore: []*event.Event{reactionEvent("$r1", "@u:e", 5000)},
		},
		pages: []historyPage{
			{
				from:   "tok1",
				endTok: "tok2",
				chunk:  []*event.Event{memberJoinEvent("$j1", "@u:e", 4000)},
			},
			{
				from:   "tok2",
				endTok: "tok3",
				chunk: []*event.Event{
					msgEvent("$m2", "@u:e", "two", 3000),
					msgEvent("$m1", "@u:e", "one", 1000),
				},
			},
		},
	}
	bot := newHistoryBot(src, noopDecrypter{})

	got, err := bot.PreviousMessages(context.Background(), id.RoomID("!r:e"), id.EventID("$cur"), 2)
	if err != nil {
		t.Fatalf("PreviousMessages: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].Body != "one" || got[1].Body != "two" {
		t.Errorf("bodies = %q/%q, want one/two", got[0].Body, got[1].Body)
	}
}

// TestPreviousMessagesPaginationStopsWhenHistoryExhausted pins that we
// don't error when the timeline runs out of events before the limit is
// reached: the caller just gets whatever exists.
func TestPreviousMessagesPaginationStopsWhenHistoryExhausted(t *testing.T) {
	src := &fakeHistorySource{
		contextResp: &mautrix.RespContext{
			Start:        "tok1",
			EventsBefore: []*event.Event{msgEvent("$2", "@u:e", "two", 2000)},
		},
		pages: []historyPage{
			{
				from:   "tok1",
				endTok: "tok2",
				chunk:  []*event.Event{msgEvent("$1", "@u:e", "one", 1000)},
			},
			// No page registered for "tok2" — the fake returns an empty
			// chunk with an empty End token, signalling end of history.
		},
	}
	bot := newHistoryBot(src, noopDecrypter{})

	got, err := bot.PreviousMessages(context.Background(), id.RoomID("!r:e"), id.EventID("$cur"), 10)
	if err != nil {
		t.Fatalf("PreviousMessages: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2 (history exhausted)", len(got))
	}
	if got[0].Body != "one" || got[1].Body != "two" {
		t.Errorf("bodies = %q/%q, want one/two", got[0].Body, got[1].Body)
	}
}

// fakeSpinningSource always returns an empty chunk with a fresh non-empty
// End token. A naive loop using "End != \"\"" as the only stop condition
// would spin here.
type fakeSpinningSource struct {
	messagesN int
}

func (f *fakeSpinningSource) Context(_ context.Context, _ id.RoomID, _ id.EventID, _ *mautrix.FilterPart, _ int) (*mautrix.RespContext, error) {
	return &mautrix.RespContext{Start: "tok-0"}, nil
}

func (f *fakeSpinningSource) Messages(_ context.Context, _ id.RoomID, from, _ string, _ mautrix.Direction, _ *mautrix.FilterPart, _ int) (*mautrix.RespMessages, error) {
	f.messagesN++
	// Always return a token that the next call will receive but that never
	// converges — the production code must cap the loop.
	return &mautrix.RespMessages{Start: from, End: from + "+", Chunk: nil}, nil
}

// TestPreviousMessagesPaginationIsBounded pins the safety cap: a malicious
// or buggy server returning empty pages with new tokens forever must not
// spin the bot. The cap exists so a stuck server can't burn the dispatch
// goroutine; if you change maxHistoryPages, update this test.
func TestPreviousMessagesPaginationIsBounded(t *testing.T) {
	src := &fakeSpinningSource{}
	bot := newHistoryBot(src, noopDecrypter{})

	got, err := bot.PreviousMessages(context.Background(), id.RoomID("!r:e"), id.EventID("$cur"), 10)
	if err != nil {
		t.Fatalf("PreviousMessages: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %v, want empty", got)
	}
	if src.messagesN > maxHistoryPages {
		t.Errorf("Messages called %d times, expected <= %d (the page cap)", src.messagesN, maxHistoryPages)
	}
	if src.messagesN == 0 {
		t.Errorf("Messages never called; the loop short-circuited too early")
	}
}
