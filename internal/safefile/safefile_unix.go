//go:build !windows

package safefile

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"

	"golang.org/x/sys/unix"
)

// readFile opens the leaf with O_NOFOLLOW, so a symlink there is refused rather
// than read through, and reads a regular file only.
func readFile(path string) ([]byte, error) {
	// O_NONBLOCK keeps a FIFO under the name from wedging the open; a regular
	// file ignores it, and anything else is refused below.
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		if errors.Is(err, unix.ELOOP) {
			return nil, fmt.Errorf("%s — символическая ссылка, чтение отклонено", path)
		}
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	f := os.NewFile(uintptr(fd), path)
	defer func() { _ = f.Close() }()

	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s — не обычный файл, чтение отклонено", path)
	}
	if err := unix.SetNonblock(fd, false); err != nil {
		return nil, err
	}
	return io.ReadAll(f)
}

// writeFile stages the data in a temp file created O_EXCL next to path and
// renames it over path: no fixed name, and no link planted under one, is ever
// opened for writing, and the replace is atomic.
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

// fill writes, hands over, syncs and closes the staged file.
func fill(f *os.File, data []byte) error {
	if _, err := f.Write(data); err != nil {
		return err
	}
	if uid, gid, ok := sudoOwner(); ok {
		if err := f.Chown(uid, gid); err != nil {
			return err
		}
	}
	// Without the sync the rename can land before the bytes do, leaving an
	// empty file after a power loss.
	if err := f.Sync(); err != nil {
		return err
	}
	return f.Close()
}

// sudoOwner is the user behind a sudo run as root; a var so a test can see the
// handover without being root.
var sudoOwner = func() (uid, gid int, ok bool) {
	if os.Geteuid() != 0 {
		return 0, 0, false
	}
	uid, err1 := strconv.Atoi(os.Getenv("SUDO_UID"))
	gid, err2 := strconv.Atoi(os.Getenv("SUDO_GID"))
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	return uid, gid, true
}
