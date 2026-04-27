# matrixbot

A Go library that runs a Matrix bot end-to-end: persistent state on
disk, operator-facing `init` / `login` / `logout` flows, a
`Bot.Run(ctx)` sync loop, an event dispatcher that walks per-room
trigger/handler routes, and an `e2ee` subpackage for cross-signing and
SAS verification. The host program supplies handlers (and credentials
for them); everything else lives here.

## What this is

Concretely, the package gives you:

- A `Bot` type that owns the `mautrix.Client`, accepts invites,
  dispatches incoming events to per-room routes, and (when configured)
  enables E2EE via mautrix's cryptohelper.
- The `Trigger` / `Handler` / `Request` / `Response` abstraction so
  hosts can write their own handlers without coupling to mautrix's
  event types beyond the `event.Event` already in `Request` callbacks.
- Built-in triggers: `MentionTrigger`, `CommandTrigger`,
  `ReactionTrigger` — see [Triggers](#triggers) below.
- A per-room routing config schema — `Config`, `RoomConfig`,
  `RouteConfig`. Both `RoomConfig` and `RouteConfig` carry an opaque
  `Extensions` (`json.RawMessage`) blob that the host program decodes
  on its own terms; matrixbot has no opinion on what's inside.
- On-disk state under a single data directory:
  - `config.json` — homeserver, bot user ID, operator user ID, rooms
    with per-room routes and extensions.
  - `session.json` — access token + device ID. Rotated by `RunLogin`.
  - `account.json` — cross-signing recovery key + crypto-store pickle
    key. Survives logout.
  - `crypto.db` (+ `-wal` / `-shm` sidecars) — mautrix's SQLite
    olm/megolm store, opened by the cryptohelper inside `Bot.Run`.
  All files are mode `0600`, the directory is `0700`, and writes are
  atomic (write-then-rename).
- An interactive `RunInit(ctx, dd, deps)` flow that prompts for
  homeserver / user ID / password / operator user ID, calls `/login`,
  mints a fresh cross-signing identity via the host-supplied
  `Bootstrapper`, and persists the resulting recovery key to
  `account.json`. The recovery key is never logged or printed —
  `account.json` is the only copy.
- `RunLogin(ctx, dd, deps)` to rotate the access token using the
  homeserver and user ID already in `config.json`.
- `RunLogout(ctx, dd, deps)` to invalidate the server-side session and
  wipe `session.json` and the crypto DB. Server-side failures don't
  block local cleanup, because that's the whole point of the command.
- A stdio `Prompter` (`NewStdioPrompter`) and an `EnvLookup` adapter
  (`EnvLookupFunc(os.Getenv)`) so tests can hand in a map and
  production can hand in `os.Getenv`.
- A `e2ee` subpackage with `Bootstrap` (cross-signing init/import) and
  `NewVerifier` (SAS verification). `Bot.Run` already wires both when
  the bot has crypto enabled; the subpackage is exported for hosts
  that need to call `Bootstrap` directly during `init`.

## What this isn't

- It does **not** define handlers. Implement `Handler` (or use
  `HandlerFunc`) in the host and pass them to `bot.RouteIn`.
- It does **not** decode `RoomConfig.Extensions`. Define your own
  per-room schema in the host and unmarshal that field yourself.
- It does **not** read host-specific env vars. The only env var
  matrixbot reads is `MATRIXBOT_DATA_DIR` (to relocate the data
  directory) and the four `MATRIX_*` variables `RunInit` consults
  (`MATRIX_HOMESERVER`, `MATRIX_USER_ID`, `MATRIX_PASSWORD`,
  `MATRIX_OPERATOR_USER_ID`). When set, each is used directly without
  prompting — handy for a non-interactive `init` from an existing
  `.envrc`, but it means `MATRIX_PASSWORD` in the environment silently
  bypasses the password prompt. Once `init` has run, runtime config
  never looks at env again.

## Data directory layout

`ResolveDataDir()` returns the absolute path:

- `MATRIXBOT_DATA_DIR` if set (resolved to absolute), otherwise
- `./.matrixbot` resolved against the current working directory.

The result is always absolute so that a later `cd` (systemd
`WorkingDirectory`, shell aliases, etc.) can't shift the device
identity.

| File           | Holds                                                                |
|----------------|----------------------------------------------------------------------|
| `config.json`  | `Config`: homeserver, bot user ID, operator user ID, rooms map       |
| `session.json` | `Session`: access token + device ID                                  |
| `account.json` | `Account`: cross-signing recovery key + crypto-store pickle key      |
| `crypto.db`    | mautrix SQLite olm/megolm store (+ `-wal` / `-shm` sidecars)         |

Note: matrixbot writes each JSON file via write-then-rename, so a
sibling `<name>.json.tmp` exists briefly during a save and may be left
behind if the process is killed mid-write. Backup tooling that copies
`.matrixbot/` should either tolerate or glob-ignore `*.tmp`.

`config.json` example:

```json
{
  "homeserver": "https://matrix.example.com",
  "user_id": "@bot:matrix.example.com",
  "operator_user_id": "@dave:matrix.example.com",
  "auto_join_rooms": ["!abc:matrix.example.com"],
  "rooms": {
    "!abc:matrix.example.com": {
      "extensions": { "your_host_specific_blob": "..." },
      "routes": [
        {"trigger": "mention", "handler": "llm"},
        {"trigger": "command", "prefix": "!tasks", "handler": "list", "extensions": {"page_size": 20}},
        {"trigger": "reaction", "emoji": "📝", "handler": "create"}
      ]
    }
  }
}
```

`Trigger`, `Handler`, `Prefix`, and `Emoji` are the trigger-shape
fields matrixbot reads from `RouteConfig` to decide which events the
route fires on. Everything else — credentials, system prompts, page
sizes, per-handler tuning — goes inside an `extensions` block and is
decoded by your host program. The room's `extensions` block holds
config shared by every route in that room (typically credentials);
each route's own `extensions` block holds knobs specific to that one
route.

## Bot, BotConfig, and the runtime loop

```go
bot, err := matrixbot.NewBot(matrixbot.BotConfig{
    Homeserver:     mbCfg.Homeserver,
    UserID:         mbCfg.UserID,
    AccessToken:    sess.AccessToken,
    DeviceID:       sess.DeviceID,
    OperatorUserID: mbCfg.OperatorUserID,
    PickleKey:      acct.PickleKey,
    CryptoDB:       dd.CryptoDBPath(),
    RecoveryKey:    acct.RecoveryKey,
    AutoJoinRooms:  toRoomIDs(mbCfg.AutoJoinRooms),
    Logger:         nil, // optional *zerolog.Logger forwarding mautrix's internal logs
})
if err != nil { /* ... */ }

bot.RouteIn(roomID, matrixbot.MentionTrigger{BotUserID: id.UserID(mbCfg.UserID)}, myHandler)
bot.RouteIn(roomID, matrixbot.CommandTrigger{Prefix: "!tasks", BotUserID: id.UserID(mbCfg.UserID)}, listHandler)

if err := bot.Run(ctx); err != nil { /* ... */ }
```

`BotConfig` is intentionally narrow: it carries only the credentials
and runtime knobs `NewBot` needs. Hosts that already keep a richer
config (LLM keys, etc.) build a `BotConfig` from whatever subset is
relevant. The `AutoJoinRooms` slice is fully consumed inside `NewBot`;
there is no post-construction map to mutate.

`Run(ctx)`:

1. Initialises the cryptohelper if `PickleKey` is non-empty (otherwise
   logs a warning and runs unencrypted — encrypted rooms will not
   deliver readable events).
2. Imports the existing cross-signing identity via `e2ee.Bootstrap`
   when `RecoveryKey` is non-empty. Empty recovery key is a supported
   opt-out.
3. Wires `e2ee.NewVerifier` when `OperatorUserID` is set, so the
   operator's "Verify Session" tap in Element completes via SAS.
4. Subscribes to `m.room.message`, `m.reaction`, and member events,
   then blocks on `mautrix.Client.SyncWithContext` until `ctx` is
   cancelled. mautrix handles reconnection internally.

`Bot` drops both the pickle key and recovery key from its in-memory
state once they have been handed off to mautrix — they never leak
through subsequent log lines or stack dumps.

`Bot.Send(ctx, roomID, markdown)` is the public seam for posting
messages that aren't replies to an incoming event (notifiers,
schedulers, periodic summaries): it renders Markdown to HTML and
returns the homeserver error so the caller decides whether a delivery
failure matters.

