//go:build !windows

package safefile

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

const victimContent = "must survive"

func plantLink(t *testing.T) (dir, victim, link string) {
	t.Helper()
	dir = t.TempDir()
	victim = filepath.Join(dir, "victim")
	if err := os.WriteFile(victim, []byte(victimContent), 0600); err != nil {
		t.Fatal(err)
	}
	link = filepath.Join(dir, "apps.txt")
	if err := os.Symlink(victim, link); err != nil {
		t.Fatal(err)
	}
	return dir, victim, link
}

func assertVictim(t *testing.T, victim string) {
	t.Helper()
	got, err := os.ReadFile(victim)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != victimContent {
		t.Errorf("symlink target was written through: %q", got)
	}
}

// A symlink in place of a state file is not read through: its target's
// contents must not reach the app (and from there, a request header).
func TestReadFile_RefusesSymlink(t *testing.T) {
	_, _, link := plantLink(t)
	if data, err := ReadFile(link); err == nil {
		t.Fatalf("read through a symlink: %q", data)
	}
}

// "No file" stays distinguishable, so callers keep treating it as empty.
func TestReadFile_MissingIsNotExist(t *testing.T) {
	_, err := ReadFile(filepath.Join(t.TempDir(), "absent"))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("err = %v, want fs.ErrNotExist", err)
	}
}

// A FIFO under the name is refused at once rather than blocking the open.
func TestReadFile_RefusesFIFOWithoutBlocking(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fifo")
	if err := syscall.Mkfifo(path, 0600); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := ReadFile(path)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("FIFO read as a state file")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ReadFile blocked on a FIFO")
	}
}

// A write replaces the link itself; the file it pointed at keeps its bytes.
func TestWriteFile_ReplacesSymlinkNotTarget(t *testing.T) {
	_, victim, link := plantLink(t)

	if err := WriteFile(link, []byte("new"), 0600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	assertVictim(t, victim)
	fi, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		t.Error("the symlink is still in place")
	}
	if got, _ := os.ReadFile(link); string(got) != "new" {
		t.Errorf("content = %q, want %q", got, "new")
	}
}

// State files hold tokens and the server: owner-only, and no temp left over.
func TestWriteFile_OwnerOnlyAndNoTempLeft(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "last_server.json")

	if err := WriteFile(path, []byte("{}"), 0600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0600 {
		t.Errorf("mode = %o, want 600", perm)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("dir holds %d entries, want only the file", len(entries))
	}
}

// Under sudo the new file goes to the invoking user before it is published, so
// a run without sudo can still read it.
func TestWriteFile_HandsOverUnderSudo(t *testing.T) {
	uid, gid := os.Getuid(), os.Getgid()
	called := false
	orig := sudoOwner
	sudoOwner = func() (int, int, bool) { called = true; return uid, gid, true }
	t.Cleanup(func() { sudoOwner = orig })

	path := filepath.Join(t.TempDir(), "hwid")
	if err := WriteFile(path, []byte("id"), 0600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if !called {
		t.Error("the sudo user was never asked for")
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st := fi.Sys().(*syscall.Stat_t); int(st.Uid) != uid || int(st.Gid) != gid {
		t.Errorf("owner %d:%d, want %d:%d", st.Uid, st.Gid, uid, gid)
	}
}
