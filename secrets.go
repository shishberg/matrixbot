package matrixbot

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

func migrateLegacySecrets(dd DataDir) error {
	migrations := map[string]string{
		filepath.Join(string(dd), "session.json"):  dd.SessionPath(),
		filepath.Join(string(dd), "account.json"):  dd.AccountPath(),
		filepath.Join(string(dd), "crypto.db"):     dd.CryptoDBPath(),
		filepath.Join(string(dd), "crypto.db-wal"): dd.CryptoDBPath() + "-wal",
		filepath.Join(string(dd), "crypto.db-shm"): dd.CryptoDBPath() + "-shm",
	}
	for oldPath, newPath := range migrations {
		if err := migrateLegacySecretFile(oldPath, newPath); err != nil {
			return err
		}
	}
	return nil
}

func migrateLegacySecretFile(oldPath, newPath string) error {
	if _, err := os.Stat(newPath); err == nil {
		return nil
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("checking %s: %w", newPath, err)
	}
	info, err := os.Stat(oldPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("checking %s: %w", oldPath, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("legacy secret %s is not a regular file", oldPath)
	}
	if err := ensurePrivateDir(filepath.Dir(newPath)); err != nil {
		return err
	}
	if err := os.Rename(oldPath, newPath); err != nil {
		return fmt.Errorf("moving %s -> %s: %w", oldPath, newPath, err)
	}
	if err := os.Chmod(newPath, 0o600); err != nil {
		return fmt.Errorf("tightening %s: %w", newPath, err)
	}
	return nil
}
