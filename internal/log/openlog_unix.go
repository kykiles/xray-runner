//go:build !windows

package log

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"

	"xray-runner/internal/system"
)

// sudoOwner is the user behind a sudo run; a var so a test can play one
// without being root.
var sudoOwner = system.SudoOwner

// openLog opens this run's log, emptied, never through a symlink at the final
// component: the tool runs as root in TUN mode while the log path lives where
// the user can write, so a planted link would otherwise pick the file root
// truncates.
//
// Under sudo the file is about to be handed to the invoking user (Init), and
// the path may be theirs to choose — LOG_FILE in the .env of whatever directory
// they started from. So root takes only a file that is already theirs to
// replace: one they own, or one in a directory they own. A file with a second
// hard link is refused too, since truncating it empties the other name as well.
// Anything refused is left exactly as it was (G01).
func openLog(path string) (*os.File, error) {
	uid, _, ok := sudoOwner()
	if !ok {
		return os.OpenFile(path, os.O_TRUNC|os.O_CREATE|os.O_WRONLY|unix.O_NOFOLLOW, 0600)
	}
	f, err := openForUser(path, uid)
	if err != nil {
		return nil, err
	}
	if err := f.Truncate(0); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

// openForUser opens path for writing, creating it if needed, when uid may have
// it; see openLog. The checks are made on the directory and the file actually
// opened, by descriptor, so nothing can be swapped between a check and its use.
func openForUser(path string, uid int) (*os.File, error) {
	dir, name := filepath.Split(path)
	if dir == "" {
		dir = "."
	}
	dfd, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: dir, Err: err}
	}
	defer func() { _ = unix.Close(dfd) }()
	var dst unix.Stat_t
	if err := unix.Fstat(dfd, &dst); err != nil {
		return nil, &os.PathError{Op: "stat", Path: dir, Err: err}
	}

	// O_NONBLOCK keeps a FIFO planted under the name from blocking the open;
	// a regular file is unaffected, and anything else is refused below.
	const flags = unix.O_WRONLY | unix.O_NOFOLLOW | unix.O_NOCTTY | unix.O_NONBLOCK | unix.O_CLOEXEC
	created := true
	fd, err := unix.Openat(dfd, name, flags|unix.O_CREAT|unix.O_EXCL, 0600)
	if errors.Is(err, unix.EEXIST) {
		created = false
		fd, err = unix.Openat(dfd, name, flags, 0)
	}
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}

	refuse := func(why string) (*os.File, error) {
		_ = unix.Close(fd)
		if created {
			_ = unix.Unlinkat(dfd, name, 0)
		}
		return nil, fmt.Errorf("лог-файл %s не открыт: %s", path, why)
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return refuse(err.Error())
	}
	switch {
	case st.Mode&unix.S_IFMT != unix.S_IFREG:
		return refuse("это не обычный файл")
	case st.Nlink != 1:
		return refuse("у файла есть другие жёсткие ссылки")
	case int(st.Uid) != uid && int(dst.Uid) != uid:
		return refuse(fmt.Sprintf("ни файл, ни каталог не принадлежат пользователю uid %d, запустившему sudo", uid))
	}
	if err := unix.SetNonblock(fd, false); err != nil {
		return refuse(err.Error())
	}
	return os.NewFile(uintptr(fd), path), nil
}
