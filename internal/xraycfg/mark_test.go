package xraycfg

import (
	"encoding/json"
	"testing"
)

// In TUN mode every packet leaving a freedom outbound has to carry the firewall
// mark, otherwise the split-default route sends it straight back into the
// tunnel and the connection loops until it is dropped.
func TestMarkDirectOutbounds_MarksFreedom(t *testing.T) {
	raw := json.RawMessage(`{"outbounds":[
		{"tag":"proxy","protocol":"vless"},
		{"tag":"direct","protocol":"freedom"},
		{"tag":"block","protocol":"blackhole"}
	]}`)

	marked, err := MarkDirectOutbounds(raw)
	if err != nil {
		t.Fatalf("MarkDirectOutbounds: %v", err)
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
func TestMarkDirectOutbounds_KeepsExistingStreamSettings(t *testing.T) {
	raw := json.RawMessage(`{"outbounds":[
		{"tag":"direct","protocol":"freedom","streamSettings":{"sockopt":{"tcpFastOpen":true}}}
	]}`)

	marked, err := MarkDirectOutbounds(raw)
	if err != nil {
		t.Fatalf("MarkDirectOutbounds: %v", err)
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

// The rest of the config must survive untouched: dns, routing and inbounds are
// what the session depends on.
func TestMarkDirectOutbounds_LeavesRestAlone(t *testing.T) {
	raw := json.RawMessage(`{"dns":{"servers":["1.1.1.1"]},"routing":{"rules":[{"type":"field"}]},"outbounds":[{"tag":"direct","protocol":"freedom"}]}`)

	marked, err := MarkDirectOutbounds(raw)
	if err != nil {
		t.Fatalf("MarkDirectOutbounds: %v", err)
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
