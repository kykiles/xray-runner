package app

import (
	"context"
	"net"
	"testing"
	"time"
)

func TestMaskString(t *testing.T) {
	cases := []struct {
		name string
		s    string
		mask bool
		want string
	}{
		{"masked empty", "", true, ""},
		{"masked short", "abcd", true, "ab...cd"},
		{"masked boundary 8", "abcdefgh", true, "ab...gh"},
		{"masked long", "abcdefghijklmnop", true, "abcd...mnop"},
		{"unmasked empty", "", false, ""},
		{"unmasked short", "abcd", false, "abcd"},
		{"unmasked long", "abcdefghijklmnop", false, "abcdefghijklmnop"},
		{"unmasked uuid", "11111111-2222-3333-4444-555555555555", false, "11111111-2222-3333-4444-555555555555"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := maskString(c.s, c.mask); got != c.want {
				t.Errorf("maskString(%q, %v) = %q, want %q", c.s, c.mask, got, c.want)
			}
		})
	}
}

func TestAwaitTUNInterface_NotFound(t *testing.T) {
	a := &App{
		interfaces: func() ([]net.Interface, error) {
			return []net.Interface{{Name: "eth0", Flags: net.FlagUp}}, nil
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	if a.awaitTUNInterface(ctx, "xray-tun", 30*time.Millisecond) {
		t.Error("expected false for non-existent interface")
	}
}

func TestAwaitTUNInterface_Found(t *testing.T) {
	a := &App{
		interfaces: func() ([]net.Interface, error) {
			return []net.Interface{
				{Name: "eth0", Flags: net.FlagUp},
				{Name: "xray-tun", Flags: net.FlagUp},
			}, nil
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	if !a.awaitTUNInterface(ctx, "xray-tun", 500*time.Millisecond) {
		t.Error("expected true for existing up interface")
	}
}

func TestAwaitTUNInterface_DownNotAccepted(t *testing.T) {
	a := &App{
		interfaces: func() ([]net.Interface, error) {
			return []net.Interface{{Name: "xray-tun", Flags: 0}}, nil
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	if a.awaitTUNInterface(ctx, "xray-tun", 30*time.Millisecond) {
		t.Error("expected false for down interface")
	}
}

func TestAwaitTUNInterface_ContextCanceled(t *testing.T) {
	a := &App{
		interfaces: func() ([]net.Interface, error) {
			return nil, nil
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if a.awaitTUNInterface(ctx, "xray-tun", 1*time.Second) {
		t.Error("expected false when context already canceled")
	}
}
