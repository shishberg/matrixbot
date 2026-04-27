package matrixbot

import (
	"errors"
	"os"
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
