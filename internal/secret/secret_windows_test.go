//go:build windows

package secret

import (
	"bytes"
	"testing"
)

func TestDPAPIRoundTrip(t *testing.T) {
	s, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	plain := []byte("https://panel.example/sub/token\n")
	blob, err := s.Seal(plain)
	if err != nil {
		t.Fatal(err)
	}
	if !Sealed(blob) || bytes.Contains(blob, []byte("token")) {
		t.Fatalf("sealed blob shows the plain text or lacks the header")
	}
	got, err := s.Open(blob)
	if err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("Open = %q, %v", got, err)
	}
	blob[len(blob)-1] ^= 1
	if _, err := s.Open(blob); err == nil {
		t.Error("a tampered blob opened")
	}
	if empty, err := s.Seal(nil); err != nil {
		t.Errorf("sealing nothing: %v", err)
	} else if got, err := s.Open(empty); err != nil || len(got) != 0 {
		t.Errorf("empty round trip: %q, %v", got, err)
	}
}