### Logging

matrixbot writes its own diagnostic events through `log/slog`, so the
host's `slog.SetDefault(...)` (handler, level, output) controls
matrixbot's logs. `BotConfig.Logger` is a separate channel: it carries
mautrix's underlying zerolog stream (crypto, sync, HTTP). Pass a
non-nil `*zerolog.Logger` to route mautrix's logs through whatever
writer/level you've already wired up; nil falls back to a no-op so
tests stay quiet.

## Routes, triggers, and handlers

A `Route` is a `(Trigger, Handler)` pair scoped to a single room.
Routes are stored per-room and evaluated in registration order; the
first trigger that returns `(req, true, nil)` wins, the handler runs,
and any non-empty `Response.Reply` is sent as a Markdown-rendered
message. Subsequent routes are not tried.

```go
type Trigger interface {
    Apply(ctx context.Context, evt *event.Event, fetcher EventFetcher) (Request, bool, error)
}

type Handler interface {
    Handle(ctx context.Context, req Request) (Response, error)
}
```

The semantic contracts:

- A trigger that does not match returns `(Request{}, false, nil)`.
- A trigger that errors (e.g. its `EventFetcher` failed) returns
  `(_, false, err)`. Dispatch logs the error and stops processing the
  event — later routes are NOT tried, because routing past an
  unexpected fetcher error would silently mask it.
