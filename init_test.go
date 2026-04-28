package matrixbot

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/id"
)

// fakeBootstrapper records its inputs and returns a canned recovery key.
// When writeCryptoDB is true it also writes a fake crypto.db (and the WAL
// / SHM sidecars) at mode 0644 to mirror what mautrix's cryptohelper does
// — it opens the SQLite store with the process umask, leaving the files
// world-readable on a default Mac/Linux setup. RunInit is supposed to
// clamp those down to 0600.
type fakeBootstrapper struct {
	gotPickleKey  string
	gotPassword   string
	recoveryKey   string
	err           error
	calledOpenDir DataDir
	writeCryptoDB bool
}

func (f *fakeBootstrapper) Bootstrap(ctx context.Context, dd DataDir, accessToken, deviceID, userID, homeserver, password, pickleKey string) (string, error) {
	f.gotPickleKey = pickleKey
	f.gotPassword = password
	f.calledOpenDir = dd
	if f.writeCryptoDB {
		if err := os.MkdirAll(string(dd), 0o700); err != nil {
			return f.recoveryKey, err
		}
		for _, p := range dd.CryptoDBPaths() {
			if err := os.WriteFile(p, []byte("fake"), 0o644); err != nil {
				return f.recoveryKey, err
			}
			// WriteFile honours umask, so chmod explicitly to pin the mode.
			if err := os.Chmod(p, 0o644); err != nil {
				return f.recoveryKey, err
			}
		}
	}
	return f.recoveryKey, f.err
}

type fakeInitLoginClient struct {
	gotReq *mautrix.ReqLogin
	resp   *mautrix.RespLogin
	err    error
}

func (f *fakeInitLoginClient) Login(ctx context.Context, req *mautrix.ReqLogin) (*mautrix.RespLogin, error) {
	f.gotReq = req
	return f.resp, f.err
}

// initEnv is a stripped-down env-vars map used as the seed for prompt
// defaults.
type initEnv map[string]string

func (e initEnv) Get(k string) string { return e[k] }

func newHappyPathSetup(t *testing.T) (DataDir, *fakeInitLoginClient, *fakeBootstrapper, *cannedPrompter, *bytes.Buffer, InitDeps) {
	t.Helper()
	dir := DataDir(t.TempDir() + "/.matrixbot")
	login := &fakeInitLoginClient{
		resp: &mautrix.RespLogin{
			AccessToken: "syt_secret",
			DeviceID:    id.DeviceID("FRESHID"),
			UserID:      id.UserID("@bot:example"),
		},
	}
	boot := &fakeBootstrapper{recoveryKey: "EsTQ-recovery"}
	prompter := &cannedPrompter{
		answers: map[string]string{
			"homeserver":       "https://matrix.example",
			"bot user ID":      "@bot:example",
			"bot password":     "hunter2",
			"operator user ID": "@dave:example",
		},
	}
	out := &bytes.Buffer{}
	deps := InitDeps{
		LoginFactory: func(homeserver string) (LoginClient, error) {
			if homeserver != "https://matrix.example" {
				t.Errorf("LoginFactory homeserver = %q", homeserver)
			}
			return login, nil
		},
		Bootstrap: boot.Bootstrap,
		Env:       initEnv{},
		Prompter:  prompter,
		Stdout:    out,
	}
	return dir, login, boot, prompter, out, deps
}

