package matrixbot

import (
	"errors"
	"os"
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
