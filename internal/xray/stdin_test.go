package xray

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// buildStdinXray builds a mock core that records its arguments and what it
// read from stdin into the file named by MOCK_XRAY_SEEN, then exits.
func buildStdinXray(t *testing.T) string {
	t.Helper()
	src := `package main

import (
	"io"
	"os"
	"strings"
)

func main() {
	in, _ := io.ReadAll(os.Stdin)
	_ = os.WriteFile(os.Getenv("MOCK_XRAY_SEEN"), []byte(strings.Join(os.Args[1:], " ")+"\n"+string(in)), 0o600)
}
`
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	name := "xray"
	if runtime.GOOS == "windows" {
		name = "xray.exe"
	}
	out := filepath.Join(dir, name)
	if b, err := exec.Command("go", "build", "-o", out, filepath.Join(dir, "main.go")).CombinedOutput(); err != nil {
		t.Fatalf("build mock xray: %v\n%s", err, b)
	}
	return out
}

// The core gets its config on stdin, both to run and to test it: no copy of
// the server's keys is written to disk for it to read (H01).
func TestRunner_HandsTheConfigOverStdin(t *testing.T) {
	binary := buildStdinXray(t)
	config := []byte(`{"outbounds":[{"protocol":"vless","settings":"secret"}]}`)

	for name, run := range map[string]func(*Runner) error{
		"run": func(r *Runner) error {
			if err := r.Start(context.Background()); err != nil {
				return err
			}
			return r.Wait()
		},
		"test": func(r *Runner) error { return r.TestConfig(context.Background()) },
	} {
		t.Run(name, func(t *testing.T) {
			seen := filepath.Join(t.TempDir(), "seen")
			t.Setenv("MOCK_XRAY_SEEN", seen)

			if err := run(New(binary, config)); err != nil {
				t.Fatalf("%s: %v", name, err)
			}

			data, err := os.ReadFile(seen)
			if err != nil {
				t.Fatal(err)
			}
			args, in, _ := strings.Cut(string(data), "\n")
			if !strings.HasSuffix(args, "-c stdin:") {
				t.Errorf("core args %q, want the config read from stdin", args)
			}
			if in != string(config) {
				t.Errorf("core read %q on stdin, want the config", in)
			}
		})
	}
}
