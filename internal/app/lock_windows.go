//go:build windows

package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// processAlive reports whether a process with the given pid is currently
// running. Opening a handle is not enough: Windows keeps a pid valid while any
// handle to the dead process is still open, so an exited owner would look alive
// and a refusal would name it. The exit code is the answer —
// STILL_ACTIVE means the owner really is running.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	h, err := syscall.OpenProcess(syscall.PROCESS_QUERY_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer syscall.CloseHandle(h)

	var code uint32
	if err := syscall.GetExitCodeProcess(h, &code); err != nil {
		return false
	}
	return code == 259 // STILL_ACTIVE
}

// openLockFile opens the lock file for acquireLock, creating it if need be. An
// elevated run opens it in the user's own directory, where an unelevated
// process of the same user can plant a symlink or a hard link at its name, or
// swap the directory for a junction — no privilege needed for that one — to
// carry the pid write, and the truncate before it, to another file. So the
// directory is opened as it is and must be a real one, the file is opened
// relative to that handle without following a reparse point at its name, and
// anything but a plain file with one name is refused (11b).
func openLockFile(path string) (*os.File, error) {
	dir, err := openRealDir(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(dir)

	name, err := windows.NewNTUnicodeString(filepath.Base(path))
	if err != nil {
		return nil, err
	}
	oa := windows.OBJECT_ATTRIBUTES{RootDirectory: dir, ObjectName: name, Attributes: windows.OBJ_CASE_INSENSITIVE}
	oa.Length = uint32(unsafe.Sizeof(oa))
	var h windows.Handle
	var iosb windows.IO_STATUS_BLOCK
	// The access and sharing os.OpenFile gives O_WRONLY, plus reading the
	// attributes checked below.
	err = windows.NtCreateFile(&h, windows.GENERIC_WRITE|windows.FILE_READ_ATTRIBUTES|windows.SYNCHRONIZE,
		&oa, &iosb, nil, windows.FILE_ATTRIBUTE_NORMAL, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		windows.FILE_OPEN_IF, windows.FILE_NON_DIRECTORY_FILE|windows.FILE_SYNCHRONOUS_IO_NONALERT|windows.FILE_OPEN_REPARSE_POINT, 0, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	var fi windows.ByHandleFileInformation
	err = windows.GetFileInformationByHandle(h, &fi)
	switch {
	case err != nil:
	case fi.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0:
		err = fmt.Errorf("%s: это ссылка, а не файл", path)
	case fi.NumberOfLinks != 1:
		err = fmt.Errorf("%s: у файла есть другие имена (жёстких ссылок: %d)", path, fi.NumberOfLinks)
	}
	if err != nil {
		_ = windows.CloseHandle(h)
		return nil, err
	}
	return os.NewFile(uintptr(h), path), nil
}

// openRealDir opens the directory at path itself, not what a junction or
// symlink there points to, and refuses anything but a plain directory.
func openRealDir(path string) (windows.Handle, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	h, err := windows.CreateFile(p, windows.FILE_TRAVERSE|windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return 0, &os.PathError{Op: "open", Path: path, Err: err}
	}
	var fi windows.ByHandleFileInformation
	err = windows.GetFileInformationByHandle(h, &fi)
	if err == nil && (fi.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 || fi.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0) {
		err = fmt.Errorf("%s: не обычный каталог (ссылка или junction)", path)
	}
	if err != nil {
		_ = windows.CloseHandle(h)
		return 0, err
	}
	return h, nil
}

// lockOffset is where the one locked byte lies. Windows byte-range locks are
// mandatory: a lock over the pid would keep a refused instance from reading who
// holds it. Locking past the end of the file is allowed, and the pid never
// reaches that far.
const lockOffset = 1 << 30

// tryLock takes an exclusive lock on f without waiting; errLockBusy when another
// handle holds it. Windows lets the lock go when the handle closes, in a killed
// process too.
func tryLock(f *os.File) error {
	err := windows.LockFileEx(windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0, 1, 0, &windows.Overlapped{Offset: lockOffset})
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return errLockBusy
	}
	return err
}

// unlock releases the lock before the handle closes: on close alone Windows
// frees it only "depending upon available system resources" (LockFileEx docs).
func unlock(f *os.File) error {
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &windows.Overlapped{Offset: lockOffset})
}
