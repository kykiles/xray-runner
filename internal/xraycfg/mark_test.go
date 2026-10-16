package xraycfg

import (
	"encoding/json"
	"testing"
)

// In TUN mode every packet leaving a freedom outbound has to carry the firewall
// mark, otherwise the split-default route sends it straight back into the
// tunnel and the connection loops until it is dropped.
func TestBindDirectOutbounds_MarksFreedom(t *testing.T) {
	raw := json.RawMessage(`{"outbounds":[
		{"tag":"proxy","protocol":"vless"},
		{"tag":"direct","protocol":"freedom"},
		{"tag":"block","protocol":"blackhole"}
	]}`)

	marked, err := BindDirectOutbounds(raw, DirectBind{Mark: DirectFwMark})
	if err != nil {
		t.Fatalf("BindDirectOutbounds: %v", err)
	}

	var cfg struct {
		Outbounds []struct {
			Tag    string `json:"tag"`
			Stream *struct {
				Sockopt *struct {
					Mark int `json:"mark"`
				} `json:"sockopt"`
			} `json:"streamSettings"`
		} `json:"outbounds"`
	}
	if err := json.Unmarshal(marked, &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	for _, o := range cfg.Outbounds {
		switch o.Tag {
		case "direct":
			if o.Stream == nil || o.Stream.Sockopt == nil {
				t.Fatalf("freedom outbound has no sockopt: %s", marked)
			}
			if o.Stream.Sockopt.Mark != DirectFwMark {
				t.Errorf("mark = %d, want %d", o.Stream.Sockopt.Mark, DirectFwMark)
			}
		default:
			if o.Stream != nil && o.Stream.Sockopt != nil {
				t.Errorf("%s outbound must not be marked: %s", o.Tag, marked)
			}
		}
	}
}

// Panels ship freedom outbounds with their own streamSettings; marking must add
// to that block, not replace it.
func TestBindDirectOutbounds_KeepsExistingStreamSettings(t *testing.T) {
	raw := json.RawMessage(`{"outbounds":[
		{"tag":"direct","protocol":"freedom","streamSettings":{"sockopt":{"tcpFastOpen":true}}}
	]}`)

	marked, err := BindDirectOutbounds(raw, DirectBind{Mark: DirectFwMark})
	if err != nil {
		t.Fatalf("BindDirectOutbounds: %v", err)
	}

	var cfg struct {
		Outbounds []struct {
			Stream struct {
				Sockopt struct {
					Mark        int  `json:"mark"`
					TCPFastOpen bool `json:"tcpFastOpen"`
				} `json:"sockopt"`
			} `json:"streamSettings"`
		} `json:"outbounds"`
	}
	if err := json.Unmarshal(marked, &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if !cfg.Outbounds[0].Stream.Sockopt.TCPFastOpen {
		t.Errorf("existing sockopt field was dropped: %s", marked)
	}
	if cfg.Outbounds[0].Stream.Sockopt.Mark != DirectFwMark {
		t.Errorf("mark = %d, want %d", cfg.Outbounds[0].Stream.Sockopt.Mark, DirectFwMark)
	}
}

// Windows ignores sockopt.mark entirely, so there the freedom outbound is
// pinned to the physical adapter instead — IP_UNICAST_IF bypasses the routing
// table, which is what breaks the loop.
func TestBindDirectOutbounds_PinsInterface(t *testing.T) {
	raw := json.RawMessage(`{"outbounds":[{"tag":"direct","protocol":"freedom"}]}`)

	bound, err := BindDirectOutbounds(raw, DirectBind{Interface: "Ethernet"})
	if err != nil {
		t.Fatalf("BindDirectOutbounds: %v", err)
	}

	var cfg struct {
		Outbounds []struct {
			Stream struct {
				Sockopt struct {
					Mark      int    `json:"mark"`
					Interface string `json:"interface"`
				} `json:"sockopt"`
			} `json:"streamSettings"`
		} `json:"outbounds"`
	}
	if err := json.Unmarshal(bound, &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if cfg.Outbounds[0].Stream.Sockopt.Interface != "Ethernet" {
		t.Errorf("interface = %q, want Ethernet: %s", cfg.Outbounds[0].Stream.Sockopt.Interface, bound)
	}
	// A mark xray cannot honour would only mislead whoever reads the config.
	if cfg.Outbounds[0].Stream.Sockopt.Mark != 0 {
		t.Errorf("mark must stay unset on the interface path: %s", bound)
	}
}

// A platform with no escape hatch must get the config it would have got before,
// rather than a half-applied sockopt.
func TestBindDirectOutbounds_ZeroBindChangesNothing(t *testing.T) {
	raw := json.RawMessage(`{"outbounds":[{"tag":"direct","protocol":"freedom"}]}`)

	bound, err := BindDirectOutbounds(raw, DirectBind{})
	if err != nil {
		t.Fatalf("BindDirectOutbounds: %v", err)
	}
	if string(bound) != string(raw) {
		t.Errorf("config changed: %s", bound)
	}
}

// The rest of the config must survive untouched: dns, routing and inbounds are
// what the session depends on.
func TestBindDirectOutbounds_LeavesRestAlone(t *testing.T) {
	raw := json.RawMessage(`{"dns":{"servers":["1.1.1.1"]},"routing":{"rules":[{"type":"field"}]},"outbounds":[{"tag":"direct","protocol":"freedom"}]}`)

	marked, err := BindDirectOutbounds(raw, DirectBind{Mark: DirectFwMark})
	if err != nil {
		t.Fatalf("BindDirectOutbounds: %v", err)
	}

	var cfg map[string]json.RawMessage
	if err := json.Unmarshal(marked, &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if string(cfg["dns"]) != `{"servers":["1.1.1.1"]}` {
		t.Errorf("dns changed: %s", cfg["dns"])
	}
	if string(cfg["routing"]) != `{"rules":[{"type":"field"}]}` {
		t.Errorf("routing changed: %s", cfg["routing"])
	}
}

// The core reads keys without regard to case, so an outbound spelled
// {"Protocol":"freedom"} is a freedom outbound and must be bound like one, with
// the binding landing in the one block the core will read.
func TestBindDirectOutbounds_KeysAreCaseInsensitive(t *testing.T) {
	raw := json.RawMessage(`{"outbounds":[
		{"tag":"a","Protocol":"freedom"},
		{"tag":"b","PROTOCOL":"freedom","StreamSettings":{"Sockopt":{"Mark":7,"Interface":"old","tcpFastOpen":true}}}
	]}`)

	for name, bind := range map[string]DirectBind{
		"mark":      {Mark: DirectFwMark},
		"interface": {Interface: "eth0"},
	} {
		t.Run(name, func(t *testing.T) {
			bound, err := BindDirectOutbounds(raw, bind)
			if err != nil {
				t.Fatalf("BindDirectOutbounds: %v", err)
			}

			// encoding/json matches keys the way the core does.
			var cfg struct {
				Outbounds []struct {
					Stream struct {
						Sockopt struct {
							Mark        int    `json:"mark"`
							Interface   string `json:"interface"`
							TCPFastOpen bool   `json:"tcpFastOpen"`
						} `json:"sockopt"`
					} `json:"streamSettings"`
				} `json:"outbounds"`
			}
			if err := json.Unmarshal(bound, &cfg); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			for i, o := range cfg.Outbounds {
				s := o.Stream.Sockopt
				if bind.Mark != 0 && s.Mark != bind.Mark {
					t.Errorf("outbound %d: mark = %d, want %d: %s", i, s.Mark, bind.Mark, bound)
				}
				if bind.Interface != "" && s.Interface != bind.Interface {
					t.Errorf("outbound %d: interface = %q, want %q: %s", i, s.Interface, bind.Interface, bound)
				}
			}
			if !cfg.Outbounds[1].Stream.Sockopt.TCPFastOpen {
				t.Errorf("existing sockopt field was dropped: %s", bound)
			}

			// Exactly one spelling of each key may remain.
			var top struct {
				Outbounds []map[string]json.RawMessage `json:"outbounds"`
			}
			if err := json.Unmarshal(bound, &top); err != nil {
				t.Fatal(err)
			}
			for i, ob := range top.Outbounds {
				if n := len(foldedMapKeys(ob, "protocol")); n != 1 {
					t.Errorf("outbound %d: %d protocol keys: %s", i, n, bound)
				}
				if n := len(foldedMapKeys(ob, "streamSettings")); n != 1 {
					t.Errorf("outbound %d: %d streamSettings keys: %s", i, n, bound)
					continue
				}
				var stream map[string]json.RawMessage
				if err := json.Unmarshal(ob[foldedMapKeys(ob, "streamSettings")[0]], &stream); err != nil {
					t.Fatal(err)
				}
				if n := len(foldedMapKeys(stream, "sockopt")); n != 1 {
					t.Errorf("outbound %d: %d sockopt keys: %s", i, n, bound)
					continue
				}
				var sockopt map[string]json.RawMessage
				if err := json.Unmarshal(stream[foldedMapKeys(stream, "sockopt")[0]], &sockopt); err != nil {
					t.Fatal(err)
				}
				for _, field := range []string{"mark", "interface"} {
					if n := len(foldedMapKeys(sockopt, field)); n > 1 {
						t.Errorf("outbound %d: %d %s keys: %s", i, n, field, bound)
					}
				}
			}
		})
	}
}

func foldedMapKeys(m map[string]json.RawMessage, name string) []string {
	var keys []string
	for k := range m {
		if sameKey(k, name) {
			keys = append(keys, k)
		}
	}
	return keys
}

// Two spellings of one key leave it unclear which the core would honour.
func TestBindDirectOutbounds_RepeatedKeyIsRejected(t *testing.T) {
	for _, raw := range []string{
		`{"outbounds":[{"protocol":"freedom","Protocol":"freedom"}]}`,
		`{"outbounds":[{"protocol":"freedom","streamSettings":{},"StreamSettings":{}}]}`,
		`{"outbounds":[{"protocol":"freedom","streamSettings":{"sockopt":{},"Sockopt":{}}}]}`,
	} {
		if _, err := BindDirectOutbounds(json.RawMessage(raw), DirectBind{Mark: DirectFwMark}); err == nil {
			t.Errorf("expected an error for %s", raw)
		}
	}
}

// Panel exports carry explicit nulls. Unmarshalling `null` into a map sets it
// back to nil, so the marking code has to re-make the map before writing to it —
// otherwise a subscription can crash the process on connect.
func TestBindDirectOutbounds_NullBlocks(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{"null streamSettings", `{"outbounds":[{"tag":"direct","protocol":"freedom","streamSettings":null}]}`},
		{"null sockopt", `{"outbounds":[{"tag":"direct","protocol":"freedom","streamSettings":{"sockopt":null}}]}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			marked, err := BindDirectOutbounds(json.RawMessage(tt.raw), DirectBind{Mark: DirectFwMark})
			if err != nil {
				t.Fatalf("BindDirectOutbounds: %v", err)
			}

			var cfg struct {
				Outbounds []struct {
					Stream struct {
						Sockopt struct {
							Mark int `json:"mark"`
						} `json:"sockopt"`
					} `json:"streamSettings"`
				} `json:"outbounds"`
			}
			if err := json.Unmarshal(marked, &cfg); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if cfg.Outbounds[0].Stream.Sockopt.Mark != DirectFwMark {
				t.Errorf("mark = %d, want %d: %s", cfg.Outbounds[0].Stream.Sockopt.Mark, DirectFwMark, marked)
			}
		})
	}
}
