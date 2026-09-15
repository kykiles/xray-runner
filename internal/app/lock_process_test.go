package app

// The in-process lock tests show the lock's logic; only separate processes show
// that the OS keeps instances apart, and that a killed owner's lock frees
// itself (11a).

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

const lockHelperEnv = "XRAY_RUNNER_LOCK_HELPER"

// TestLockHelperProcess is not a test but one contender, started by the tests
// below with its directory in lockHelperEnv. It waits on stdin for the start,
// claims the install and says how it went on stdout. A winner writes its config,
// names it, and holds the lock until stdin closes; a loser cleans up and exits.
func TestLockHelperProcess(t *testing.T) {
	dir := os.Getenv(lockHelperEnv)
	if dir == "" {
		t.Skip("helper process for the lock tests")
	}
	// The contender's runtime dir is the parent's concern no more than its own:
	// a process of its own, it would take the machine's (an elevated run's is the
	// system temp) and be refused by that, not by the lock.
	isolateRuntime(t)
	a := newLockApp(dir)
	in := bufio.NewReader(os.Stdin)
	fmt.Println("ready")
	if _, err := in.ReadString('\n'); err != nil {
		os.Exit(2)
	}
	if err := a.claimInstance(); err != nil {
		fmt.Println("refused")
		a.cleanup()
		os.Exit(0)
	}
	if err := os.WriteFile(a.tmpFile, []byte(configOf(os.Getpid())), 0o600); err != nil {
		fmt.Println("error", err)
		os.Exit(2)
	}
	fmt.Println("won", a.tmpFile)
	_, _ = io.Copy(io.Discard, in)
	a.cleanup()
	os.Exit(0)
}

func configOf(pid int) string { return "config of " + strconv.Itoa(pid) }

type contender struct {
	cmd   *exec.Cmd
	start *os.File // the helper's stdin: a line starts it, closing it lets a winner go
	lines chan string
}

// startContender runs a helper on dir and waits until it is ready to go.
func startContender(t *testing.T, dir string) *contender {
	t.Helper()
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestLockHelperProcess$")
	cmd.Env = append(os.Environ(), lockHelperEnv+"="+dir)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = inR, outW, os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	inR.Close()
	outW.Close()

	c := &contender{cmd: cmd, start: inW, lines: make(chan string, 4)}
	go func() {
		defer outR.Close()
		sc := bufio.NewScanner(outR)
		for sc.Scan() {
			c.lines <- sc.Text()
		}
		close(c.lines)
	}()
	t.Cleanup(func() {
		inW.Close()
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	if got := c.next(t); got != "ready" {
		t.Fatalf("contender said %q, want ready", got)
	}
	return c
}

// next is the helper's next line, "exited" once it has closed stdout.
func (c *contender) next(t *testing.T) string {
	t.Helper()
	select {
	case l, ok := <-c.lines:
		if !ok {
			return "exited"
		}
		return l
	case <-time.After(30 * time.Second):
		t.Fatal("contender did not answer")
		return ""
	}
}

func (c *contender) wait(t *testing.T) {
	t.Helper()
	if err := c.cmd.Wait(); err != nil {
		t.Fatalf("contender: %v", err)
	}
}

// Contenders started together on one install: exactly one owns the lock, the
// others leave its config alone — once they are gone it is still where the
// owner's core reads it on a restart. When the owner is killed, with no
// cleanup, the next run gets the lock.
func TestAcquireLock_ProcessesContend(t *testing.T) {
	isolateRuntime(t)
	dir := t.TempDir()
	cs := make([]*contender, 8)
	for i := range cs {
		cs[i] = startContender(t, dir)
	}
	// All of them are blocked on stdin now; the barrier opens for all at once.
	for _, c := range cs {
		fmt.Fprintln(c.start, "go")
	}

	var winner *contender
	var config string
	for _, c := range cs {
		switch got := c.next(t); {
		case strings.HasPrefix(got, "won "):
			if winner != nil {
				t.Fatal("two processes hold the lock at once")
			}
			winner, config = c, strings.TrimPrefix(got, "won ")
		case got == "refused":
			c.wait(t)
		default:
			t.Fatalf("contender said %q", got)
		}
	}
	if winner == nil {
		t.Fatal("no process got the lock")
	}
	assertFileHolds(t, config, configOf(winner.cmd.Process.Pid))

	_ = winner.cmd.Process.Kill()
	_ = winner.cmd.Wait()
	// Killed, the owner leaves its runtime dir behind.
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Dir(config)) })

	// Linux lets a dead process's lock go by the time Wait returns; Windows only
	// promises to do it "depending on available system resources", so a refusal
	// right after the kill is retried for a while.
	deadline := time.Now().Add(10 * time.Second)
	for {
		next := startContender(t, dir)
		fmt.Fprintln(next.start, "go")
		got := next.next(t)
		if strings.HasPrefix(got, "won ") {
			break
		}
		if got != "refused" || time.Now().After(deadline) {
			t.Fatalf("after the owner was killed the lock should be free, contender said %q", got)
		}
		next.wait(t)
		time.Sleep(100 * time.Millisecond)
	}
}
