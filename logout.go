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
// plus the local crypto store from the secrets dir. config.json and account.json are
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

	var failures []error
	if err := reportRemoval(deps.Stdout, dd.SessionPath()); err != nil {
		failures = append(failures, err)
	}
	for _, p := range dd.CryptoDBPaths() {
		if err := reportRemoval(deps.Stdout, p); err != nil {
			failures = append(failures, err)
		}
	}
	if len(failures) > 0 {
		// An operator who sees "logged out" while the access token is
		// still on disk is the failure mode this guards against.
		return fmt.Errorf("local cleanup failed: %w", errors.Join(failures...))
	}
	fmt.Fprintln(deps.Stdout, "logged out")
	return nil
}

// reportRemoval deletes path, prints a one-line status, and returns nil
// when the path is gone (either removed now or already absent). A real
// removal failure prints the same status line and returns the error so
// RunLogout can refuse to claim success.
func reportRemoval(stdout io.Writer, path string) error {
	err := os.Remove(path)
	switch {
	case err == nil:
		fmt.Fprintf(stdout, "removed: %s\n", path)
		return nil
	case errors.Is(err, os.ErrNotExist):
		fmt.Fprintf(stdout, "not found: %s\n", path)
		return nil
	default:
		fmt.Fprintf(stdout, "remove failed: %s: %s\n", path, err)
		return fmt.Errorf("remove %s: %w", path, err)
	}
}
