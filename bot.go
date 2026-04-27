package matrixbot

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/rs/zerolog"
	"github.com/shishberg/matrixbot/e2ee"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/crypto/cryptohelper"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/format"
	"maunium.net/go/mautrix/id"
)

// matrixSender abstracts the slice of *mautrix.Client we send through, so
// the bot is unit-testable without a homeserver.
type matrixSender interface {
	SendMessageEvent(ctx context.Context, roomID id.RoomID, eventType event.Type, contentJSON interface{}, extra ...mautrix.ReqSendEvent) (*mautrix.RespSendEvent, error)
}

// roomJoiner is the slice of *mautrix.Client used to accept invites,
// extracted so the bot is unit-testable without a homeserver.
type roomJoiner interface {
	JoinRoomByID(ctx context.Context, roomID id.RoomID) (*mautrix.RespJoinRoom, error)
}

// Route is a (Trigger, Handler) pair. Routes are evaluated in registration
// order; first match wins.
type Route struct {
	Trigger Trigger
	Handler Handler
}

// BotConfig is the slice of credentials and runtime knobs NewBot needs.
// The host program builds this from whatever config it keeps on disk.
type BotConfig struct {
	Homeserver     string
	UserID         id.UserID
	AccessToken    string
	DeviceID       id.DeviceID
	OperatorUserID id.UserID

	// PickleKey + CryptoDB enable E2EE; an empty PickleKey disables it
	// (encrypted rooms will not deliver readable events).
	PickleKey string
	CryptoDB  string
	// RecoveryKey, when non-empty, is imported on every Run start to
	// restore cross-signing identity. Empty disables cross-signing.
	RecoveryKey string

	// AutoJoinRooms is the set of room IDs whose invites the bot accepts
	// without further checks. Operator invites (from OperatorUserID) are
	// always accepted regardless.
	AutoJoinRooms []id.RoomID

	// Logger, when non-nil, is the zerolog logger plumbed into the
	// underlying mautrix client. Nil falls back to a no-op logger.
	Logger *zerolog.Logger
}

// Bot owns the Matrix client and a per-room route table. Behaviour-specific
// state (LLM history, task tracker handles, etc.) lives in the handlers, not
// here.
type Bot struct {
	client       *mautrix.Client
	sender       matrixSender
	fetcher      EventFetcher
	joiner       roomJoiner
	botUserID    id.UserID
	routesByRoom map[id.RoomID][]Route

	// autoJoinRooms is the set of room IDs whose invites the bot accepts
	// without further checks. Empty means only operator invites are
	// accepted (and only if operatorUserID is set).
	autoJoinRooms map[id.RoomID]bool

	// pickleKey, when non-empty, opts the bot into Matrix E2EE. The crypto
	// helper is initialised lazily in Run so that the ctx is available.
	pickleKey string
	cryptoDB  string

	// recoveryKey is the base58 SSSS recovery key minted by `init`. On every
	// restart Run uses it to import the existing cross-signing identity from
	// the homeserver — no first-run path lives here.
	recoveryKey string
	// operatorUserID, when non-empty, is the only user allowed to verify
	// the bot's device via SAS.
	operatorUserID id.UserID
}

// NewBot constructs the Matrix client. Routes are added with RouteIn() before
// calling Run().
func NewBot(cfg BotConfig) (*Bot, error) {
	client, err := mautrix.NewClient(cfg.Homeserver, cfg.UserID, cfg.AccessToken)
	if err != nil {
		return nil, err
	}
	client.DeviceID = cfg.DeviceID
	// mautrix defaults to a no-op logger, hiding errors from the SAS state
	// machine and other crypto code. Hosts pass a shared zerolog logger via
	// BotConfig.Logger so mautrix's logs go through the same writer and level
	// filter as the host's slog calls. Setting client.Log here also covers
	// the CryptoHelper, which derives its sub-logger from client.Log at
	// construction time. A nil logger falls back to Nop so tests don't write
	// to stderr.
	if cfg.Logger != nil {
		client.Log = *cfg.Logger
	} else {
		client.Log = zerolog.Nop()
	}

	autoJoin := map[id.RoomID]bool{}
	for _, r := range cfg.AutoJoinRooms {
		autoJoin[r] = true
	}

	return &Bot{
		client:         client,
		sender:         client,
		fetcher:        client,
		joiner:         client,
		botUserID:      cfg.UserID,
		routesByRoom:   map[id.RoomID][]Route{},
		autoJoinRooms:  autoJoin,
		pickleKey:      cfg.PickleKey,
		cryptoDB:       cfg.CryptoDB,
		recoveryKey:    cfg.RecoveryKey,
		operatorUserID: cfg.OperatorUserID,
	}, nil
}

