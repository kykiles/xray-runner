//go:build windows

package service

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/eventlog"
)

// Name is the service's name with the service control manager.
const Name = "xray-runner"

// runPlatform runs under the service control manager, or in the foreground
// when started from a console (for a look at what it does).
func runPlatform(version string) error {
	closeLog := openServiceLog()
	defer closeLog()
	isService, err := svc.IsWindowsService()
	if err != nil || !isService {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		return runService(ctx, version)
	}
	return svc.Run(Name, &handler{version: version})
}

type handler struct{ version string }

func (h *handler) Execute(_ []string, req <-chan svc.ChangeRequest, st chan<- svc.Status) (bool, uint32) {
	st <- svc.Status{State: svc.StartPending}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runService(ctx, h.version) }()
	st <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	for {
		select {
		case c := <-req:
			switch c.Cmd {
			case svc.Interrogate:
				st <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				st <- svc.Status{State: svc.StopPending}
				cancel()
				<-done
				return false, 0
			}
		case err := <-done:
			if err != nil {
				return false, 1
			}
			return false, 0
		}
	}
}

// LogDir is where the service writes service.log: in its own folder under
// Program Files, which only administrators write. Under ProgramData, where
// the log used to be, any user may make the folder first — or a junction in
// its place, to have SYSTEM set its security on and write into a folder of
// the user's choosing.
func LogDir() string { return filepath.Join(InstallDir(), "log") }

// geoStoreDir is where the service keeps the geo databases interfaces hand
// it: in its folder under Program Files, which no user writes.
func geoStoreDir() string { return filepath.Join(InstallDir(), "geo") }

// logDirSDDL keeps the service's log to SYSTEM and the administrators, who
// own it: the core's output is there, and nothing is inherited from Program
// Files, which lets users read.
const logDirSDDL = "O:BAD:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)"

// trustedOwner reports whether the log folder's owner is SYSTEM or the
// administrators. A variable so tests, which own their folders, can pass.
var trustedOwner = func(owner *windows.SID) bool {
	return owner.IsWellKnown(windows.WinLocalSystemSid) || owner.IsWellKnown(windows.WinBuiltinAdministratorsSid)
}

// errLogDirLink is a log folder that is a junction or a link, not a folder.
var errLogDirLink = errors.New("это junction или ссылка, а не папка")

// openLogDir opens dir itself — not what it may point to — and checks that it
// is a folder of SYSTEM's or the administrators'. Held without
// FILE_SHARE_DELETE, the handle keeps the folder from being renamed or
// replaced while it is open.
func openLogDir(dir string, access uint32) (windows.Handle, error) {
	name, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return windows.InvalidHandle, err
	}
	h, err := windows.CreateFile(name, access|windows.READ_CONTROL|windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return windows.InvalidHandle, err
	}
	if err := checkLogDir(h); err != nil {
		_ = windows.CloseHandle(h)
		return windows.InvalidHandle, fmt.Errorf("%s: %w", dir, err)
	}
	return h, nil
}

func checkLogDir(h windows.Handle) error {
	var fi windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &fi); err != nil {
		return err
	}
	if fi.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return errLogDirLink
	}
	if fi.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 {
		return errors.New("это не папка")
	}
	sd, err := windows.GetSecurityInfo(h, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return err
	}
	if !trustedOwner(owner) {
		return fmt.Errorf("владелец %s, а не SYSTEM или администраторы", owner)
	}
	return nil
}

// makeLogDir makes dir the service's log folder, with logDirSDDL. A junction
// or link in its place is removed — the link, not what it points to; a
// folder of someone else's is left alone, and the install stops.
func makeLogDir(dir string) error {
	h, err := openLogDir(dir, windows.WRITE_DAC)
	if err == nil {
		defer func() { _ = windows.CloseHandle(h) }()
		return setLogDirDACL(h)
	}
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case errors.Is(err, errLogDirLink):
		if err := os.Remove(dir); err != nil {
			return fmt.Errorf("папка лога службы %s — ссылка, и она не удалена: %w", dir, err)
		}
	default:
		return fmt.Errorf("папка лога службы не годится (удалите её и повторите установку): %w", err)
	}
	sd, err := windows.SecurityDescriptorFromString(logDirSDDL)
	if err != nil {
		return err
	}
	name, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return err
	}
	sa := &windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}
	if err := windows.CreateDirectory(name, sa); err != nil {
		return fmt.Errorf("папка лога службы %s: %w", dir, err)
	}
	h, err = openLogDir(dir, 0)
	if err != nil {
		return err
	}
	return windows.CloseHandle(h)
}

// setLogDirDACL puts logDirSDDL's DACL on the folder open as h.
func setLogDirDACL(h windows.Handle) error {
	sd, err := windows.SecurityDescriptorFromString(logDirSDDL)
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	return windows.SetSecurityInfo(h, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
}

// openServiceLog points slog at service.log in LogDir. The folder is checked
// before anything is done in it; one that does not pass gets no log, and the
// reason goes to the Windows event log instead.
func openServiceLog() func() {
	dir := LogDir()
	h, err := openLogDir(dir, 0)
	if errors.Is(err, fs.ErrNotExist) {
		// Installed before the folder was the install's to make. Program Files
		// is the administrators', so nobody else can have been here first.
		if err = makeLogDir(dir); err == nil {
			h, err = openLogDir(dir, 0)
		}
	}
	if err != nil {
		logElsewhere(fmt.Sprintf("лог службы не открыт, служба работает без него: %v", err))
		return func() {}
	}
	f, err := os.OpenFile(filepath.Join(dir, "service.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		_ = windows.CloseHandle(h)
		logElsewhere(fmt.Sprintf("лог службы не открыт, служба работает без него: %v", err))
		return func() {}
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(f, &slog.HandlerOptions{Level: slog.LevelInfo})))
	return func() {
		_ = f.Close()
		_ = windows.CloseHandle(h)
	}
}

// logElsewhere says what went wrong with the log where an administrator will
// find it: the Application event log, and stderr for a run from a console.
func logElsewhere(msg string) {
	slog.Warn(msg)
	if l, err := eventlog.Open(Name); err == nil {
		_ = l.Warning(1, msg)
		_ = l.Close()
	}
}

// listenerUID has no meaning here: the split is built on the tunnel.
func listenerUID(int) (int, bool) { return 0, false }
