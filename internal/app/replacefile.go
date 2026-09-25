package app

import (
	"crypto/rand"
	"errors"
	"fmt"
	"os"
)

// The steps of replaceFile, swapped by tests to fail one of them.
var (
	stagedWrite   = (*os.File).Write
	stagedSync    = (*os.File).Sync
	stagedClose   = (*os.File).Close
	publishStaged = (*os.Root).Rename
)

// replaceInRoot puts data under name directly inside r as a new file staged
// next to it (0600 on Unix), instead of writing into whatever name already
// holds. A link planted at name is replaced, not followed, so the file on its
// other end keeps its bytes (A04). Nothing at name changes before the final
// rename: a failure at any step leaves the previous file intact, and the temp
// this call made goes with it. Staging, rename and cleanup all go through r, so
// the directory cannot be swapped for a link between them. The rename is
// atomic on Unix only — Go promises no atomicity for it on Windows. own, when
// set, runs on the staged file before it is published — the place to hand it
// over by descriptor rather than by name afterwards.
func replaceInRoot(r *os.Root, name string, data []byte, own func(*os.File) error) (err error) {
	// O_EXCL: the staged name is never a file or link that was already there.
	tmp := "." + name + "-" + rand.Text() + ".tmp"
	f, err := r.OpenFile(tmp, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	published := false
	defer func() {
		if published {
			return
		}
		_ = f.Close() // already closed when only the rename failed
		if rmErr := r.Remove(tmp); rmErr != nil {
			err = errors.Join(err, fmt.Errorf("remove temp: %w", rmErr))
		}
	}()

	if _, err = stagedWrite(f, data); err != nil {
		return fmt.Errorf("write temp: %w", err)
	}
	if own != nil {
		if err = own(f); err != nil {
			return fmt.Errorf("owner temp: %w", err)
		}
	}
	// Without the sync the rename can land before the bytes do, leaving an empty
	// file after a power loss.
	if err = stagedSync(f); err != nil {
		return fmt.Errorf("sync temp: %w", err)
	}
	if err = stagedClose(f); err != nil {
		return fmt.Errorf("close temp: %w", err)
	}
	if err = publishStaged(r, tmp, name); err != nil {
		return fmt.Errorf("replace: %w", err)
	}
	published = true
	return nil
}