// RouteIn registers a (trigger, handler) pair scoped to a single room.
// Routes within a room are evaluated in registration order; the first
// match wins. Events for rooms with no routes are dropped early.
func (b *Bot) RouteIn(roomID id.RoomID, t Trigger, h Handler) {
	if b.routesByRoom == nil {
		b.routesByRoom = map[id.RoomID][]Route{}
	}
	b.routesByRoom[roomID] = append(b.routesByRoom[roomID], Route{Trigger: t, Handler: h})
}

// Run subscribes to message and reaction events and blocks until ctx is
// cancelled. mautrix handles reconnection internally. We subscribe to both
// event types unconditionally — the per-route Trigger filters which events
// each route actually cares about.
func (b *Bot) Run(ctx context.Context) error {
	syncer, ok := b.client.Syncer.(*mautrix.DefaultSyncer)
	if !ok {
		return fmt.Errorf("unexpected syncer type %T", b.client.Syncer)
	}

	// Setting client.Crypto makes the existing OnEventType hooks fire on
	// decrypted m.room.message events and makes SendMessageEvent
	// transparently encrypt for encrypted rooms. With no pickle key we skip
	// this entirely; encrypted rooms simply won't deliver readable events.
	if b.pickleKey == "" {
		slog.Warn("matrixbot: running without E2EE (no pickle key); messages in encrypted rooms will be silently dropped")
	} else {
		helper, err := cryptohelper.NewCryptoHelper(b.client, []byte(b.pickleKey), b.cryptoDB)
		if err != nil {
			return fmt.Errorf("creating crypto helper: %w", err)
		}
		if err := helper.Init(ctx); err != nil {
			return fmt.Errorf("initialising crypto helper: %w", err)
		}
		b.client.Crypto = helper
		b.clearPickleKey()
		slog.Info("matrixbot: crypto enabled", "store", b.cryptoDB)
		defer func() {
			if cerr := helper.Close(); cerr != nil {
				slog.Warn("matrixbot: closing crypto helper", "err", cerr)
			}
		}()

		mach := helper.Machine()
		// `init` already minted and persisted the cross-signing identity; on
		// every restart we import it via the recovery key. Empty recoveryKey
		// is allowed (the operator opted out) — it disables cross-signing
		// without aborting the bot.
		if _, err := e2ee.Bootstrap(ctx, mach, "", b.recoveryKey); err != nil {
			return fmt.Errorf("cross-signing: %w", err)
		}
		b.clearRecoveryKey()

		if verifier := e2ee.NewVerifier(b.client, b.operatorUserID); verifier != nil {
			if err := verifier.Init(ctx, mach); err != nil {
				return fmt.Errorf("verifier: %w", err)
			}
		}
	}

	syncer.OnEvent(func(_ context.Context, evt *event.Event) {
		slog.Debug("matrixbot: rx", "type", evt.Type.Type, "class", evt.Type.Class, "room", evt.RoomID, "sender", evt.Sender, "id", evt.ID)
	})
	syncer.OnEventType(event.StateMember, b.handleInvite)
	if len(b.routesByRoom) == 0 {
		slog.Warn("matrixbot: no routes registered; the bot will sync but ignore every event")
	} else {
		syncer.OnEventType(event.EventMessage, b.dispatch)
		syncer.OnEventType(event.EventReaction, b.dispatch)
	}
	return b.client.SyncWithContext(ctx)
}

