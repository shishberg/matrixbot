package matrixbot

import (
	"path/filepath"
	"testing"
)

func TestResolveDataDirDefault(t *testing.T) {
	t.Setenv("MATRIXBOT_DATA_DIR", "")
	got, err := ResolveDataDir()
	if err != nil {
		t.Fatalf("ResolveDataDir: %v", err)
	}
	if !filepath.IsAbs(string(got)) {
		t.Errorf("DataDir = %q, want an absolute path", got)
	}
	if filepath.Base(string(got)) != ".matrixbot" {
		t.Errorf("DataDir basename = %q, want %q", filepath.Base(string(got)), ".matrixbot")
	}
}

func TestResolveDataDirEnvOverride(t *testing.T) {
	t.Setenv("MATRIXBOT_DATA_DIR", "/var/lib/example")
	got, err := ResolveDataDir()
	if err != nil {
		t.Fatalf("ResolveDataDir: %v", err)
	}
	if string(got) != "/var/lib/example" {
		t.Errorf("DataDir = %q, want %q", got, "/var/lib/example")
	}
}

func TestResolveDataDirEnvOverrideRelativeIsResolvedAbsolute(t *testing.T) {
	t.Setenv("MATRIXBOT_DATA_DIR", "./somewhere")
	got, err := ResolveDataDir()
	if err != nil {
		t.Fatalf("ResolveDataDir: %v", err)
	}
	if !filepath.IsAbs(string(got)) {
		t.Errorf("DataDir = %q, want absolute path", got)
	}
	if filepath.Base(string(got)) != "somewhere" {
		t.Errorf("DataDir basename = %q, want %q", filepath.Base(string(got)), "somewhere")
	}
}

func TestPathsAccessors(t *testing.T) {
	dd := DataDir("/tmp/example")
	if got, want := dd.ConfigPath(), "/tmp/example/config.json"; got != want {
		t.Errorf("ConfigPath = %q, want %q", got, want)
	}
	if got, want := dd.SessionPath(), "/tmp/example/session.json"; got != want {
		t.Errorf("SessionPath = %q, want %q", got, want)
	}
	if got, want := dd.AccountPath(), "/tmp/example/account.json"; got != want {
		t.Errorf("AccountPath = %q, want %q", got, want)
	}
	if got, want := dd.CryptoDBPath(), "/tmp/example/crypto.db"; got != want {
		t.Errorf("CryptoDBPath = %q, want %q", got, want)
	}
	if got, want := dd.SchedulePath(), "/tmp/example/schedule.json"; got != want {
		t.Errorf("SchedulePath = %q, want %q", got, want)
	}
}

func TestCryptoDBSidecarPaths(t *testing.T) {
	dd := DataDir("/tmp/example")
	got := dd.CryptoDBPaths()
	want := []string{
		"/tmp/example/crypto.db",
		"/tmp/example/crypto.db-wal",
		"/tmp/example/crypto.db-shm",
	}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d (got %v)", len(got), len(want), got)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("got[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
