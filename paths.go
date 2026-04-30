// Package matrixbot is a self-contained scaffold for a Matrix bot's
// persistent state and CLI flows (init / login / logout). It owns the
// on-disk layout under a single data directory and never reads
// host-project-specific env vars.
//
// The package is shared across host programs, so it must not import
// anything bot-specific from its host.
package matrixbot

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
)

// DataDirEnv is the one env var matrixbot reads. It picks the data
// directory; everything else lives inside that directory as JSON.
const DataDirEnv = "MATRIXBOT_DATA_DIR"

const defaultDataDir = ".matrixbot"

// DataDir is an absolute filesystem path to the bot's data directory. It
// holds config.json plus the private .secrets directory.
type DataDir string

// ResolveDataDir picks the data directory. MATRIXBOT_DATA_DIR wins;
// otherwise the default is ./.matrixbot resolved against the current
// working directory. The result is always absolute so a later cwd change
// (systemd WorkingDirectory, shell aliases that cd first) can't shift the
// device identity.
func ResolveDataDir() (DataDir, error) {
	raw := os.Getenv(DataDirEnv)
	if raw == "" {
		raw = defaultDataDir
	}
	abs, err := filepath.Abs(raw)
	if err != nil {
		return "", err
	}
	return DataDir(abs), nil
}

// ConfigPath returns the absolute path to config.json.
func (d DataDir) ConfigPath() string { return filepath.Join(string(d), "config.json") }

// SecretsDir returns the private directory matrixbot uses for tokens,
// encryption keys, and other non-operator-editable files.
func (d DataDir) SecretsDir() string { return filepath.Join(string(d), ".secrets") }

// SecretPath returns a path inside SecretsDir.
func (d DataDir) SecretPath(name string) string {
	return filepath.Join(d.SecretsDir(), safeSecretSegment(name))
}

// ExtensionSecretPath returns a stable secret path for a room extension.
func (d DataDir) ExtensionSecretPath(roomID, extension, name string) string {
	sum := sha256.Sum256([]byte(roomID))
	return filepath.Join(d.SecretsDir(), "extensions", hex.EncodeToString(sum[:])[:16], safeSecretSegment(extension), safeSecretSegment(name))
}

// SessionPath returns the absolute path to session.json.
func (d DataDir) SessionPath() string { return d.SecretPath("session.json") }

// AccountPath returns the absolute path to account.json.
func (d DataDir) AccountPath() string { return d.SecretPath("account.json") }

// CryptoDBPath returns the absolute path to crypto.db (the SQLite file
// itself; sidecars are at CryptoDBPaths).
func (d DataDir) CryptoDBPath() string { return d.SecretPath("crypto.db") }

// SchedulePath returns the absolute path to schedule.json, where the
// scheduler persists each schedule's next-fire time so a restart picks up
// where it left off.
func (d DataDir) SchedulePath() string { return filepath.Join(string(d), "schedule.json") }

// CryptoDBPaths returns the SQLite DB path and its -wal / -shm sidecars.
// SQLite in WAL mode creates the sidecars; wiping all three together is
// what logout needs — leaving any one behind keeps a stale identity.
func (d DataDir) CryptoDBPaths() []string {
	db := d.CryptoDBPath()
	return []string{db, db + "-wal", db + "-shm"}
}

func safeSecretSegment(s string) string {
	if s != "" && s != "." && s != ".." {
		ok := true
		for _, r := range s {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' || r == '.' {
				continue
			}
			ok = false
			break
		}
		if ok {
			return s
		}
	}
	sum := sha256.Sum256([]byte(s))
	return "sha256-" + hex.EncodeToString(sum[:])[:16]
}
