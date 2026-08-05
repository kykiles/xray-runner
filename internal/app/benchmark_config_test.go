package app

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"xray-runner/internal/subscription"
)

// mockCore builds a stand-in xray. It never opens a port, so the measurement
// always reaches the "core not ready" branch; rejectConfig decides what
// `xray run -test` answers there, which is the whole thing under test.
func mockCore(t *testing.T, rejectConfig bool) string {
	t.Helper()

	verdict := `os.Exit(0)`
	if rejectConfig {
		verdict = `os.Stderr.WriteString("infra/conf: unknown outbound protocol \"vless-next\"\n"); os.Exit(23)`
	}
	src := `package main

import (
	"os"
	"time"
)

func main() {
	for _, a := range os.Args[1:] {
		if a == "-test" {
			` + verdict + `
		}
	}
	// Started for real: stay alive, but never listen on anything.
	time.Sleep(30 * time.Second)
}
`
	dir := t.TempDir()
	mainPath := filepath.Join(dir, "main.go")
	if err := os.WriteFile(mainPath, []byte(src), 0644); err != nil {
		t.Fatalf("write mock source: %v", err)
	}
	name := "xray"
	if runtime.GOOS == "windows" {
		name = "xray.exe"
	}
	out := filepath.Join(dir, name)
	if b, err := exec.Command("go", "build", "-o", out, mainPath).CombinedOutput(); err != nil {
		t.Fatalf("build mock xray: %v\n%s", err, b)
	}
	return out
}

// Task #3: a core whose port never opened is two different problems wearing one
// label. The config the panel shipped may need a newer Xray than the installed
// one — waiting longer never fixes that, so it must not read as "start".
func TestRunAndMeasure_SeparatesRejectedConfigFromSlowStart(t *testing.T) {
	// A port nothing listens on: the mock core deliberately never binds.
	ports, err := freePortPair()
	if err != nil {
		t.Fatalf("freePortPair: %v", err)
	}

	cases := []struct {
		name    string
		reject  bool
		wantErr error
		wantS   string
	}{
		{"core rejected the config", true, subscription.ErrConfigRejected, "config"},
		{"core just did not come up", false, subscription.ErrCoreNotReady, "start"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pb := &ProxyBenchmarker{xrayBinary: mockCore(t, c.reject), timeout: 300 * time.Millisecond}
			res := pb.runAndMeasure(context.Background(), []byte(`{}`), ports, t.TempDir())

			if !errors.Is(res.Error, c.wantErr) {
				t.Fatalf("error = %v, want %v", res.Error, c.wantErr)
			}
			if got := res.String(); got != c.wantS {
				t.Errorf("ping label = %q, want %q", got, c.wantS)
			}
		})
	}
}

// A rejected config keeps what the core said, so the log can answer "why".
func TestRunAndMeasure_RejectedConfigCarriesCoreOutput(t *testing.T) {
	ports, err := freePortPair()
	if err != nil {
		t.Fatalf("freePortPair: %v", err)
	}

	pb := &ProxyBenchmarker{xrayBinary: mockCore(t, true), timeout: 300 * time.Millisecond}
	res := pb.runAndMeasure(context.Background(), []byte(`{}`), ports, t.TempDir())

	if res.Error == nil || !strings.Contains(res.Error.Error(), "vless-next") {
		t.Errorf("error lost the core's own words: %v", res.Error)
	}
}
