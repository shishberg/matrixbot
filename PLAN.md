# Matrixbot Abstraction Cleanup Plan

This plan covers the structural findings from the matrixbot review, with
`../bilby` treated as the primary consumer. Prefer changing matrixbot and
bilby together over preserving compatibility seams that no longer fit.

Use strict TDD for each item: write the failing test first, run it to see
the failure, make the smallest production change, then run the relevant
matrixbot and bilby test suites.

## 1. Move Init Crypto Bootstrap Into Matrixbot

**Finding:** `RunInit` owns login, persistence, pickle key generation, and
half-bootstrap recovery handling, but the host still has to implement the
real `Bootstrapper` by recreating a mautrix client, setting the device ID,
opening cryptohelper, initializing it, closing it, and calling
`e2ee.Bootstrap`.

**Current leak:**

- matrixbot: `init.go` exposes `Bootstrapper` as a raw multi-string callback.
- bilby: `init.go` has `realBootstrap`, which is generic matrixbot behavior.
- README repeats the same bootstrap code and the same recovery-key footgun.

**Proposed fix:**

- Add a matrixbot-owned production bootstrap implementation.
- Prefer removing `Bootstrapper` from `InitDeps` entirely if tests can inject
  lower-level seams cleanly. If not, keep an unexported/default path and a
  test-only narrow seam, but do not require hosts to provide real crypto
  bootstrap code.
- Keep `RunInit` as the place that persists `account.json`, including the
  rule that a non-empty recovery key must be saved even when bootstrap
  returns an error.

**Suggested shape:**

- Add `DefaultBootstrapper` or an unexported `bootstrapCrossSigning` in
  matrixbot that does the current bilby `realBootstrap` work.
- Change `InitDeps` so production callers only provide `LoginFactory`, `Env`,
  `Prompter`, `Stdout`, and `ProgramName`.
- For tests, inject a small seam that returns `(recoveryKey, error)` without
  asking callers to know access token, device ID, homeserver, and pickle key
  ordering.

**Matrixbot tests to write first:**

- `RunInit` uses matrixbot's default bootstrap path when no test seam is set.
- A fake bootstrap returning `(recoveryKey, err)` still causes `account.json`
  to be written before the error is returned.
- Bootstrap close errors remain non-fatal if the existing behavior is kept.

**Bilby sync:**

- Delete `realBootstrap` from `../bilby/init.go`.
- Remove bilby's direct imports of `matrixbot/e2ee`, `mautrix`,
  `cryptohelper`, and `mautrix/id` from `init.go` if no longer needed.
- Update `runInit` to pass the simplified `matrixbot.InitDeps`.

**Docs:**

- Replace the README bootstrap sample with the simpler host wiring.
- Remove the host-facing warning that `Bootstrapper` implementations must
  return the recovery key on error, because hosts should no longer implement
  that contract.

## 2. Let RouteConfig Build Built-In Triggers

**Finding:** matrixbot defines `RouteConfig` and the built-in triggers, but
bilby has to translate `"mention"`, `"command"`, and `"reaction"` into
matrixbot trigger structs and remember to pass `BotUserID`.

**Current leak:**

- matrixbot: `RouteConfig` carries `Trigger`, `Prefix`, and `Emoji`.
- bilby: `routes.go` has `buildTrigger`, a pure matrixbot mapping.

**Proposed fix:**

- Add a matrixbot helper that turns a `RouteConfig` into a built-in
  `Trigger`.
- Keep host-specific handler construction in bilby.

**Suggested shape:**

- Add `func (r RouteConfig) BuildTrigger(botUserID id.UserID) (Trigger, error)`.
- Validate built-in trigger config there:
  - `mention` needs no extra field.
  - `command` requires non-empty `Prefix`.
  - `reaction` requires non-empty `Emoji`.
  - unknown trigger names return an error naming the trigger.
- Consider exported trigger-name constants if they make tests/readability
  better, but do not over-abstract.

**Matrixbot tests to write first:**

- Mention route builds `MentionTrigger` with `BotUserID`.
- Command route requires `Prefix` and builds `CommandTrigger`.
- Reaction route requires `Emoji` and builds `ReactionTrigger`.
- Unknown trigger returns an actionable error.

**Bilby sync:**

- Delete `buildTrigger` from `../bilby/routes.go`.
- Replace `buildTrigger(route, botCfg.UserID)` in `../bilby/main.go` with
  `route.BuildTrigger(botCfg.UserID)`.
- Delete bilby's `TestBuildTrigger*` tests or move equivalent coverage to
  matrixbot.

## 3. Add Config Schema Validation With Explicit Legacy Allowances

**Finding:** `LoadConfig` currently ignores unknown JSON fields by design.
That preserved a known legacy `limit` route field, but it also hides typos in
operator-edited config such as `prefx`, `extension`, or `emoji_name`.

**Current leak:**

- matrixbot has targeted legacy detectors for top-level `room_id`,
  `target_room_id`, top-level `extensions`, and per-route `config`.
- unknown fields are otherwise ignored because `json.Unmarshal` is
  permissive.
- `TestRouteConfigIgnoresUnknownJSONFields` pins that permissiveness.

**Proposed fix:**

- Replace broad permissiveness with an explicit validation pass over raw JSON.
- Allow only known current fields and known legacy fields that have a clear
  migration reason.
- Prefer actionable load errors over silently ignored config.

**Suggested shape:**

- Validate top-level config keys:
  - current: `homeserver`, `user_id`, `operator_user_id`, `auto_join_rooms`,
    `rooms`
  - legacy hard errors: `room_id`, `target_room_id`, top-level `extensions`
- Validate room keys:
  - current: `extensions`, `routes`
  - unknown keys should error with `rooms.<room_id>.<field>`.
