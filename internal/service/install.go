package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"xray-runner/internal/ipc"
	"xray-runner/internal/xray"
)

// payload is what an install copies: the program itself, and the core with
// its geo databases (and, on Windows, wintun.dll) from wherever this run
// finds them. The service runs only what is in its own folder, which only an
// administrator or root may write: the user can then not swap what runs with
// the service's rights.
type payload struct {
	exe  string   // this program
	core string   // the core
	aux  []string // beside the core: geo databases, wintun.dll
}

// auxFiles sit next to the core and go with it.
var auxFiles = []string{"geoip.dat", "geosite.dat", "wintun.dll"}

func findPayload() (payload, error) {
	exe, err := os.Executable()
	if err != nil {
		return payload{}, err
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return payload{}, err
	}
	core, err := xray.FindBinary()
	if err != nil {
		return payload{}, err
	}
	p := payload{exe: exe, core: core}
	for _, name := range auxFiles {
		path := filepath.Join(filepath.Dir(core), name)
		if _, err := os.Stat(path); err == nil {
			p.aux = append(p.aux, path)
		}
	}
	return p, nil
}

// copyInto copies the payload into dir: the program at its top, the core and
// its files in bin/, the layout FindBinary looks in. Each file is written
// beside its final name and renamed over it, so a failed copy leaves the old
// one in place.
func (p payload) copyInto(dir string) error {
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o755); err != nil { //nolint:gosec // G301: program files, readable by all
		return err
	}
	files := map[string]string{p.exe: filepath.Join(dir, programFile()), p.core: filepath.Join(dir, "bin", filepath.Base(p.core))}
	for _, a := range p.aux {
		files[a] = filepath.Join(dir, "bin", filepath.Base(a))
	}
	for src, dst := range files {
		if err := copyFile(src, dst); err != nil {
			return fmt.Errorf("копирование %s: %w", filepath.Base(src), err)
		}
	}
	return nil
}

// programFile is the installed program's name, whatever the copy it came
// from was called: the unit and the service's command line name it.
func programFile() string {
	if runtime.GOOS == "windows" {
		return "xray-runner.exe"
	}
	return "xray-runner"
}

func copyFile(src, dst string) error {
	if same, _ := sameFile(src, dst); same {
		return nil
	}
	in, err := os.Open(src) //nolint:gosec // G304: the program's own files
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	mode := os.FileMode(0o644)
	if st, err := in.Stat(); err == nil && st.Mode()&0o111 != 0 {
		mode = 0o755
	}
	tmp := dst + ".new"
	_ = os.Remove(tmp)
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func sameFile(a, b string) (bool, error) {
	sa, err := os.Stat(a)
	if err != nil {
		return false, err
	}
	sb, err := os.Stat(b)
	if err != nil {
		return false, err
	}
	return os.SameFile(sa, sb), nil
}

// confirm waits for the freshly installed service to answer and says so.
func confirm() error {
	deadline := time.Now().Add(15 * time.Second)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		c, err := ipc.Connect(ctx, "")
		cancel()
		if err == nil {
			h := c.Hello()
			_ = c.Close()
			fmt.Printf("Служба отвечает: версия %s, ядро %s.\n", h.ServiceVersion, orDash(firstLine(h.CoreVersion)))
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("служба установлена, но не отвечает: %w", err)
		}
		time.Sleep(300 * time.Millisecond)
	}
}

// errNotAdmin is an install or uninstall without the rights for it.
var errNotAdmin = errors.New("нужны права администратора")