- A handler error is logged and surfaced to the room as
  `"Sorry, I hit an error: …"`. Empty `Response.Reply` stays quiet.

`TriggerFunc` and `HandlerFunc` adapt plain functions to the
interfaces.

### Triggers

| Trigger            | Fires when                                                                          | `Request.Input`                          |
|--------------------|-------------------------------------------------------------------------------------|------------------------------------------|
| `MentionTrigger`   | message body or `m.mentions` references the full bot user ID, sender ≠ bot         | message body with the mention stripped   |
| `CommandTrigger`   | trimmed body starts with `Prefix` followed by end-of-string or whitespace          | trimmed remainder after the prefix       |
| `ReactionTrigger`  | reaction whose emoji equals `Emoji`, sender ≠ bot, parent fetched via `EventFetcher` | parent message body                      |

`MentionTrigger` deliberately ignores the localpart (`@name` without
`:server`) because that pattern false-matches on quotes and on user
IDs like `@name-admin`.

`CommandTrigger`'s end-of-string-or-whitespace rule is the whole point
of having a custom matcher rather than `strings.HasPrefix` — without
it, `"!tasksearch"` would fire a `"!tasks"` route.

### Writing a custom handler

```go
type echoHandler struct{}

func (echoHandler) Handle(ctx context.Context, req matrixbot.Request) (matrixbot.Response, error) {
    return matrixbot.Response{Reply: "you said: " + req.Input}, nil
}

bot.RouteIn(roomID, matrixbot.CommandTrigger{Prefix: "!echo"}, echoHandler{})
```

`Request` carries `EventID`, `RoomID`, `Sender`, and the
trigger-extracted `Input`. Handlers that need richer event context
(unparsed content, formatted body, etc.) should write a custom
`Trigger` that pulls those fields onto `Request.Input` (or extend
`Request` upstream — the struct lives in this package).

## The e2ee subpackage

`github.com/shishberg/matrixbot/e2ee` exposes:

- `Bootstrap(ctx, mach, password, recoveryKey)` — generates or imports
  cross-signing keys. With `recoveryKey` set, it imports the existing
  identity from SSSS (the steady-state path on every restart). With
  `password` set and `recoveryKey` empty, it mints a fresh identity,
  uploads it via UIA, and returns the freshly-generated recovery key.
  Both empty is a no-op.
- `NewVerifier(client, operatorUserID)` — an SAS responder that
  auto-accepts requests from `operatorUserID`, dismisses everything
  else, and logs the emoji/decimal SAS for the operator to compare.
  Empty operator returns nil so callers can wire it unconditionally.

