package matrixbot

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"maunium.net/go/mautrix"
)

type fakeLogoutClient struct {
	gotToken string
	err      error
	called   bool
}

func (f *fakeLogoutClient) Logout(ctx context.Context) (*mautrix.RespLogout, error) {
	f.called = true
	return &mautrix.RespLogout{}, f.err
}

func seedLoggedInDataDir(t *testing.T) DataDir {
	t.Helper()
	dd := DataDir(t.TempDir() + "/.matrixbot")
	if err := (Config{Homeserver: "https://matrix.example", UserID: "@bot:e"}).Save(dd); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	if err := (Session{AccessToken: "syt_secret", DeviceID: "DEV"}).Save(dd); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	if err := (Account{RecoveryKey: "rk", PickleKey: "pk"}).Save(dd); err != nil {
		t.Fatalf("seed account: %v", err)
	}
	for _, p := range dd.CryptoDBPaths() {
		if err := os.WriteFile(p, []byte("fake"), 0o600); err != nil {
			t.Fatalf("seed %s: %v", p, err)
		}
	}
	return dd
}

func TestRunLogoutCallsServerWipesSessionAndCryptoDBPreservesConfigAndAccount(t *testing.T) {
	dd := seedLoggedInDataDir(t)
	fake := &fakeLogoutClient{}
	out := &bytes.Buffer{}
	deps := LogoutDeps{
		LogoutFactory: func(homeserver, token string) (LogoutClient, error) {
			if homeserver != "https://matrix.example" {
				t.Errorf("homeserver = %q", homeserver)
			}
			if token != "syt_secret" {
				t.Errorf("token = %q", token)
			}
			fake.gotToken = token
			return fake, nil
		},
		Stdout: out,
	}
	if err := RunLogout(context.Background(), dd, deps); err != nil {
		t.Fatalf("RunLogout: %v", err)
	}
	if !fake.called {
		t.Error("Logout was not called")
	}
	// Session + crypto db gone.
	for _, p := range append([]string{dd.SessionPath()}, dd.CryptoDBPaths()...) {
		if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s still exists after logout", p)
		}
	}
	// Config + account preserved.
	for _, p := range []string{dd.ConfigPath(), dd.AccountPath()} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s should be preserved, stat err: %v", p, err)
		}
	}
	if !strings.Contains(out.String(), "logged out") {
		t.Errorf("stdout missing 'logged out': %q", out.String())
	}
}

func TestRunLogoutContinuesWhenServerErrors(t *testing.T) {
	dd := seedLoggedInDataDir(t)
	fake := &fakeLogoutClient{err: errors.New("M_UNKNOWN_TOKEN")}
	deps := LogoutDeps{
		LogoutFactory: func(string, string) (LogoutClient, error) { return fake, nil },
		Stdout:        &bytes.Buffer{},
	}
	if err := RunLogout(context.Background(), dd, deps); err != nil {
		t.Fatalf("RunLogout should not fail: %v", err)
	}
	if _, err := os.Stat(dd.SessionPath()); !errors.Is(err, os.ErrNotExist) {
		t.Error("session.json should be removed even when server errored")
	}
}

func TestRunLogoutErrorsWhenSessionMissing(t *testing.T) {
	dd := DataDir(t.TempDir() + "/.matrixbot")
	if err := (Config{Homeserver: "h", UserID: "@b:e"}).Save(dd); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	deps := LogoutDeps{
		LogoutFactory: func(string, string) (LogoutClient, error) {
			t.Fatal("LogoutFactory should not be called without a session")
			return nil, nil
		},
		Stdout: &bytes.Buffer{},
	}
	err := RunLogout(context.Background(), dd, deps)
	if err == nil {
		t.Fatal("want error, got nil")
	}
}

func TestRunLogoutMissingCryptoDBNotAnError(t *testing.T) {
	// Logging out from a freshly-init'd data dir (no crypto db yet)
	// must still succeed — that's the recovery path after an aborted
	// init.
	dd := DataDir(t.TempDir() + "/.matrixbot")
	if err := (Config{Homeserver: "h", UserID: "@b:e"}).Save(dd); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	if err := (Session{AccessToken: "tok", DeviceID: "D"}).Save(dd); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	fake := &fakeLogoutClient{}
	deps := LogoutDeps{
		LogoutFactory: func(string, string) (LogoutClient, error) { return fake, nil },
		Stdout:        &bytes.Buffer{},
	}
	if err := RunLogout(context.Background(), dd, deps); err != nil {
		t.Fatalf("RunLogout: %v", err)
	}
}
