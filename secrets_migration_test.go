package matrixbot

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadSessionMigratesLegacyCryptoDBFiles(t *testing.T) {
	dir := t.TempDir()
	dd := DataDir(dir)
	if err := os.WriteFile(filepath.Join(dir, "session.json"), []byte(`{"access_token":"t","device_id":"D"}`), 0o600); err != nil {
		t.Fatalf("write legacy session: %v", err)
	}
	legacyCryptoPaths := []string{
		filepath.Join(dir, "crypto.db"),
		filepath.Join(dir, "crypto.db-wal"),
		filepath.Join(dir, "crypto.db-shm"),
	}
	for _, p := range legacyCryptoPaths {
		if err := os.WriteFile(p, []byte("secret"), 0o600); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}

	if _, err := LoadSession(dd); err != nil {
		t.Fatalf("LoadSession: %v", err)
	}

	for _, p := range legacyCryptoPaths {
		if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("legacy crypto file should be moved: %s (stat err: %v)", p, err)
		}
	}
	for _, p := range dd.CryptoDBPaths() {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatalf("stat migrated %s: %v", p, err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Errorf("%s mode = %o, want 0600", p, got)
		}
	}
}
