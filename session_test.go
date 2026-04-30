package matrixbot

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestSessionSaveAndLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	dd := DataDir(dir)
	in := Session{AccessToken: "syt_abc", DeviceID: "DEV1"}
	if err := in.Save(dd); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := LoadSession(dd)
	if err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	if got != in {
		t.Errorf("round-trip lost data: got %+v, want %+v", got, in)
	}
}

func TestSessionSaveMode0600(t *testing.T) {
	dir := t.TempDir()
	dd := DataDir(dir)
	if err := (Session{AccessToken: "t", DeviceID: "d"}).Save(dd); err != nil {
		t.Fatalf("Save: %v", err)
	}
	info, err := os.Stat(dd.SessionPath())
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("mode = %o, want 0600", got)
	}

	secretsInfo, err := os.Stat(dd.SecretsDir())
	if err != nil {
		t.Fatalf("stat secrets dir: %v", err)
	}
	if got := secretsInfo.Mode().Perm(); got != 0o700 {
		t.Errorf("secrets dir mode = %o, want 0700", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "session.json")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("legacy top-level session.json should not be created, stat err: %v", err)
	}
}

func TestLoadSessionMigratesLegacyTopLevelFile(t *testing.T) {
	dir := t.TempDir()
	dd := DataDir(dir)
	oldPath := filepath.Join(dir, "session.json")
	if err := os.WriteFile(oldPath, []byte(`{"access_token":"legacy","device_id":"OLD"}`), 0o600); err != nil {
		t.Fatalf("write legacy session: %v", err)
	}

	got, err := LoadSession(dd)
	if err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	if got != (Session{AccessToken: "legacy", DeviceID: "OLD"}) {
		t.Errorf("session = %+v", got)
	}
	if _, err := os.Stat(oldPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("legacy session should be moved, stat err: %v", err)
	}
	info, err := os.Stat(dd.SessionPath())
	if err != nil {
		t.Fatalf("stat migrated session: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("migrated mode = %o, want 0600", got)
	}
}

func TestLoadSessionMissingReturnsErrNotInitialized(t *testing.T) {
	dir := t.TempDir()
	_, err := LoadSession(DataDir(dir))
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if !errors.Is(err, ErrNotInitialized) {
		t.Errorf("err = %v, want ErrNotInitialized", err)
	}
}
