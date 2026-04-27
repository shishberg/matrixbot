package matrixbot

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"

	"maunium.net/go/mautrix"
)

// LoginClient is the slice of mautrix.Client init / login need. Tests
// inject a fake.
type LoginClient interface {
	Login(ctx context.Context, req *mautrix.ReqLogin) (*mautrix.RespLogin, error)
}

// LoginClientFactory builds a LoginClient pointed at the given homeserver
// URL.
type LoginClientFactory func(homeserverURL string) (LoginClient, error)

// Bootstrapper mints the cross-signing identity. The real implementation
// (in the host program, since it pulls in mautrix's cryptohelper) opens
// crypto.db with pickleKey, calls helper.Init, then calls e2ee.Bootstrap.
// The test fake just returns a canned key without touching olm/sqlite.
//
// Returns the fresh recovery key (which the caller MUST persist even if
// err is non-nil — see RunInit's half-bootstrap handling).
type Bootstrapper func(ctx context.Context, dd DataDir, accessToken, deviceID, userID, homeserver, password, pickleKey string) (recoveryKey string, err error)

// EnvLookup is what InitDeps uses to read backwards-compat seed env vars
// when filling in prompt defaults. The test injects a map; production
// wires it to os.Getenv.
type EnvLookup interface {
	Get(key string) string
}

// EnvLookupFunc adapts os.Getenv (and any other plain `func(string) string`)
// to the EnvLookup interface.
type EnvLookupFunc func(string) string

// Get satisfies EnvLookup.
func (f EnvLookupFunc) Get(key string) string { return f(key) }

// InitDeps bundles the seams RunInit needs. Production wires real
// implementations; tests inject fakes.
type InitDeps struct {
	LoginFactory LoginClientFactory
	Bootstrap    Bootstrapper
	Env          EnvLookup
	Prompter     Prompter
	Stdout       io.Writer

	// ProgramName is used in operator-facing messages so this package
	// stays free of any bot-specific naming. Empty defaults to "the bot".
	ProgramName string
}

func (d InitDeps) program() string {
	if d.ProgramName == "" {
		return "the bot"
	}
	return d.ProgramName
}

// RunInit drives the interactive setup. It writes config.json,
// session.json and account.json under dd, in that order, then prints a
// single line directing the operator to start the bot.
//
// Refuses when config.json already exists — re-running init is almost
// never what the operator wants and silently overwriting credentials
// would be a footgun.
//
// Prompt-default seeding from env (homeserver, user_id, password,
// operator_user_id) is the only place matrixbot reads MATRIX_* env
// vars. Once the files exist, runtime config never looks at env again.
func RunInit(ctx context.Context, dd DataDir, deps InitDeps) error {
	if _, err := os.Stat(dd.ConfigPath()); err == nil {
		return fmt.Errorf("already initialized -- edit %s directly, or run '%s login' to log in again", dd.ConfigPath(), deps.program())
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("checking %s: %w", dd.ConfigPath(), err)
	}

	cfg, password, err := promptConfig(deps)
	if err != nil {
		return err
	}

	pickleKey, err := generatePickleKey()
	if err != nil {
		return fmt.Errorf("generating pickle key: %w", err)
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

	// Bootstrap may need the data dir to already exist (it opens
	// crypto.db there). Save config + session first so a half-bootstrap
	// failure leaves the operator able to inspect / retry without
	// re-prompting.
	if err := cfg.Save(dd); err != nil {
		return err
	}
	if err := session.Save(dd); err != nil {
		return err
	}

	recoveryKey, bootErr := deps.Bootstrap(ctx, dd, session.AccessToken, session.DeviceID, cfg.UserID, cfg.Homeserver, password, pickleKey)

	// Persist account.json BEFORE returning bootErr: e2ee.Bootstrap can
	// return a non-empty recovery key alongside an error (the SSSS key
	// was minted but the UIA-gated upload failed). That key is the
	// operator's only way out of a half-bootstrapped account.
	acct := Account{
		RecoveryKey: recoveryKey,
		PickleKey:   pickleKey,
	}
	if saveErr := acct.Save(dd); saveErr != nil {
		if bootErr != nil {
			return fmt.Errorf("bootstrap failed (%w) and saving account.json also failed: %v", bootErr, saveErr)
		}
		return saveErr
	}
	if bootErr != nil {
		return fmt.Errorf("cross-signing bootstrap: %w", bootErr)
	}

	fmt.Fprintf(deps.Stdout, "Initialized %s. Run '%s' to start.\n", string(dd), deps.program())
	return nil
}

// promptConfig drives the prompter, seeding answers from env where
// possible. When an env var is set we use it directly without prompting
// — that's the backwards-compat path for operators who already have a
// .envrc, so init is non-interactive for them.
func promptConfig(deps InitDeps) (Config, string, error) {
	homeserver, err := promptOrEnv(deps, "homeserver", "MATRIX_HOMESERVER", false)
	if err != nil {
		return Config{}, "", err
	}
	if homeserver == "" {
		return Config{}, "", fmt.Errorf("homeserver is required")
	}
	userID, err := promptOrEnv(deps, "bot user ID", "MATRIX_USER_ID", false)
	if err != nil {
		return Config{}, "", err
	}
	if userID == "" {
		return Config{}, "", fmt.Errorf("bot user ID is required")
	}
	if userID[0] != '@' {
		return Config{}, "", fmt.Errorf("bot user ID must start with '@'")
	}
	password, err := promptOrEnv(deps, "bot password", "MATRIX_PASSWORD", true)
	if err != nil {
		return Config{}, "", err
	}
	if password == "" {
		return Config{}, "", fmt.Errorf("bot password is required")
	}
	operator, err := promptOrEnv(deps, "operator user ID", "MATRIX_OPERATOR_USER_ID", false)
	if err != nil {
		return Config{}, "", err
	}

	return Config{
		Homeserver:     homeserver,
		UserID:         userID,
		OperatorUserID: operator,
	}, password, nil
}

// promptOrEnv returns the env value when non-empty; otherwise prompts.
// The silent env path is what makes init non-interactive for operators
// who already have MATRIX_* set in their shell.
func promptOrEnv(deps InitDeps, label, envKey string, secret bool) (string, error) {
	if v := deps.Env.Get(envKey); v != "" {
		return v, nil
	}
	return deps.Prompter.Prompt(label, "", secret)
}

// generatePickleKey returns 32 random bytes hex-encoded. 64 hex chars is
// long enough that an attacker who has the encrypted pickles still can't
// brute-force the wrapping key.
func generatePickleKey() (string, error) {
	var buf [32]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf[:]), nil
}
