package matrixbot

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestAccountSaveAndLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	dd := DataDir(dir)
	in := Account{
		RecoveryKey: "EsTQ 9MUs xSRn Vptm 1m1H",
		PickleKey:   "abc123",
	}
	if err := in.Save(dd); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := LoadAccount(dd)
	if err != nil {
		t.Fatalf("LoadAccount: %v", err)
	}
	if got != in {
		t.Errorf("round-trip lost data: got %+v, want %+v", got, in)
	}
}

func TestAccountSaveMode0600(t *testing.T) {
	dir := t.TempDir()
	dd := DataDir(dir)
	if err := (Account{RecoveryKey: "rk", PickleKey: "pk"}).Save(dd); err != nil {
		t.Fatalf("Save: %v", err)
	}
	info, err := os.Stat(dd.AccountPath())
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
	if _, err := os.Stat(filepath.Join(dir, "account.json")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("legacy top-level account.json should not be created, stat err: %v", err)
	}
}

func TestLoadAccountMigratesLegacyTopLevelFile(t *testing.T) {
	dir := t.TempDir()
	dd := DataDir(dir)
	oldPath := filepath.Join(dir, "account.json")
	if err := os.WriteFile(oldPath, []byte(`{"recovery_key":"legacy-rk","pickle_key":"legacy-pk"}`), 0o600); err != nil {
		t.Fatalf("write legacy account: %v", err)
	}

	got, err := LoadAccount(dd)
	if err != nil {
		t.Fatalf("LoadAccount: %v", err)
	}
	if got != (Account{RecoveryKey: "legacy-rk", PickleKey: "legacy-pk"}) {
		t.Errorf("account = %+v", got)
	}
	if _, err := os.Stat(oldPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("legacy account should be moved, stat err: %v", err)
	}
	info, err := os.Stat(dd.AccountPath())
	if err != nil {
		t.Fatalf("stat migrated account: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("migrated mode = %o, want 0600", got)
	}
}

func TestLoadAccountMissingReturnsErrNotInitialized(t *testing.T) {
	dir := t.TempDir()
	_, err := LoadAccount(DataDir(dir))
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if !errors.Is(err, ErrNotInitialized) {
		t.Errorf("err = %v, want ErrNotInitialized", err)
	}
}