`Bot.Run` already calls both during steady-state startup; the
subpackage is exported because `RunInit`'s `Bootstrapper` callback
needs to call `Bootstrap` itself with the freshly-prompted password to
mint the first-run recovery key.

`Bootstrap`'s return contract has one footgun worth flagging: on the
first-run path it may return a non-empty recovery key alongside a
non-nil error. mautrix mints the SSSS key first and only later does
the UIA-gated upload, so a 401 leaves the caller with a
half-bootstrapped account whose only re-entry point is that recovery
key. Callers MUST persist the recovery key whenever it is non-empty,
even when err is also non-nil.

## Build tags: `goolm` vs default (libolm)

mautrix's crypto stack has two olm backends:

- **`-tags goolm`** — pure Go, no system dependency. Recommended for
  most hosts.
- **default** — cgo wrapper around `libolm`. Needs the C headers at
  build time: `apt install libolm-dev`, `dnf install libolm-devel`,
  or `brew install libolm`.

```sh
go build -tags goolm
go test -tags goolm ./...
go vet -tags goolm ./...
```

The current development environment for this repository does not have
`libolm` installed, so CI / local test runs use the `goolm` tag. Both
build paths should work; if you're on a host with libolm, the default
(cgo) path skips the pure-Go olm port and links against the system
library instead.

## Type seams

The CLI flows take dependency structs so the host program can plug in
real implementations and tests can plug in fakes:

| Seam                                                          | Used by    | Real implementation                                                                                |
|---------------------------------------------------------------|------------|----------------------------------------------------------------------------------------------------|
| `LoginClientFactory func(homeserverURL string) (LoginClient, error)`                | init+login | Build a `mautrix.NewClient` and return it; only `Login(ctx, *ReqLogin)` is called.                 |
| `LogoutClientFactory func(homeserverURL, accessToken string) (LogoutClient, error)` | logout     | Build a `mautrix.NewClient` with the token set; only `Logout(ctx)` is called.                      |
| `Bootstrapper`                                                | init       | Open `crypto.db` with the pickle key, run mautrix `cryptohelper.Init` then `e2ee.Bootstrap`, return the recovery key. |
| `Prompter`                                                    | init+login | `NewStdioPrompter(os.Stdin, os.Stdout)`.                                                           |
| `EnvLookup`                                                   | init       | `EnvLookupFunc(os.Getenv)`.                                                                        |

The `Bootstrapper` contract has the same footgun as `e2ee.Bootstrap`
itself: implementations MUST return whatever recovery key got minted,
even when they also return a non-nil error. `RunInit` is the consumer
of this contract — it persists whatever recovery key the
`Bootstrapper` returns before propagating the bootstrap error.

`InitDeps`, `LoginDeps`, and `LogoutDeps` each carry a `ProgramName`
field. Operator-facing messages substitute it in (e.g. `"run 'mybot
init' first"`); empty falls back to `"the bot"`. The `Initialized X.
Run 'Y' to start.` line `RunInit` prints on success is where most
operators will first see this name, so set it in your host program.

## How a host program wires this up

Sketch — a host CLI dispatches to `init` / `login` / `logout` /
`<run>`. The `run` path loads the persisted state, builds a `BotConfig`,
constructs a `Bot`, registers routes, and blocks on `Bot.Run`.

