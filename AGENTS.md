# Repository Guidelines

## Project Structure

This repository is a Go library module, `github.com/shishberg/matrixbot`, for running Matrix bots with persistent local state, routing, init/login/logout helpers, and optional E2EE. Most code lives in the root package. Key areas include `bot.go` for the runtime loop, `route.go` and `triggers.go` for dispatch, `config.go`, `session.go`, `account.go`, and `paths.go` for data-dir persistence, and `scrub.go` for redaction. The `e2ee/` subpackage contains cross-signing and SAS verification helpers. Tests sit beside the code as `*_test.go`.

## Build and Test Commands

- `make test` runs the full local test suite.
- `TESTFLAGS='-run TestName' make test` runs a focused test while iterating.
- `TESTFLAGS='-count=1' make test` bypasses cached results when checking behavior changes.
- `make test-race` is useful for route, sync-loop, or concurrency-sensitive changes.
- `make build` verifies all packages compile; this repo does not ship a standalone main package.
- `make vet` runs Go's static checks.
- `make fmt` formats Go files in place.

The Makefile centralizes the pure-Go olm tag, `-tags goolm`, so local contributors do not need libolm headers. Plain `go test ./...` may require libolm headers on the local machine.

## Coding Style

Use idiomatic Go and run `make fmt` for Go changes. Prefer small interfaces around external Matrix clients so behavior stays unit-testable without a homeserver. Keep errors wrapped with useful context and preserve existing `errors.Is` contracts. Comments should explain non-obvious invariants, security choices, or public API contracts, not restate nearby code.

## Testing

Add or update focused tests for every behavior change. Prefer table tests for trigger, config, and path edge cases. Keep tests deterministic and filesystem-safe with temp directories. Avoid real Matrix network calls in unit tests; use local fakes or narrow interfaces.

## Commit and PR Guidelines

Keep commits scoped and describe the user-visible behavior or maintenance reason. PRs should summarize the change, list validation run, and call out config or migration impact. Include security notes when touching session, account, crypto, or logging paths.

## Security and Config Tips

Treat `.matrixbot/`, `session.json`, `account.json`, and `crypto.db` as secrets. Do not commit local data directories, access tokens, recovery keys, pickle keys, or Matrix passwords. Be careful with `MATRIX_PASSWORD` and other `MATRIX_*` init variables because they can bypass prompts. Preserve private file modes and existing redaction behavior.