func TestRunInitHappyPathWritesAllThreeFiles(t *testing.T) {
	dd, _, boot, _, out, deps := newHappyPathSetup(t)

	if err := RunInit(context.Background(), dd, deps); err != nil {
		t.Fatalf("RunInit: %v", err)
	}

	cfg, err := LoadConfig(dd)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Homeserver != "https://matrix.example" {
		t.Errorf("cfg.Homeserver = %q", cfg.Homeserver)
	}
	if cfg.UserID != "@bot:example" {
		t.Errorf("cfg.UserID = %q", cfg.UserID)
	}
	if cfg.OperatorUserID != "@dave:example" {
		t.Errorf("cfg.OperatorUserID = %q", cfg.OperatorUserID)
	}

	sess, err := LoadSession(dd)
	if err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	if sess.AccessToken != "syt_secret" {
		t.Errorf("sess.AccessToken = %q", sess.AccessToken)
	}
	if sess.DeviceID != "FRESHID" {
		t.Errorf("sess.DeviceID = %q", sess.DeviceID)
	}

	acct, err := LoadAccount(dd)
	if err != nil {
		t.Fatalf("LoadAccount: %v", err)
	}
	if acct.RecoveryKey != "EsTQ-recovery" {
		t.Errorf("acct.RecoveryKey = %q", acct.RecoveryKey)
	}
	if acct.PickleKey == "" {
		t.Error("acct.PickleKey is empty; expected a generated value")
	}
	if acct.PickleKey != boot.gotPickleKey {
		t.Errorf("Bootstrap was called with pickle_key %q but file holds %q", boot.gotPickleKey, acct.PickleKey)
	}

	// Stdout should NOT include the password or recovery key — both are
	// secrets that belong only on disk.
	s := out.String()
	if strings.Contains(s, "hunter2") {
		t.Errorf("stdout leaks password: %q", s)
	}
	if strings.Contains(s, "EsTQ-recovery") {
		t.Errorf("stdout leaks recovery key: %q", s)
	}
	if !strings.Contains(s, string(dd)) {
		t.Errorf("stdout should mention data dir, got %q", s)
	}
}

func TestRunInitDoesNotPromptForRoomID(t *testing.T) {
	// Per-room config now lives under cfg.Rooms and is hand-edited.
	// init must not ask for a room — that prompt belongs to the old
	// single-room schema.
	dd, _, _, prompter, _, deps := newHappyPathSetup(t)
	if err := RunInit(context.Background(), dd, deps); err != nil {
		t.Fatalf("RunInit: %v", err)
	}
	for _, label := range prompter.calls {
		if strings.Contains(strings.ToLower(label), "room") {
			t.Errorf("init prompted for %q; should not ask for a room", label)
		}
	}
}

func TestRunInitFilesAreMode0600(t *testing.T) {
	dd, _, _, _, _, deps := newHappyPathSetup(t)
	if err := RunInit(context.Background(), dd, deps); err != nil {
		t.Fatalf("RunInit: %v", err)
	}
	for _, p := range []string{dd.ConfigPath(), dd.SessionPath(), dd.AccountPath()} {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatalf("stat %s: %v", p, err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Errorf("%s mode = %o, want 0600", p, got)
		}
	}
}

func TestRunInitEnvDefaultsSkipPrompts(t *testing.T) {
	dd := DataDir(t.TempDir() + "/.matrixbot")
	login := &fakeInitLoginClient{
		resp: &mautrix.RespLogin{AccessToken: "tok", DeviceID: id.DeviceID("D")},
	}
	boot := &fakeBootstrapper{recoveryKey: "rk"}
	// Empty answers map — if anything actually prompts (i.e. Prompter is
	// called for a label not pre-seeded), the call returns "" and we'd
	// fail to write a usable config below.
	prompter := &cannedPrompter{}
	deps := InitDeps{
		LoginFactory: func(string) (LoginClient, error) { return login, nil },
		Bootstrap:    boot.Bootstrap,
		Env: initEnv{
			"MATRIX_HOMESERVER":       "https://matrix.example",
			"MATRIX_USER_ID":          "@bot:example",
			"MATRIX_PASSWORD":         "hunter2",
			"MATRIX_OPERATOR_USER_ID": "@dave:example",
		},
		Prompter: prompter,
		Stdout:   &bytes.Buffer{},
	}
	if err := RunInit(context.Background(), dd, deps); err != nil {
		t.Fatalf("RunInit: %v", err)
	}
	if len(prompter.calls) != 0 {
		t.Errorf("prompter was called for %v, want zero calls when env covers everything", prompter.calls)
	}
	cfg, _ := LoadConfig(dd)
	if cfg.Homeserver != "https://matrix.example" {
		t.Errorf("cfg.Homeserver = %q", cfg.Homeserver)
	}
}

