package matrixbot

import (
	"context"
	"fmt"
	"io"

	"maunium.net/go/mautrix"
)

// LoginDeps bundles the seams RunLogin needs.
type LoginDeps struct {
	LoginFactory LoginClientFactory
	Prompter     Prompter
	Stdout       io.Writer

	// ProgramName is used in operator-facing messages; empty defaults
	// to "the bot".
	ProgramName string
}

func (d LoginDeps) program() string {
	if d.ProgramName == "" {
		return "the bot"
	}
	return d.ProgramName
}

// RunLogin rotates the access token. It reads homeserver + user_id from
// config.json (so the operator never has to repeat them), prompts for a
// password, calls /login, and overwrites session.json. It deliberately
// does NOT read MATRIX_PASSWORD from env: keeping secrets out of env at
// runtime is the whole reason for the data dir.
func RunLogin(ctx context.Context, dd DataDir, deps LoginDeps) error {
	cfg, err := LoadConfig(dd)
	if err != nil {
		return fmt.Errorf("%w (run '%s init' first)", err, deps.program())
	}
	password, err := deps.Prompter.Prompt("bot password", "", true)
	if err != nil {
		return err
	}
	if password == "" {
		return fmt.Errorf("password is required")
	}

	client, err := deps.LoginFactory(cfg.Homeserver)
	if err != nil {
		return fmt.Errorf("building matrix client: %w", err)
	}
	resp, err := client.Login(ctx, &mautrix.ReqLogin{
		Type: mautrix.AuthTypePassword,
		Identifier: mautrix.UserIdentifier{
			Type: mautrix.IdentifierTypeUser,
			User: cfg.UserID,
		},
		Password: password,
	})
	if err != nil {
		return fmt.Errorf("login: %s", scrubSecret(err.Error(), password))
	}
	if resp == nil || resp.AccessToken == "" {
		return fmt.Errorf("login: server returned no access token")
	}
	if resp.DeviceID == "" {
		// Persisting an empty device_id would re-register the bot as a
		// fresh device on next start, silently shedding cross-signing.
		return fmt.Errorf("login: server returned no device_id")
	}

	session := Session{
		AccessToken: resp.AccessToken,
		DeviceID:    string(resp.DeviceID),
	}
	if err := session.Save(dd); err != nil {
		return err
	}

	fmt.Fprintf(deps.Stdout, "logged in: device_id=%s\n", resp.DeviceID)
	return nil
}
