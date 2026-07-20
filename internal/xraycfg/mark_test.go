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
