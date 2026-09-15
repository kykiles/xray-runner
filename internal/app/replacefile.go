package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// The steps of replaceFile, swapped by tests to fail one of them.
var (
	stagedWrite   = (*os.File).Write
	stagedSync    = (*os.File).Sync
	stagedClose   = (*os.File).Close
	publishStaged = os.Rename
)

// replaceFile puts data under path as a new file staged next to it (0600 on
// Unix), instead of writing into whatever path already names. A link planted at
// path is replaced, not followed, so the file on its other end keeps its bytes
// (A04). Nothing at path changes before the final rename: a failure at any step
// leaves the previous file intact, and the temp this call made goes with it.
//
// The rename is atomic on Unix only — Go promises no atomicity for it on
// Windows — and it does not guard against a hostile parent directory.
func replaceFile(path string, data []byte) (err error) {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	published := false
	defer func() {
		if published {
			return
		}
		_ = f.Close() // already closed when only the rename failed
		if rmErr := os.Remove(f.Name()); rmErr != nil {
			err = errors.Join(err, fmt.Errorf("remove temp: %w", rmErr))
		}
	}()

	if _, err = stagedWrite(f, data); err != nil {
		return fmt.Errorf("write temp: %w", err)
	}
	// Without the sync the rename can land before the bytes do, leaving an empty
	// file after a power loss.
	if err = stagedSync(f); err != nil {
		return fmt.Errorf("sync temp: %w", err)
	}
	if err = stagedClose(f); err != nil {
		return fmt.Errorf("close temp: %w", err)
	}
	if err = publishStaged(f.Name(), path); err != nil {
		return fmt.Errorf("replace: %w", err)
	}
	published = true
	return nil
}
