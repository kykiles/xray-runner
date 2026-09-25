//go:build windows

package service

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"

	"xray-runner/internal/ipc"
)

// InstallDir is where the service's program and core live: under Program
// Files, which only administrators write.
func InstallDir() string {
	base := os.Getenv("ProgramFiles")
	if base == "" {
		base = `C:\Program Files`
	}
	return filepath.Join(base, "xray-runner")
}

func install() error {
	if !windows.GetCurrentProcessToken().IsElevated() {
		return fmt.Errorf("%w: запустите «xray-runner service install» от имени администратора", errNotAdmin)
	}
	p, err := findPayload()
	if err != nil {
		return err
	}
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer func() { _ = m.Disconnect() }()

	// The running service holds the old binaries; the new ones go in once it
	// has let them go.
	if s, err := m.OpenService(Name); err == nil {
		_ = stopService(s)
		_ = s.Close()
	}
	dir := InstallDir()
	if err := p.copyInto(dir); err != nil {
		return err
	}
	exe := filepath.Join(dir, programFile())
	if err := makeLogDir(LogDir()); err != nil {
		return err
	}
	if err := ipc.CreateUsersGroup(); err != nil {
		return err
	}
	grant := addInstallingUser()

	s, err := m.OpenService(Name)
	if err == nil {
		cfg, err := s.Config()
		if err == nil {
			cfg.BinaryPathName = fmt.Sprintf(`"%s" service run`, exe)
			cfg.StartType = mgr.StartAutomatic
			err = s.UpdateConfig(cfg)
		}
		if err != nil {
			_ = s.Close()
			return err
		}
	} else {
		s, err = m.CreateService(Name, exe, mgr.Config{
			DisplayName: "xray-runner",
			Description: "TUN, kill switch and per-process routing for xray-runner, without running the interface as administrator.",
			StartType:   mgr.StartAutomatic,
		}, "service", "run")
		if err != nil {
			return err
		}
	}
	defer func() { _ = s.Close() }()
	// A crashed service comes back; its start takes down what it left (H06).
	_ = s.SetRecoveryActions([]mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 2 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 30 * time.Second},
	}, 24*60*60)
	if err := s.Start(); err != nil {
		return fmt.Errorf("служба установлена, но не запустилась: %w", err)
	}
	fmt.Printf("Служба установлена в %s и запущена. TUN теперь работает без запуска от имени администратора.\n", dir)
	fmt.Println(grant)
	return confirm()
}

// stopService stops s and waits until it has.
func stopService(s *mgr.Service) error {
	st, err := s.Control(svc.Stop)
	if err != nil {
		return err
	}
	deadline := time.Now().Add(60 * time.Second)
	for st.State != svc.Stopped {
		if time.Now().After(deadline) {
			return errors.New("служба не остановилась за минуту")
		}
		time.Sleep(300 * time.Millisecond)
		if st, err = s.Query(); err != nil {
			return err
		}
	}
	return nil
}

func uninstall() error {
	if !windows.GetCurrentProcessToken().IsElevated() {
		return fmt.Errorf("%w: запустите «xray-runner service uninstall» от имени администратора", errNotAdmin)
	}
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer func() { _ = m.Disconnect() }()
	if s, err := m.OpenService(Name); err == nil {
		// Stopping takes the session down: the service leaves the machine as
		// it found it on the way out.
		_ = stopService(s)
		err := s.Delete()
		_ = s.Close()
		if err != nil {
			return err
		}
	}
	if err := ipc.DeleteUsersGroup(); err != nil {
		return err
	}
	// The log goes with the folder.
	if err := os.RemoveAll(InstallDir()); err != nil {
		return err
	}
	fmt.Println("Служба удалена.")
	return nil
}

// installed says what is installed.
func installed() {
	m, err := mgr.Connect()
	if err != nil {
		fmt.Println("Диспетчер служб недоступен:", err)
		return
	}
	defer func() { _ = m.Disconnect() }()
	s, err := m.OpenService(Name)
	if err != nil {
		fmt.Println("Служба не установлена (от имени администратора: xray-runner service install).")
		return
	}
	defer func() { _ = s.Close() }()
	cfg, _ := s.Config()
	fmt.Printf("Служба установлена: %s\n", cfg.BinaryPathName)
}

// addInstallingUser puts the user the install is for in ipc.UsersGroup and
// says what it did. That is the user logged in to this session, who may have
// elevated with another account's password — not that account, which is
// an administrator and needs no group. Without a session user (a remote shell,
// a SYSTEM prompt) it is this process's own user, unless that is SYSTEM.
func addInstallingUser() string {
	howTo := fmt.Sprintf("Другого пользователя добавит администратор: net localgroup \"%s\" <имя> /add", ipc.UsersGroup)
	sid, name, err := sessionUser()
	if err != nil {
		tu, terr := windows.GetCurrentProcessToken().GetTokenUser()
		if terr != nil || tu.User.Sid.IsWellKnown(windows.WinLocalSystemSid) {
			return fmt.Sprintf("Службой пользуются администраторы и члены группы «%s»; кто устанавливает, не определено (%v).\n%s",
				ipc.UsersGroup, err, howTo)
		}
		sid, name = tu.User.Sid, tu.User.Sid.String()
		if account, domain, _, err := sid.LookupAccount(""); err == nil {
			name = domain + `\` + account
		}
	}
	if err := ipc.AddToUsersGroup(sid); err != nil {
		return fmt.Sprintf("%v.\n%s", err, howTo)
	}
	return fmt.Sprintf("Службой пользуются администраторы и члены группы «%s»; %s добавлен в неё, входить заново не нужно.\n%s",
		ipc.UsersGroup, name, howTo)
}

var (
	wtsapi32                        = windows.NewLazySystemDLL("wtsapi32.dll")
	procWTSQuerySessionInformationW = wtsapi32.NewProc("WTSQuerySessionInformationW")
)

// sessionUser is the user logged in to this process's session.
func sessionUser() (*windows.SID, string, error) {
	const (
		currentSession = 0xFFFFFFFF // WTS_CURRENT_SESSION
		wtsUserName    = 5
		wtsDomainName  = 7
	)
	query := func(class uintptr) (string, error) {
		var buf *uint16
		var n uint32
		r, _, err := procWTSQuerySessionInformationW.Call(0, currentSession, class,
			uintptr(unsafe.Pointer(&buf)), uintptr(unsafe.Pointer(&n))) //nolint:gosec // G103: pointer arguments, converted in the call so they stay put
		if r == 0 {
			return "", err
		}
		defer windows.WTSFreeMemory(uintptr(unsafe.Pointer(buf))) //nolint:gosec // G103: the buffer the query allocated
		return windows.UTF16PtrToString(buf), nil
	}
	user, err := query(wtsUserName)
	if err != nil {
		return nil, "", err
	}
	if user == "" {
		return nil, "", errors.New("в этом сеансе никто не вошёл")
	}
	domain, err := query(wtsDomainName)
	if err != nil {
		return nil, "", err
	}
	name := domain + `\` + user
	sid, _, _, err := windows.LookupSID("", name)
	if err != nil {
		return nil, "", err
	}
	return sid, name, nil
}
