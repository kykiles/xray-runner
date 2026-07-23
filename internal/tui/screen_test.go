package tui

import (
	"io"
	"os"
	"testing"
)

// The alt-buffer exit must never reach the terminal, or the shell flashes
// between two screens; everything around it must.
func TestAltKeeperSwallowsAltExit(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	in := []byte("before" + altExit + "after")
	go func() {
		defer w.Close()
		n, err := altKeeper{w}.Write(in)
		if err != nil || n != len(in) {
			t.Errorf("Write = %d, %v; want %d, nil", n, err, len(in))
		}
	}()

	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "beforeafter" {
		t.Fatalf("got %q, want %q", got, "beforeafter")
	}
}
