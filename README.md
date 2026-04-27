# matrixbot

A Go library that handles the persistent state and operator-facing CLI
flows for a Matrix bot. It owns a single data directory on disk, drives
the `init` / `login` / `logout` flows you'd expect a bot to expose, and
exposes typed seams (`InitDeps`, `LoginDeps`, `LogoutDeps`,
`Bootstrapper`, `LoginClientFactory`, `LogoutClientFactory`) so the host
program wires in the real `mautrix.Client` and crypto helper while tests
inject fakes.

## What this is

Concretely, the package gives you:

- A per-room routing config schema — `Config`, `RoomConfig`,
  `RouteConfig`. Each room carries its own opaque `Extensions`
  (`json.RawMessage`) blob that the host program decodes on its own
  terms; matrixbot has no opinion on what's inside.
- On-disk state under a single data directory:
  - `config.json` — homeserver, bot user ID, operator user ID, rooms
    with per-room routes and extensions.
  - `session.json` — access token + device ID. Rotated by `RunLogin`.
  - `account.json` — cross-signing recovery key + crypto-store pickle
    key. Survives logout.
  - `crypto.db` (+ `-wal` / `-shm` sidecars) — the host program's
    SQLite olm/megolm store, written by mautrix's cryptohelper.
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

## What this isn't

- It does **not** run the bot's sync loop, dispatch handlers, or define
  triggers. Those are the host program's job. matrixbot only manages
  the credentials and per-room routing schema the host reads at
  startup.
- It does **not** depend on libolm or open `crypto.db` itself. The
  cryptohelper (which does pull in cgo or `goolm`) lives in the host;
  matrixbot just hands the host a pickle key and a path and stores
  whatever recovery key comes back.
- It does **not** decode `RoomConfig.Extensions`. Define your own
  per-room schema in the host and unmarshal that field yourself.
- It does **not** read host-specific env vars. The only env var
  matrixbot reads is `MATRIXBOT_DATA_DIR` (to relocate the data
  directory) and the four `MATRIX_*` variables that seed prompt
  defaults during `RunInit` (`MATRIX_HOMESERVER`, `MATRIX_USER_ID`,
  `MATRIX_PASSWORD`, `MATRIX_OPERATOR_USER_ID`). Once `init` has run,
  runtime config never looks at env again.

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
| `crypto.db`    | Host-managed SQLite olm/megolm store (+ `-wal` / `-shm` sidecars)    |

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
        {"trigger": "command", "prefix": "!tasks", "handler": "list", "limit": 20},
        {"trigger": "reaction", "emoji": "📝", "handler": "create"}
      ]
    }
  }
}
```

`Trigger`, `Handler`, `Prefix`, `Emoji`, and `Limit` are the
trigger/handler-shape fields matrixbot exposes on `RouteConfig`.
Anything else — credentials, system prompts, per-handler tuning — goes
inside `extensions` and is decoded by your host program.

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

The `Bootstrapper` contract has one footgun worth flagging: if the
recovery key is non-empty, **persist it even when the error is
non-nil**. A half-bootstrapped account can have its SSSS key minted
before a UIA-gated upload fails; the recovery key returned in that case
is the operator's only way back into the account. `RunInit` already
follows this rule when calling the host's `Bootstrapper`.

`InitDeps`, `LoginDeps`, and `LogoutDeps` each carry a `ProgramName`
field. Operator-facing messages substitute it in (e.g. `"run 'mybot
init' first"`); empty falls back to `"the bot"`.

## How a host program wires this up

Sketch — a host CLI dispatches to `init` / `login` / `logout` /
`<run>` and reads the persisted state for its sync loop:

```go
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/shishberg/matrixbot"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/crypto/cryptohelper"
)

func main() {
	ctx := context.Background()

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

	bootstrap := func(ctx context.Context, dd matrixbot.DataDir,
		accessToken, deviceID, userID, homeserver, password, pickleKey string,
	) (string, error) {
		// open crypto.db at dd.CryptoDBPath() with pickleKey, run
		// cryptohelper.Init + e2ee.Bootstrap, return the recovery key.
		_ = cryptohelper.CryptoHelper{} // host's job
		return "", nil
	}

	switch cmd, _ := argOrEmpty(os.Args, 1), 0; cmd {
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
		// Run the bot: load state, build a real mautrix.Client, sync loop.
		cfg, err := matrixbot.LoadConfig(dd)
		if err != nil {
			fail(err)
		}
		sess, err := matrixbot.LoadSession(dd)
		if err != nil {
			fail(err)
		}
		acct, err := matrixbot.LoadAccount(dd)
		if err != nil {
			fail(err)
		}
		_ = cfg
		_ = sess
		_ = acct
		// ...
	}
	if err != nil {
		fail(err)
	}
}

func argOrEmpty(args []string, i int) (string, bool) {
	if i >= len(args) {
		return "", false
	}
	return args[i], true
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
```

## Versioning

This module is pre-1.0. Expect breaking changes to `Config`,
`RoomConfig`, and the `*Deps` structs. Pin a specific version in your
host's `go.mod` until things settle.

## License

TBD.
