package safefile

import (
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
)

func readFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}

// writeFile replaces through a temp file so a crash or a full disk leaves the
// previous contents rather than a truncated file. os.Rename replaces an
// existing file on Windows too, though Go promises no atomicity for it there.
func writeFile(path string, data []byte, perm os.FileMode) error {
	tmp := filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+"-"+rand.Text()+".tmp")
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return fmt.Errorf("временный файл для %s: %w", filepath.Base(path), err)
	}
	if err := fill(f, data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func fill(f *os.File, data []byte) error {
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	return f.Close()
}
