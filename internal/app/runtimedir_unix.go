//go:build !windows

package app

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// geteuid is os.Geteuid; a var so a test can see a root run refuse a base the
// user owns without being root.
var geteuid = os.Geteuid

// createRuntimeDir makes this instance's runtime dir: a new 0700 directory in
// the temp dir, which must be one that only root or this process's user can
// change (11b). Root does not count the user in: under sudo the data dir is the
// user's on purpose, and so may be a TMPDIR that sudo -E carried over.
func createRuntimeDir() (string, error) {
	base, err := filepath.EvalSymlinks(os.TempDir())
	if err != nil {
		return "", fmt.Errorf("каталог для рабочего конфига: %w", err)
	}
	if err := checkTrustedDirs(base, uint32(geteuid())); err != nil {
		return "", err
	}
	return os.MkdirTemp(base, "xray-runner-*")
}

// checkTrustedDirs refuses a base that someone other than root or euid could
// rename, replace or put a config of their own into: it and every directory
// above it must belong to one of the two and be writable to nobody else —
// unless sticky, where others may add entries but not touch ours. None of it
// can then change after the check, so checking the path is enough.
func checkTrustedDirs(base string, euid uint32) error {
	for dir := base; ; dir = filepath.Dir(dir) {
		fi, err := os.Lstat(dir)
		if err != nil {
			return fmt.Errorf("каталог для рабочего конфига %s: %w", base, err)
		}
		st, ok := fi.Sys().(*syscall.Stat_t)
		switch {
		case !ok || !fi.IsDir():
			return untrustedBase(base, dir, "не каталог")
		case st.Uid != 0 && st.Uid != euid:
			return untrustedBase(base, dir, fmt.Sprintf("владелец uid %d", st.Uid))
		case fi.Mode().Perm()&0o022 != 0 && fi.Mode()&os.ModeSticky == 0:
			return untrustedBase(base, dir, "в него могут писать другие пользователи")
		}
		if dir == filepath.Dir(dir) {
			return nil
		}
	}
}

func untrustedBase(base, dir, why string) error {
	return fmt.Errorf("каталог для рабочего конфига %s ненадёжен: %s — %s; укажите в TMPDIR каталог, который могут менять только root и владелец процесса", base, dir, why)
}