// dispatch walks the routes for evt's room in order and runs the first one
// whose Trigger matches. A Trigger.Apply error is a hard fail for the
// event — later routes are NOT tried, because routing past an unexpected
// fetcher error would mask it. Handler errors are logged and surfaced to
// the user as a "Sorry, …" message.
func (b *Bot) dispatch(ctx context.Context, evt *event.Event) {
	slog.Debug("matrixbot: event", "id", evt.ID, "type", evt.Type.Type, "sender", evt.Sender, "room", evt.RoomID)
	routes, ok := b.routesByRoom[evt.RoomID]
	if !ok || len(routes) == 0 {
		slog.Debug("matrixbot: no routes for room", "event", evt.ID, "room", evt.RoomID)
		return
	}
	for i, r := range routes {
		req, matched, err := r.Trigger.Apply(ctx, evt, b.fetcher)
		if err != nil {
			slog.Warn("matrixbot: trigger error", "event", evt.ID, "err", err)
			return
		}
		if !matched {
			continue
		}
		slog.Debug("matrixbot: event matched route", "event", evt.ID, "route", i, "handler", fmt.Sprintf("%T", r.Handler))
		resp, err := r.Handler.Handle(ctx, req)
		if err != nil {
			slog.Warn("matrixbot: handler error", "event", evt.ID, "err", err)
			b.send(ctx, evt.RoomID, "Sorry, I hit an error: "+err.Error())
			return
		}
		if resp.Reply != "" {
			slog.Debug("matrixbot: replying", "event", evt.ID, "chars", len(resp.Reply))
			b.send(ctx, evt.RoomID, resp.Reply)
		}
		return
	}
	slog.Debug("matrixbot: no route matched", "event", evt.ID)
}

// handleInvite auto-joins invites to any room in autoJoinRooms, plus any
// invite from the configured operator (so Element's "Verify User" DM, which
// creates a fresh room, can complete SAS). Both paths are deliberately narrow:
// no general join-any policy.
func (b *Bot) handleInvite(ctx context.Context, evt *event.Event) {
	if evt.GetStateKey() != string(b.botUserID) {
		return
	}
	if evt.Content.AsMember().Membership != event.MembershipInvite {
		return
	}
	autoJoin := b.autoJoinRooms[evt.RoomID]
	// Operator already holds device-verification trust; extending that to
	// "may invite the bot into a verification room" stays within the same
	// person's scope.
	operatorInvite := b.operatorUserID != "" && evt.Sender == b.operatorUserID
	if !autoJoin && !operatorInvite {
		slog.Info("matrixbot: ignoring invite", "room", evt.RoomID, "sender", evt.Sender)
		return
	}
	slog.Info("matrixbot: invited, joining", "room", evt.RoomID, "sender", evt.Sender)
	if _, err := b.joiner.JoinRoomByID(ctx, evt.RoomID); err != nil {
		slog.Warn("matrixbot: join failed", "room", evt.RoomID, "err", err)
		return
	}
	slog.Info("matrixbot: joined", "room", evt.RoomID)
}

// clearPickleKey drops the in-memory copy of the pickle key once it has been
// handed to the cryptohelper. The helper holds the working copy from then on;
// keeping a second copy on Bot only widens the leak surface (stack dumps,
// future log lines that accidentally include the receiver).
func (b *Bot) clearPickleKey() {
	b.pickleKey = ""
}

// clearRecoveryKey drops the in-memory copy of the cross-signing recovery key
// once mautrix has imported the identity. Same reasoning as clearPickleKey.
func (b *Bot) clearRecoveryKey() {
	b.recoveryKey = ""
}

// Send renders markdown to HTML and posts it to roomID, returning whatever
// error the homeserver call produced. It is the seam for consumers that
// post unsolicited messages (notifiers, schedulers, anything posting
// independent of the incoming-event dispatch loop) and want to decide
// themselves whether a delivery failure matters. The internal dispatch
// path uses the unexported send wrapper, which logs and swallows.
func (b *Bot) Send(ctx context.Context, roomID id.RoomID, markdown string) error {
	content := format.RenderMarkdown(markdown, true, false)
	content.MsgType = event.MsgText
	content.Format = event.FormatHTML
	_, err := b.sender.SendMessageEvent(ctx, roomID, event.EventMessage, &content)
	return err
}

func (b *Bot) send(ctx context.Context, roomID id.RoomID, markdown string) {
	if err := b.Send(ctx, roomID, markdown); err != nil {
		slog.Warn("matrixbot: send failed", "err", err)
		return
	}
	slog.Debug("matrixbot: sent reply", "chars", len(markdown), "room", roomID)
}