func TestRunInitRefusesWhenAlreadyInitialized(t *testing.T) {
	dd, _, _, _, _, deps := newHappyPathSetup(t)
	if err := (Config{Homeserver: "h", UserID: "u"}).Save(dd); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	err := RunInit(context.Background(), dd, deps)
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if !strings.Contains(err.Error(), "already initialized") {
		t.Errorf("err should mention 'already initialized', got %q", err)
	}
	if !strings.Contains(err.Error(), dd.ConfigPath()) {
		t.Errorf("err should mention config path, got %q", err)
	}
}

func TestRunInitHalfBootstrapPersistsRecoveryKeyDespiteError(t *testing.T) {
	dd, _, boot, _, _, deps := newHappyPathSetup(t)
	boot.recoveryKey = "EsTQ-half"
	boot.err = errors.New("UIA upload failed")

	err := RunInit(context.Background(), dd, deps)
	if err == nil {
		t.Fatal("want error, got nil")
	}

	// Recovery key MUST be on disk even though Bootstrap errored — that
	// key is the operator's only way out of a half-bootstrapped account.
	acct, loadErr := LoadAccount(dd)
	if loadErr != nil {
		t.Fatalf("LoadAccount: %v", loadErr)
	}
	if acct.RecoveryKey != "EsTQ-half" {
		t.Errorf("acct.RecoveryKey = %q, want %q", acct.RecoveryKey, "EsTQ-half")
	}
}

func TestRunInitErrorsWhenServerOmitsDeviceID(t *testing.T) {
	// An empty device_id from /login would land on disk and silently
	// break cross-signing on the next start. Refuse early.
	dd, login, _, _, _, deps := newHappyPathSetup(t)
	login.resp = &mautrix.RespLogin{AccessToken: "syt_secret", DeviceID: ""}

	err := RunInit(context.Background(), dd, deps)
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if !strings.Contains(err.Error(), "device_id") {
		t.Errorf("err should mention device_id, got %q", err)
	}
	for _, p := range []string{dd.ConfigPath(), dd.SessionPath(), dd.AccountPath()} {
		if _, statErr := os.Stat(p); !errors.Is(statErr, os.ErrNotExist) {
			t.Errorf("%s must not be written when device_id is missing", p)
		}
	}
}

func TestRunInitTightensCryptoDBPermissions(t *testing.T) {
	// mautrix's cryptohelper opens crypto.db with the process umask
	// (typically 0644). RunInit must clamp it (and any sidecars) to 0600
	// so the README's "all files are mode 0600" promise holds out of the
	// box, not just after the bot has run once.
	dd, _, boot, _, _, deps := newHappyPathSetup(t)
	boot.writeCryptoDB = true

	if err := RunInit(context.Background(), dd, deps); err != nil {
		t.Fatalf("RunInit: %v", err)
	}

	for _, p := range dd.CryptoDBPaths() {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatalf("stat %s: %v", p, err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Errorf("%s mode = %o, want 0600", p, got)
		}
	}
}

func TestRunInitTightensCryptoDBPermissionsOnHalfBootstrap(t *testing.T) {
	// Half-bootstrap means Bootstrap returned a recovery key alongside
	// an error — the SSSS key was minted but the UIA-gated upload failed.
	// crypto.db is on disk regardless, so the chmod must still happen
	// even though RunInit will return an error.
	dd, _, boot, _, _, deps := newHappyPathSetup(t)
	boot.writeCryptoDB = true
	boot.recoveryKey = "EsTQ-half"
	boot.err = errors.New("UIA upload failed")

	err := RunInit(context.Background(), dd, deps)
	if err == nil {
		t.Fatal("want error, got nil")
	}

	for _, p := range dd.CryptoDBPaths() {
		info, statErr := os.Stat(p)
		if statErr != nil {
			t.Fatalf("stat %s: %v", p, statErr)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Errorf("%s mode = %o, want 0600", p, got)
		}
	}
}

func TestRunInitPasswordNeverInError(t *testing.T) {
	dd, login, _, _, _, deps := newHappyPathSetup(t)
	login.err = errors.New("400: response body included hunter2 echoed back")
	login.resp = nil

	err := RunInit(context.Background(), dd, deps)
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if strings.Contains(err.Error(), "hunter2") {
		t.Errorf("error leaks password: %q", err)
	}
}
