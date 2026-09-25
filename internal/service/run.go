package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"xray-runner/internal/app"
	"xray-runner/internal/ipc"
	"xray-runner/internal/system"
	"xray-runner/internal/xray"
	"xray-runner/internal/xraycfg"
)

// Main is `xray-runner service <command>`; its result is the exit code.
func Main(args []string, version string) int {
	cmd := "status"
	if len(args) > 0 {
		cmd = args[0]
	}
	var err error
	switch cmd {
	case "run":
		err = runPlatform(version)
	case "status":
		err = printStatus()
	case "install":
		err = install()
	case "uninstall":
		err = uninstall()
	default:
		fmt.Fprintln(os.Stderr, "использование: xray-runner service run|install|uninstall|status")
		return 2
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

// runService is the service itself, until ctx ends.
func runService(ctx context.Context, version string) error {
	s, l, err := setUp(version)
	if err != nil {
		slog.Error("служба не запустилась", "error", err)
		return err
	}
	slog.Info("служба запущена", "version", version, "core", s.d.CoreVersion)
	err = s.Serve(ctx, l)
	slog.Info("служба остановлена", "error", err)
	return err
}

func setUp(version string) (*Service, ipc.Listener, error) {
	binary, err := ownCore()
	if err != nil {
		return nil, nil, err
	}
	xraycfg.SetGeoAssets(filepath.Dir(binary), "")
	coreVer, err := xray.Version(binary)
	if err != nil {
		slog.Warn("версия ядра не прочитана", "error", err)
	}

	// What runs that died without their teardown left — this service's own
	// earlier life, or an interface run under sudo before it — comes down
	// before anything new goes up (H06). Nothing new goes up if it would not:
	// the next start tries again.
	note, err := system.RecoverJournal()
	if err != nil {
		return nil, nil, fmt.Errorf("остатки прошлых запусков не сняты: %w", err)
	}
	if note != "" {
		slog.Warn(note)
	}

	l, err := ipc.Listen()
	if err != nil {
		return nil, nil, err
	}
	s := New(Deps{
		Tun: app.ServeTun,
		EnableSplit: func(names []string, uid int) (system.SplitScan, error) {
			return system.EnableSplitFor(names, xraycfg.RedirectPort, xraycfg.RedirectDNS, uid)
		},
		RefreshSplit: system.RefreshSplitFor,
		DisableSplit: system.DisableSplit,
		CloseConns:   system.CloseConns,
		ListenerUID:  listenerUID,
		Binary:       binary,
		CoreVersion:  strings.TrimSpace(firstLine(coreVer)),
		Version:      version,
	})
	slog.SetDefault(slog.New(&teeHandler{Handler: slog.Default().Handler(), fwd: s.forwardLog}))
	return s, l, nil
}

// ownCore is the core beside the service's binary. Nothing else is run with
// the service's rights: a core off PATH is whatever put it there.
func ownCore() (string, error) {
	binary, err := xray.FindBinary()
	if err != nil {
		return "", err
	}
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if !xray.Bundled(binary, filepath.Dir(exe)) {
		return "", fmt.Errorf("ядро %s не рядом со службой — переустановите её: xray-runner service install", binary)
	}
	return binary, nil
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}

// teeHandler logs as its inner handler does and hands each line to fwd too.
type teeHandler struct {
	slog.Handler
	fwd func(slog.Level, string)
}

func (h *teeHandler) Handle(ctx context.Context, r slog.Record) error {
	err := h.Handler.Handle(ctx, r)
	var b strings.Builder
	b.WriteString(r.Message)
	r.Attrs(func(a slog.Attr) bool {
		b.WriteString(" " + a.String())
		return true
	})
	h.fwd(r.Level, b.String())
	return err
}

func (h *teeHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &teeHandler{Handler: h.Handler.WithAttrs(attrs), fwd: h.fwd}
}

func (h *teeHandler) WithGroup(name string) slog.Handler {
	return &teeHandler{Handler: h.Handler.WithGroup(name), fwd: h.fwd}
}

// printStatus is `service status`: what is installed, and whether the
// service answers.
func printStatus() error {
	installed()
	c, err := ipc.Connect(context.Background(), "")
	if err != nil {
		fmt.Println("Служба не отвечает:", errors.Unwrap(err))
		return nil
	}
	defer func() { _ = c.Close() }()
	fmt.Printf("Служба отвечает: версия %s, ядро %s\n", c.Hello().ServiceVersion, orDash(c.Hello().CoreVersion))
	var st ipc.StatusReply
	if err := c.Call(context.Background(), ipc.TypeStatus, struct{}{}, &st); err != nil {
		return err
	}
	switch {
	case !st.Active:
		fmt.Println("Сессии нет.")
	case st.Mine:
		fmt.Printf("Идёт ваша сессия: %s %s\n", strings.ToUpper(st.Kind), st.Title)
	default:
		fmt.Printf("Идёт сессия другого пользователя: %s\n", strings.ToUpper(st.Kind))
	}
	return nil
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}
