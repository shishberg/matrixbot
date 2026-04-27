package matrixbot

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"maunium.net/go/mautrix"
)

// LogoutClient is the slice of mautrix.Client RunLogout needs.
type LogoutClient interface {
	Logout(ctx context.Context) (*mautrix.RespLogout, error)
}

// LogoutClientFactory builds a LogoutClient pointed at the given
// homeserver and authenticated with the given access token.
type LogoutClientFactory func(homeserverURL, accessToken string) (LogoutClient, error)

// LogoutDeps bundles the seams RunLogout needs.
type LogoutDeps struct {
	LogoutFactory LogoutClientFactory
	Stdout        io.Writer

	// ProgramName is used in operator-facing messages; empty defaults
	// to "the bot".
	ProgramName string
}

func (d LogoutDeps) program() string {
	if d.ProgramName == "" {
		return "the bot"
	}
	return d.ProgramName
}

// RunLogout invalidates the server-side session and wipes session.json
// plus the local crypto store. config.json and account.json are
// preserved so the next login keeps the same recovery key and
// cross-signing identity — rotating an access token doesn't change who
// the bot is.
//
// A server-side failure (token already invalidated, network blip) MUST
// NOT block the local cleanup — recovering from a desynced state is
// the whole reason this command exists.
func RunLogout(ctx context.Context, dd DataDir, deps LogoutDeps) error {
	cfg, err := LoadConfig(dd)
	if err != nil {
		return fmt.Errorf("%w (run '%s init' first)", err, deps.program())
	}
	sess, err := LoadSession(dd)
	if err != nil {
		return fmt.Errorf("%w (no session to log out)", err)
	}

	client, err := deps.LogoutFactory(cfg.Homeserver, sess.AccessToken)
	switch {
	case err != nil:
		fmt.Fprintf(deps.Stdout, "server logout skipped: %s\n", scrubSecret(err.Error(), sess.AccessToken))
	default:
		if _, err := client.Logout(ctx); err != nil {
			fmt.Fprintf(deps.Stdout, "server logout failed (continuing with local cleanup): %s\n", scrubSecret(err.Error(), sess.AccessToken))
		} else {
			fmt.Fprintln(deps.Stdout, "server logout: ok")
		}
	}

	reportRemoval(deps.Stdout, dd.SessionPath())
	for _, p := range dd.CryptoDBPaths() {
		reportRemoval(deps.Stdout, p)
	}
	fmt.Fprintln(deps.Stdout, "logged out")
	return nil
}

// reportRemoval deletes path and prints "removed: <path>" or
// "not found: <path>" depending on whether it was there. Other errors
// (permission denied, etc.) are reported but don't abort the command.
func reportRemoval(stdout io.Writer, path string) {
	err := os.Remove(path)
	switch {
	case err == nil:
		fmt.Fprintf(stdout, "removed: %s\n", path)
	case errors.Is(err, os.ErrNotExist):
		fmt.Fprintf(stdout, "not found: %s\n", path)
	default:
		fmt.Fprintf(stdout, "remove failed: %s: %s\n", path, err)
	}
}
