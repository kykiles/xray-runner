package app

import (
	"context"
	"net"
	"testing"
	"time"

	"xray-runner/internal/xraycfg"
)

func TestPortsFromInbounds(t *testing.T) {
	t.Run("by protocol regardless of order", func(t *testing.T) {
		inbounds := []xraycfg.Inbound{
			{Tag: "http", Protocol: "http", Port: 10809},
			{Tag: "socks", Protocol: "socks", Port: 10808},
		}
		p, err := portsFromInbounds(inbounds, false)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if p.socks != 10808 || p.http != 10809 {
			t.Errorf("got socks=%d http=%d, want 10808/10809", p.socks, p.http)
		}
	})

	t.Run("missing http is an error", func(t *testing.T) {
		inbounds := []xraycfg.Inbound{{Tag: "socks", Protocol: "socks", Port: 10808}}
		if _, err := portsFromInbounds(inbounds, false); err == nil {
			t.Error("expected error when http inbound is missing")
		}
	})

	t.Run("tun mode needs no local ports", func(t *testing.T) {
		if _, err := portsFromInbounds(nil, true); err != nil {
			t.Errorf("tun mode should not require socks/http ports: %v", err)
		}
	})
}

func TestMaskURL(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		mask bool
		want string
	}{
		{"unmasked", "https://sub.example.com/QzWXrL3rcM2DYRoF", false, "https://sub.example.com/QzWXrL3rcM2DYRoF"},
		{"masked long token", "https://sub.example.com/QzWXrL3rcM2DYRoF", true, "https://sub.example.com/QzWXr…"},
		{"masked short path", "https://sub.example.com/ab", true, "https://sub.example.com/ab…"},
		{"empty", "", true, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := maskURL(c.raw, c.mask); got != c.want {
				t.Errorf("maskURL(%q, %v) = %q, want %q", c.raw, c.mask, got, c.want)
			}
		})
	}
}

func TestMaskString(t *testing.T) {
	cases := []struct {
		name string
		s    string
		mask bool
		want string
	}{
		{"masked empty", "", true, ""},
		// A16: a one-character sid from a subscription used to panic on s[:2], and
		// up to 8 characters the "partial" mask showed the whole value.
		{"masked one char", "a", true, "***"},
		{"masked short", "abcd", true, "***"},
		{"masked boundary 8", "abcdefgh", true, "***"},
		{"masked 9", "abcdefghi", true, "abcd...fghi"},
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
