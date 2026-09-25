//go:build windows

package service

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
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

// logDirSDDL keeps the service's log to SYSTEM and the administrators: the
// core's output is there, and ProgramData lets users read by default.
const logDirSDDL = "D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)"

// openServiceLog points slog at %ProgramData%\xray-runner\service.log.
func openServiceLog() func() {
	dir := filepath.Join(os.Getenv("ProgramData"), "xray-runner")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return func() {}
	}
	if sd, err := windows.SecurityDescriptorFromString(logDirSDDL); err == nil {
		if dacl, _, err := sd.DACL(); err == nil {
			_ = windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT,
				windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
		}
	}
	f, err := os.OpenFile(filepath.Join(dir, "service.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return func() {}
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(f, &slog.HandlerOptions{Level: slog.LevelInfo})))
	return func() { _ = f.Close() }
}

// listenerUID has no meaning here: the split is built on the tunnel.
func listenerUID(int) (int, bool) { return 0, false }
