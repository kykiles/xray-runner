package app

import (
	"context"
	"net"
	"testing"
	"time"

	"xray-runner/internal/xraycfg"
)

func TestResolvePorts(t *testing.T) {
	a := &App{}

	t.Run("by protocol regardless of order", func(t *testing.T) {
		cfg := &xraycfg.XrayConfig{Inbounds: []xraycfg.Inbound{
			{Tag: "http", Protocol: "http", Port: 10809},
			{Tag: "socks", Protocol: "socks", Port: 10808},
		}}
		socks, httpP, err := a.resolvePorts(cfg)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if socks != 10808 || httpP != 10809 {
			t.Errorf("got socks=%d http=%d, want 10808/10809", socks, httpP)
		}
	})

	t.Run("missing http is an error", func(t *testing.T) {
		cfg := &xraycfg.XrayConfig{Inbounds: []xraycfg.Inbound{
			{Tag: "socks", Protocol: "socks", Port: 10808},
		}}
		if _, _, err := a.resolvePorts(cfg); err == nil {
			t.Error("expected error when http inbound is missing")
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
