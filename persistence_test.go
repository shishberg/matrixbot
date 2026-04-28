package matrixbot

import (
	"os"
	"testing"
)

func TestSaveReplacesPreExistingTempFileWithMode0600(t *testing.T) {
	tests := []struct {
		name string
		path func(DataDir) string
		save func(DataDir) error
	}{
		{
			name: "config",
			path: func(dd DataDir) string { return dd.ConfigPath() },
			save: func(dd DataDir) error {
				return (Config{Homeserver: "h", UserID: "u"}).Save(dd)
			},
		},
		{
			name: "account",
			path: func(dd DataDir) string { return dd.AccountPath() },
			save: func(dd DataDir) error {
				return (Account{RecoveryKey: "rk", PickleKey: "pk"}).Save(dd)
			},
		},
		{
			name: "session",
			path: func(dd DataDir) string { return dd.SessionPath() },
			save: func(dd DataDir) error {
				return (Session{AccessToken: "t", DeviceID: "d"}).Save(dd)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dd := DataDir(t.TempDir())
			if err := os.MkdirAll(string(dd), 0o700); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			tmp := tt.path(dd) + ".tmp"
			if err := os.WriteFile(tmp, []byte("stale"), 0o644); err != nil {
				t.Fatalf("write temp: %v", err)
			}
			if err := os.Chmod(tmp, 0o644); err != nil {
				t.Fatalf("chmod temp: %v", err)
			}

			if err := tt.save(dd); err != nil {
				t.Fatalf("Save: %v", err)
			}

			info, err := os.Stat(tt.path(dd))
			if err != nil {
				t.Fatalf("stat saved file: %v", err)
			}
			if got := info.Mode().Perm(); got != 0o600 {
				t.Errorf("mode = %o, want 0600", got)
			}
		})
	}
}
