package matrixbot

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

// captureSlog redirects slog.Default to a buffer at DEBUG level so tests can
// assert on log output. The previous default is restored when the test ends.
func captureSlog(t *testing.T) *bytes.Buffer {
	t.Helper()
	prev := slog.Default()
	buf := &bytes.Buffer{}
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

// fakeSender records SendMessageEvent calls and answers GetEvent from a map.
// It satisfies both matrixSender and EventFetcher.
type fakeSender struct {
	mu      sync.Mutex
	sent    []string
	parents map[id.EventID]*event.Event
	getErr  error
	sendErr error
}

// fakeJoiner records JoinRoomByID calls so invite-handler tests can assert
// which rooms (if any) the bot tried to join.
type fakeJoiner struct {
	mu     sync.Mutex
	joined []id.RoomID
	err    error
}

func (f *fakeJoiner) JoinRoomByID(ctx context.Context, roomID id.RoomID) (*mautrix.RespJoinRoom, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	f.joined = append(f.joined, roomID)
	return &mautrix.RespJoinRoom{RoomID: roomID}, nil
}

func (f *fakeSender) SendMessageEvent(ctx context.Context, roomID id.RoomID, eventType event.Type, contentJSON interface{}, extra ...mautrix.ReqSendEvent) (*mautrix.RespSendEvent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sendErr != nil {
		return nil, f.sendErr
	}
	if mec, ok := contentJSON.(*event.MessageEventContent); ok && mec != nil {
		f.sent = append(f.sent, mec.Body)
	}
	return &mautrix.RespSendEvent{}, nil
}

func (f *fakeSender) GetEvent(ctx context.Context, roomID id.RoomID, eventID id.EventID) (*event.Event, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	if e, ok := f.parents[eventID]; ok {
		return e, nil
	}
	return nil, errors.New("not found")
}

func newTestBot(sender matrixSender, fetcher EventFetcher) *Bot {
	return &Bot{
		sender:        sender,
		fetcher:       fetcher,
		botUserID:     id.UserID("@bot:e"),
		autoJoinRooms: map[id.RoomID]bool{id.RoomID("!target:e"): true},
	}
}

func newTestBotWithJoiner(sender matrixSender, fetcher EventFetcher, joiner roomJoiner) *Bot {
	return &Bot{
		sender:        sender,
		fetcher:       fetcher,
		joiner:        joiner,
		botUserID:     id.UserID("@bot:e"),
		autoJoinRooms: map[id.RoomID]bool{id.RoomID("!target:e"): true},
	}
}

func memberEvent(roomID id.RoomID, stateKey string, membership event.Membership) *event.Event {
	return memberEventFrom(roomID, stateKey, membership, id.UserID("@inviter:e"))
}

func memberEventFrom(roomID id.RoomID, stateKey string, membership event.Membership, sender id.UserID) *event.Event {
	sk := stateKey
	return &event.Event{
		Type:     event.StateMember,
		RoomID:   roomID,
		StateKey: &sk,
		Sender:   sender,
		Content: event.Content{
			Parsed: &event.MemberEventContent{Membership: membership},
		},
	}
}

func TestNewBotStoresCryptoConfig(t *testing.T) {
	cfg := BotConfig{
		Homeserver:  "https://matrix.example",
		UserID:      "@bot:example",
		AccessToken: "tok",
		DeviceID:    "DEV",
		PickleKey:   "pickle-secret",
		CryptoDB:    "/tmp/matrixbot-crypto-test.db",
	}
	bot, err := NewBot(cfg)
	if err != nil {
		t.Fatalf("NewBot: %v", err)
	}
	if bot.pickleKey != "pickle-secret" {
		t.Errorf("pickleKey = %q, want %q", bot.pickleKey, "pickle-secret")
	}
	if bot.cryptoDB != "/tmp/matrixbot-crypto-test.db" {
		t.Errorf("cryptoDB = %q, want %q", bot.cryptoDB, "/tmp/matrixbot-crypto-test.db")
	}
}

func TestNewBotStoresCrossSigningConfig(t *testing.T) {
	cfg := BotConfig{
		Homeserver:     "https://matrix.example",
		UserID:         "@bot:example",
		AccessToken:    "tok",
		DeviceID:       "DEV",
		PickleKey:      "pickle-secret",
		CryptoDB:       "/tmp/matrixbot-crypto-test.db",
		RecoveryKey:    "EsTQ 9MUs xSRn",
		OperatorUserID: "@dave:example",
	}
	bot, err := NewBot(cfg)
	if err != nil {
		t.Fatalf("NewBot: %v", err)
	}
	if bot.recoveryKey != "EsTQ 9MUs xSRn" {
		t.Errorf("recoveryKey = %q, want %q", bot.recoveryKey, "EsTQ 9MUs xSRn")
	}
	if bot.operatorUserID != "@dave:example" {
		t.Errorf("operatorUserID = %q, want %q", bot.operatorUserID, "@dave:example")
	}
}

func TestNewBotUsesProvidedLogger(t *testing.T) {
	// If BotConfig.Logger is set, the bot should pipe mautrix's zerolog
	// through it instead of constructing its own. That keeps the host's slog
	// handler and mautrix's zerolog calls sharing one underlying writer.
	zl := zerolog.Nop()
	cfg := BotConfig{
		Homeserver:  "https://matrix.example",
		UserID:      "@bot:example",
		AccessToken: "tok",
		DeviceID:    "DEV",
		Logger:      &zl,
	}
	bot, err := NewBot(cfg)
	if err != nil {
		t.Fatalf("NewBot: %v", err)
	}
	if bot.client.Log.GetLevel() != zl.GetLevel() {
		t.Errorf("client.Log not the provided logger (got level %v, want %v)", bot.client.Log.GetLevel(), zl.GetLevel())
	}
}

func TestNewBotCryptoDisabledWhenPickleKeyEmpty(t *testing.T) {
	cfg := BotConfig{
		Homeserver:  "https://matrix.example",
		UserID:      "@bot:example",
		AccessToken: "tok",
		DeviceID:    "DEV",
		PickleKey:   "",
		CryptoDB:    "./matrixbot-crypto.db",
	}
	bot, err := NewBot(cfg)
	if err != nil {
		t.Fatalf("NewBot: %v", err)
	}
	if bot.pickleKey != "" {
		t.Errorf("pickleKey = %q, want empty", bot.pickleKey)
	}
}

// TestNewBotPopulatesAutoJoinRooms pins the BotConfig contract: AutoJoinRooms
// is fully consumed inside NewBot rather than left for the caller to mutate
// after construction. The previous host-mutates-the-map shortcut leaked an
// implementation detail (the unexported map) into the host package.
func TestNewBotPopulatesAutoJoinRooms(t *testing.T) {
	cfg := BotConfig{
		Homeserver:  "https://matrix.example",
		UserID:      "@bot:example",
		AccessToken: "tok",
		DeviceID:    "DEV",
		AutoJoinRooms: []id.RoomID{
			id.RoomID("!one:example"),
			id.RoomID("!two:example"),
		},
	}
	bot, err := NewBot(cfg)
	if err != nil {
		t.Fatalf("NewBot: %v", err)
	}
	if !bot.autoJoinRooms[id.RoomID("!one:example")] {
		t.Errorf("autoJoinRooms missing !one:example: %v", bot.autoJoinRooms)
	}
	if !bot.autoJoinRooms[id.RoomID("!two:example")] {
		t.Errorf("autoJoinRooms missing !two:example: %v", bot.autoJoinRooms)
	}
	if bot.autoJoinRooms[id.RoomID("!other:example")] {
		t.Errorf("autoJoinRooms unexpectedly contains !other:example")
	}
}

func TestBotHandleInviteJoinsConfiguredRoom(t *testing.T) {
	sender := &fakeSender{}
	joiner := &fakeJoiner{}
	bot := newTestBotWithJoiner(sender, sender, joiner)

	bot.handleInvite(context.Background(), memberEvent(
		id.RoomID("!target:e"), "@bot:e", event.MembershipInvite,
	))

	if got, want := joiner.joined, []id.RoomID{"!target:e"}; len(got) != 1 || got[0] != want[0] {
		t.Errorf("joined = %v, want %v", got, want)
	}
}

func TestBotHandleInviteIgnoresOtherRoom(t *testing.T) {
	sender := &fakeSender{}
	joiner := &fakeJoiner{}
	bot := newTestBotWithJoiner(sender, sender, joiner)

	bot.handleInvite(context.Background(), memberEvent(
		id.RoomID("!other:e"), "@bot:e", event.MembershipInvite,
	))

	if len(joiner.joined) != 0 {
		t.Errorf("joined = %v, want empty (invite to non-target room)", joiner.joined)
	}
}

func TestBotHandleInviteIgnoresOtherUserInTargetRoom(t *testing.T) {
	sender := &fakeSender{}
	joiner := &fakeJoiner{}
	bot := newTestBotWithJoiner(sender, sender, joiner)

	bot.handleInvite(context.Background(), memberEvent(
		id.RoomID("!target:e"), "@somebody-else:e", event.MembershipInvite,
	))

	if len(joiner.joined) != 0 {
		t.Errorf("joined = %v, want empty (invite for another user)", joiner.joined)
	}
}

func TestHandleInviteJoinsOperatorRoom(t *testing.T) {
	sender := &fakeSender{}
	joiner := &fakeJoiner{}
	bot := newTestBotWithJoiner(sender, sender, joiner)
	bot.operatorUserID = "@dave:e"

	bot.handleInvite(context.Background(), memberEventFrom(
		id.RoomID("!verify:e"), "@bot:e", event.MembershipInvite, id.UserID("@dave:e"),
	))

	if got, want := joiner.joined, []id.RoomID{"!verify:e"}; len(got) != 1 || got[0] != want[0] {
		t.Errorf("joined = %v, want %v (operator invite to non-target room)", got, want)
	}
}

// TestHandleInviteJoinsOperatorTargetRoomOverlap pins the harmless overlap
// case: when both predicates would fire (operator inviting into the target
// room), we still join exactly once and don't get tangled in the OR.
func TestHandleInviteJoinsOperatorTargetRoomOverlap(t *testing.T) {
	sender := &fakeSender{}
	joiner := &fakeJoiner{}
	bot := newTestBotWithJoiner(sender, sender, joiner)
	bot.operatorUserID = "@dave:e"

	bot.handleInvite(context.Background(), memberEventFrom(
		id.RoomID("!target:e"), "@bot:e", event.MembershipInvite, id.UserID("@dave:e"),
	))

	if got, want := joiner.joined, []id.RoomID{"!target:e"}; len(got) != 1 || got[0] != want[0] {
		t.Errorf("joined = %v, want %v (operator invite to target room)", got, want)
	}
}

func TestHandleInviteIgnoresStrangerNonTargetRoom(t *testing.T) {
	sender := &fakeSender{}
	joiner := &fakeJoiner{}
	bot := newTestBotWithJoiner(sender, sender, joiner)
	bot.operatorUserID = "@dave:e"

	bot.handleInvite(context.Background(), memberEventFrom(
		id.RoomID("!random:e"), "@bot:e", event.MembershipInvite, id.UserID("@stranger:e"),
	))

	if len(joiner.joined) != 0 {
		t.Errorf("joined = %v, want empty (stranger invited to non-target room)", joiner.joined)
	}
}

func TestHandleInviteIgnoresOperatorWhenOperatorUnset(t *testing.T) {
	sender := &fakeSender{}
	joiner := &fakeJoiner{}
	bot := newTestBotWithJoiner(sender, sender, joiner)
	// operatorUserID intentionally left empty: verification disabled must not
	// silently widen auto-join to "anyone who invites us".

	bot.handleInvite(context.Background(), memberEventFrom(
		id.RoomID("!random:e"), "@bot:e", event.MembershipInvite, id.UserID("@dave:e"),
	))

	if len(joiner.joined) != 0 {
		t.Errorf("joined = %v, want empty (operator unset must not widen policy)", joiner.joined)
	}
}

func TestBotHandleInviteLogsJoinError(t *testing.T) {
	sender := &fakeSender{}
	joiner := &fakeJoiner{err: errors.New("network down")}
	bot := newTestBotWithJoiner(sender, sender, joiner)

	buf := captureSlog(t)

	bot.handleInvite(context.Background(), memberEvent(
		id.RoomID("!target:e"), "@bot:e", event.MembershipInvite,
	))

	if !strings.Contains(buf.String(), "network down") {
		t.Errorf("expected join error in log, got %q", buf.String())
	}
	if len(joiner.joined) != 0 {
		t.Errorf("joined = %v, want empty (join failed)", joiner.joined)
	}
}

func TestBotHandleInviteIgnoresNonInviteMembership(t *testing.T) {
	for _, m := range []event.Membership{event.MembershipJoin, event.MembershipLeave, event.MembershipBan} {
		t.Run(string(m), func(t *testing.T) {
			sender := &fakeSender{}
			joiner := &fakeJoiner{}
			bot := newTestBotWithJoiner(sender, sender, joiner)

			bot.handleInvite(context.Background(), memberEvent(
				id.RoomID("!target:e"), "@bot:e", m,
			))

			if len(joiner.joined) != 0 {
				t.Errorf("joined = %v, want empty (membership=%s)", joiner.joined, m)
			}
		})
	}
}

func TestBotDispatchFirstMatchWins(t *testing.T) {
	sender := &fakeSender{}
	bot := newTestBot(sender, sender)

	first, second := 0, 0

	bot.RouteIn(id.RoomID("!r:e"),
		TriggerFunc(func(ctx context.Context, evt *event.Event, f EventFetcher) (Request, bool, error) {
			return Request{Input: "first"}, true, nil
		}),
		HandlerFunc(func(ctx context.Context, req Request) (Response, error) {
			first++
			return Response{Reply: "from-first"}, nil
		}),
	)
	bot.RouteIn(id.RoomID("!r:e"),
		TriggerFunc(func(ctx context.Context, evt *event.Event, f EventFetcher) (Request, bool, error) {
			return Request{Input: "second"}, true, nil
		}),
		HandlerFunc(func(ctx context.Context, req Request) (Response, error) {
			second++
			return Response{}, nil
		}),
	)

	bot.dispatch(context.Background(), &event.Event{RoomID: id.RoomID("!r:e")})

	if first != 1 {
		t.Errorf("first handler called %d times, want 1", first)
	}
	if second != 0 {
		t.Errorf("second handler called %d times, want 0 (first match wins)", second)
	}
	if len(sender.sent) != 1 || sender.sent[0] != "from-first" {
		t.Errorf("sent = %v", sender.sent)
	}
}

func TestBotDispatchNoMatchSendsNothing(t *testing.T) {
	sender := &fakeSender{}
	bot := newTestBot(sender, sender)
	bot.RouteIn(id.RoomID("!r:e"),
		TriggerFunc(func(ctx context.Context, evt *event.Event, f EventFetcher) (Request, bool, error) {
			return Request{}, false, nil
		}),
		HandlerFunc(func(ctx context.Context, req Request) (Response, error) {
			t.Fatal("handler must not run when trigger returns false")
			return Response{}, nil
		}),
	)
	bot.dispatch(context.Background(), &event.Event{RoomID: id.RoomID("!r:e")})
	if len(sender.sent) != 0 {
		t.Errorf("nothing should have been sent, got %v", sender.sent)
	}
}

func TestBotDispatchHandlerErrorSendsApology(t *testing.T) {
	sender := &fakeSender{}
	bot := newTestBot(sender, sender)
	bot.RouteIn(id.RoomID("!r:e"),
		TriggerFunc(func(ctx context.Context, evt *event.Event, f EventFetcher) (Request, bool, error) {
			return Request{Input: "x"}, true, nil
		}),
		HandlerFunc(func(ctx context.Context, req Request) (Response, error) {
			return Response{}, errors.New("kaboom")
		}),
	)
	bot.dispatch(context.Background(), &event.Event{RoomID: id.RoomID("!r:e")})
	if len(sender.sent) != 1 {
		t.Fatalf("expected one message, got %v", sender.sent)
	}
	if !strings.Contains(sender.sent[0], "kaboom") {
		t.Errorf("apology should mention the error, got %q", sender.sent[0])
	}
	if !strings.Contains(strings.ToLower(sender.sent[0]), "sorry") {
		t.Errorf("apology should say sorry, got %q", sender.sent[0])
	}
}

func TestBotDispatchEmptyReplyStaysQuiet(t *testing.T) {
	sender := &fakeSender{}
	bot := newTestBot(sender, sender)
	bot.RouteIn(id.RoomID("!r:e"),
		TriggerFunc(func(ctx context.Context, evt *event.Event, f EventFetcher) (Request, bool, error) {
			return Request{Input: "x"}, true, nil
		}),
		HandlerFunc(func(ctx context.Context, req Request) (Response, error) {
			return Response{Reply: ""}, nil
		}),
	)
	bot.dispatch(context.Background(), &event.Event{RoomID: id.RoomID("!r:e")})
	if len(sender.sent) != 0 {
		t.Errorf("empty reply should send nothing, got %v", sender.sent)
	}
}

func TestBotDispatchTriggerErrorSkipsAllRoutes(t *testing.T) {
	// A Trigger.Apply error is a hard fail for that event: don't try later
	// routes (otherwise an EventFetcher hiccup on route 1 silently routes
	// the event to route 2, which is almost certainly not what the operator
	// wants).
	sender := &fakeSender{}
	bot := newTestBot(sender, sender)

	laterCalled := false
	bot.RouteIn(id.RoomID("!r:e"),
		TriggerFunc(func(ctx context.Context, evt *event.Event, f EventFetcher) (Request, bool, error) {
			return Request{}, false, errors.New("fetch failed")
		}),
		HandlerFunc(func(ctx context.Context, req Request) (Response, error) {
			t.Fatal("first handler must not run when trigger errors")
			return Response{}, nil
		}),
	)
	bot.RouteIn(id.RoomID("!r:e"),
		TriggerFunc(func(ctx context.Context, evt *event.Event, f EventFetcher) (Request, bool, error) {
			return Request{Input: "ok"}, true, nil
		}),
		HandlerFunc(func(ctx context.Context, req Request) (Response, error) {
			laterCalled = true
			return Response{Reply: "should not appear"}, nil
		}),
	)

	bot.dispatch(context.Background(), &event.Event{RoomID: id.RoomID("!r:e")})
	if laterCalled {
		t.Error("later route fired despite earlier trigger error")
	}
	if len(sender.sent) != 0 {
		t.Errorf("no message should be sent on trigger error, got %v", sender.sent)
	}
}

func TestBotDispatchLogsWhenNoRouteMatches(t *testing.T) {
	sender := &fakeSender{}
	bot := newTestBot(sender, sender)
	// Two non-matching routes — if the no-match log were placed inside the
	// loop instead of after it, we'd see two log lines, not one.
	nonMatch := TriggerFunc(func(ctx context.Context, evt *event.Event, f EventFetcher) (Request, bool, error) {
		return Request{}, false, nil
	})
	mustNotRun := HandlerFunc(func(ctx context.Context, req Request) (Response, error) {
		t.Fatal("handler must not run")
		return Response{}, nil
	})
	bot.RouteIn(id.RoomID("!r:e"), nonMatch, mustNotRun)
	bot.RouteIn(id.RoomID("!r:e"), nonMatch, mustNotRun)

	buf := captureSlog(t)

	bot.dispatch(context.Background(), &event.Event{ID: id.EventID("$nomatch"), RoomID: id.RoomID("!r:e")})

	if got := strings.Count(buf.String(), "no route matched"); got != 1 {
		t.Errorf("expected exactly one no-route log, got %d in %q", got, buf.String())
	}
	if !strings.Contains(buf.String(), "$nomatch") {
		t.Errorf("expected event ID in log, got %q", buf.String())
	}
}

func TestBotDispatchTriggerErrorDoesNotLogNoMatch(t *testing.T) {
	// A trigger error returns early; the no-match log line is for the
	// "walked every route, none fired" case, not for an aborted walk.
	sender := &fakeSender{}
	bot := newTestBot(sender, sender)
	bot.RouteIn(id.RoomID("!r:e"),
		TriggerFunc(func(ctx context.Context, evt *event.Event, f EventFetcher) (Request, bool, error) {
			return Request{}, false, errors.New("fetch failed")
		}),
		HandlerFunc(func(ctx context.Context, req Request) (Response, error) {
			return Response{}, nil
		}),
	)

	buf := captureSlog(t)

	bot.dispatch(context.Background(), &event.Event{ID: id.EventID("$err"), RoomID: id.RoomID("!r:e")})

	if strings.Contains(buf.String(), "no route matched") {
		t.Errorf("trigger-error path should not log no-route, got %q", buf.String())
	}
}

func TestBotDispatchPassesEventMetadataToHandler(t *testing.T) {
	sender := &fakeSender{}
	bot := newTestBot(sender, sender)

	var got Request
	bot.RouteIn(id.RoomID("!r:e"),
		TriggerFunc(func(ctx context.Context, evt *event.Event, f EventFetcher) (Request, bool, error) {
			return Request{
				EventID: evt.ID,
				RoomID:  evt.RoomID,
				Sender:  evt.Sender,
				Input:   "hi",
			}, true, nil
		}),
		HandlerFunc(func(ctx context.Context, req Request) (Response, error) {
			got = req
			return Response{}, nil
		}),
	)
	bot.dispatch(context.Background(), &event.Event{
		ID:     id.EventID("$evt"),
		RoomID: id.RoomID("!r:e"),
		Sender: id.UserID("@u:e"),
	})
	if got.EventID != id.EventID("$evt") || got.RoomID != id.RoomID("!r:e") || got.Sender != id.UserID("@u:e") {
		t.Errorf("got %+v", got)
	}
}

// TestBotRouteInIsolatesRooms pins the per-room dispatch contract: a route
// registered in room A must NOT fire for events in room B.
func TestBotRouteInIsolatesRooms(t *testing.T) {
	sender := &fakeSender{}
	bot := newTestBot(sender, sender)

	roomACalls := 0
	bot.RouteIn(id.RoomID("!a:e"),
		TriggerFunc(func(ctx context.Context, evt *event.Event, f EventFetcher) (Request, bool, error) {
			return Request{Input: "from-a"}, true, nil
		}),
		HandlerFunc(func(ctx context.Context, req Request) (Response, error) {
			roomACalls++
			return Response{}, nil
		}),
	)

	bot.dispatch(context.Background(), &event.Event{ID: id.EventID("$1"), RoomID: id.RoomID("!b:e")})

	if roomACalls != 0 {
		t.Errorf("route in room A fired for room B event: %d calls", roomACalls)
	}
}

// TestBotDispatchLogsWhenRoomHasNoRoutes pins the operator-visible warning:
// events in unconfigured rooms must produce a debug log so the operator can
// tell the bot is receiving them.
func TestBotDispatchLogsWhenRoomHasNoRoutes(t *testing.T) {
	sender := &fakeSender{}
	bot := newTestBot(sender, sender)
	// Register a route in a different room so routesByRoom is non-empty
	// overall; the event still goes to a room with no routes.
	bot.RouteIn(id.RoomID("!known:e"),
		TriggerFunc(func(ctx context.Context, evt *event.Event, f EventFetcher) (Request, bool, error) {
			return Request{}, false, nil
		}),
		HandlerFunc(func(ctx context.Context, req Request) (Response, error) { return Response{}, nil }),
	)

	buf := captureSlog(t)

	bot.dispatch(context.Background(), &event.Event{ID: id.EventID("$x"), RoomID: id.RoomID("!unknown:e")})

	if !strings.Contains(buf.String(), "no routes for room") {
		t.Errorf("expected 'no routes for room' log, got %q", buf.String())
	}
}