- Validate route keys:
  - current: `trigger`, `handler`, `prefix`, `emoji`, `extensions`
  - legacy hard error: `config`
  - known compatibility allowance: decide whether old top-level `limit`
    should be accepted and ignored, or rejected with "move it into
    extensions.limit". Since bilby can be updated in sync, prefer rejecting
    it with the migration message.

**Matrixbot tests to write first:**

- Unknown top-level field fails with the field name.
- Unknown room field fails with the room ID and field name.
- Unknown route field fails with room ID, route index, and field name.
- Legacy route `config` keeps the existing migration error.
- Legacy route `limit` now fails with a message pointing to
  `extensions.limit`.
- Valid current config still round-trips.

**Bilby sync:**

- Update any bilby tests or docs that still show route-level `limit`.
- Ensure bilby uses `RouteConfig.Extensions` for `mopoke_list.limit`.

## 4. Make Request Carry Common Matrix Context Explicitly

**Finding:** `Request.Input` is doing several jobs. For reaction routes it is
the parent message body, while `Request.EventID` remains the reaction event
ID. A handler that needs to refer to the source message has to know that the
trigger discarded parent metadata.

**Current leak:**

- `ReactionTrigger` fetches the parent event, extracts the body, and throws
  away the parent event ID and parent sender.
- README tells handlers that need richer context to write custom triggers and
  pack fields into `Input` or change `Request` upstream.

**Proposed fix:**

- Extend `Request` with common optional fields instead of forcing custom
  trigger conventions for standard Matrix concepts.

**Suggested shape:**

- Add `ParentEventID id.EventID` to `Request`.
- Consider `ParentSender id.UserID` and `ParentBody string` only if bilby or
  tests demonstrate a concrete need. Avoid adding a large raw-event escape
  hatch unless a handler actually needs it.
- Set `ParentEventID` in `ReactionTrigger` from the relation event ID.
- Keep `Input` as the handler's primary text so existing handler logic stays
  simple while bilby is updated in sync.

**Matrixbot tests to write first:**

- Reaction trigger sets `Request.ParentEventID`.
- Mention and command triggers leave `ParentEventID` empty.
- Existing reaction `Input` behavior remains the parent body.

**Bilby sync:**

- No functional bilby change may be needed immediately.
- Add or update handler tests only if bilby wants to mention/link the source
  event in task creation responses.

## 5. Align Mention Matching And Mention Text Extraction

**Finding:** mention matching deliberately avoids localpart-only body matches,
but extraction still strips the first localpart occurrence when the message
matched via structured `m.mentions`. That can mangle messages containing text
like `@bot-admin`.

**Current leak:**

- `shouldHandleMention` matches full MXID in body or `m.mentions`.
- `extractMessageText` removes full MXID first, then localpart.
- If `m.mentions` names the bot but the body contains another localpart-like
  token first, extraction can remove the wrong text.

**Proposed fix:**

- Make one mention parser own both match evidence and extraction, or pass the
  match kind into extraction.
- Do not strip localpart text unless the body match actually used that
  representation safely.

**Suggested shape:**

- Introduce an internal helper that returns `(input string, ok bool)`.
- For body fallback, only match and strip the full Matrix user ID.
- For structured `m.mentions`, strip the full Matrix user ID if present;
  otherwise leave the body intact except for existing whitespace/punctuation
  trimming.
- If localpart stripping is still desired, require a safer boundary rule and
  test `@bot-admin`.

**Matrixbot tests to write first:**

- Structured mention plus body containing `@bot-admin` does not remove
  `@bot` from `@bot-admin`.
- Full MXID mention still strips correctly.
- Bare structured mention with no textual payload still does not match if
  the resulting input is empty.

**Bilby sync:**

- No bilby code change expected; behavior improves through matrixbot.

## 6. Preserve 0600 Modes When Temp Files Already Exist

**Finding:** `writeJSON` writes to `<path>.tmp` with `os.WriteFile(...,
0600)`, but that mode only applies when the temp file is newly created. A
stale wide temp file can be renamed into place with wider permissions.

**Current leak:**

- matrixbot documents that JSON files are secret and written as 0600.
- A crash-leftover or manually created temp file can violate the invariant.

**Proposed fix:**

- Ensure the temp file is private before rename.

**Suggested shape:**

- Prefer opening the temp file with `os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)`.
- If an old temp file exists, remove it and retry, or use a unique temp name
  in the same directory.
- Alternatively, `Chmod(tmp, 0600)` before rename is a smaller fix, but an
  exclusive create avoids following stale-file assumptions.

**Matrixbot tests to write first:**

- Pre-create `config.json.tmp` with mode 0644, call `Save`, and assert the
  final `config.json` is 0600.
- Repeat through `Session.Save` or `Account.Save` if the helper-level test
  does not cover all public paths clearly.

**Bilby sync:**

- No bilby code change expected.

## Suggested Execution Order

1. `writeJSON` temp-file mode fix: small, isolated, security invariant.
2. mention extraction fix: small behavior cleanup with focused tests.
3. `RouteConfig.BuildTrigger`: straightforward matrixbot + bilby sync.
4. config validation: affects operator config and tests; do after trigger
   helper so route names and fields are centralized.
5. `Request.ParentEventID`: low risk but public API shape; do before any
   bilby handler work that needs source-message references.
6. default init bootstrap: highest payoff but touches crypto seams, README,
   and bilby imports.

## Verification Commands

Run from `matrixbot`:

```sh
go test -tags goolm ./...
```

Run from `../bilby` after sync changes:

```sh
go test -tags goolm ./...
```

Plain `go test ./...` may fail on machines without `libolm` headers because
mautrix's default crypto backend uses cgo/libolm.