```go
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/shishberg/matrixbot"
	"github.com/shishberg/matrixbot/e2ee"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/crypto/cryptohelper"
	"maunium.net/go/mautrix/id"
)

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		sc := make(chan os.Signal, 1)
		signal.Notify(sc, syscall.SIGINT, syscall.SIGTERM)
		<-sc
		cancel()
	}()

	dd, err := matrixbot.ResolveDataDir()
	if err != nil {
		fail(err)
	}

	prompter, err := matrixbot.NewStdioPrompter(os.Stdin, os.Stdout)
	if err != nil {
		fail(err)
	}

	loginFactory := func(hs string) (matrixbot.LoginClient, error) {
		return mautrix.NewClient(hs, "", "")
	}
	logoutFactory := func(hs, token string) (matrixbot.LogoutClient, error) {
		c, err := mautrix.NewClient(hs, "", token)
		if err != nil {
			return nil, err
		}
		return c, nil
	}

	// Declaring bootstrap with the matrixbot.Bootstrapper type means the
	// example tracks whatever signature the package currently exposes
	// rather than carrying a stale inline copy.
	var bootstrap matrixbot.Bootstrapper = func(
		ctx context.Context,
		dd matrixbot.DataDir,
		accessToken, deviceID, userID, homeserver, password, pickleKey string,
	) (string, error) {
		client, err := mautrix.NewClient(homeserver, id.UserID(userID), accessToken)
		if err != nil {
			return "", err
		}
		client.DeviceID = id.DeviceID(deviceID)
		helper, err := cryptohelper.NewCryptoHelper(client, []byte(pickleKey), dd.CryptoDBPath())
		if err != nil {
			return "", err
		}
		if err := helper.Init(ctx); err != nil {
			return "", err
		}
		defer helper.Close()
		client.Crypto = helper
		return e2ee.Bootstrap(ctx, helper.Machine(), password, "")
	}

	cmd := argOrEmpty(os.Args, 1)
	switch cmd {
	case "init":
		err = matrixbot.RunInit(ctx, dd, matrixbot.InitDeps{
			LoginFactory: loginFactory,
			Bootstrap:    bootstrap,
			Env:          matrixbot.EnvLookupFunc(os.Getenv),
			Prompter:     prompter,
			Stdout:       os.Stdout,
			ProgramName:  "mybot",
		})
	case "login":
		err = matrixbot.RunLogin(ctx, dd, matrixbot.LoginDeps{
			LoginFactory: loginFactory,
			Prompter:     prompter,
			Stdout:       os.Stdout,
			ProgramName:  "mybot",
		})
	case "logout":
		err = matrixbot.RunLogout(ctx, dd, matrixbot.LogoutDeps{
			LogoutFactory: logoutFactory,
			Stdout:        os.Stdout,
			ProgramName:   "mybot",
		})
	default:
		err = run(ctx, dd)
	}
	if err != nil {
		fail(err)
	}
}

func run(ctx context.Context, dd matrixbot.DataDir) error {
	cfg, err := matrixbot.LoadConfig(dd)
	if err != nil {
		return err
	}
	sess, err := matrixbot.LoadSession(dd)
	if err != nil {
		return err
	}
	acct, err := matrixbot.LoadAccount(dd)
	if err != nil {
		return err
	}

	autoJoin := make([]id.RoomID, 0, len(cfg.AutoJoinRooms))
	for _, r := range cfg.AutoJoinRooms {
		autoJoin = append(autoJoin, id.RoomID(r))
	}
	bot, err := matrixbot.NewBot(matrixbot.BotConfig{
		Homeserver:     cfg.Homeserver,
		UserID:         id.UserID(cfg.UserID),
		AccessToken:    sess.AccessToken,
		DeviceID:       id.DeviceID(sess.DeviceID),
		OperatorUserID: id.UserID(cfg.OperatorUserID),
		PickleKey:      acct.PickleKey,
		CryptoDB:       dd.CryptoDBPath(),
		RecoveryKey:    acct.RecoveryKey,
		AutoJoinRooms:  autoJoin,
	})
	if err != nil {
		return err
	}

	for roomStr, room := range cfg.Rooms {
		roomID := id.RoomID(roomStr)
		for _, route := range room.Routes {
			trigger, handler, err := buildRoute(route, cfg.UserID) // host-supplied
			if err != nil {
				return err
			}
			bot.RouteIn(roomID, trigger, handler)
		}
	}

	return bot.Run(ctx)
}

func argOrEmpty(args []string, i int) string {
	if i >= len(args) {
		return ""
	}
	return args[i]
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
```

`buildRoute` is the host's responsibility: it inspects `route.Trigger`
and `route.Handler` strings (and decodes `room.Extensions` for
credentials) and returns the matching `matrixbot.Trigger` /
`matrixbot.Handler` pair.

## Versioning

This module is pre-1.0. Expect breaking changes to `Config`,
`RoomConfig`, `BotConfig`, and the `*Deps` structs. Pin a specific
version in your host's `go.mod` until things settle.

## License

TBD.
