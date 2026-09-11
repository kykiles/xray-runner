package subscription

// A14: the direct VLESS builder knows gRPC mode=multi and authority, but a
// subscription lost them on the way in — a multiMode server got a client in
// "gun" mode, and one behind a CDN lost the :authority it routes on.

import (
	"encoding/json"
	"testing"

	"xray-runner/internal/xraycfg"
)

const grpcUUID = "b831381d-6324-4d53-ad4f-8cda48b30811"

// grpcSettingsOf builds the entry's outbound and returns its gRPC block.
func grpcSettingsOf(t *testing.T, e *SubEntry) *xraycfg.GRPCSettings {
	t.Helper()
	raw, err := BuildOutboundJSON(e)
	if err != nil {
		t.Fatalf("BuildOutboundJSON: %v", err)
	}
	var ob struct {
		Stream *xraycfg.StreamSettings `json:"streamSettings"`
	}
	if err := json.Unmarshal(raw, &ob); err != nil {
		t.Fatalf("unmarshal outbound: %v", err)
	}
	if ob.Stream == nil || ob.Stream.GRPCSettings == nil {
		t.Fatalf("no grpcSettings in %s", raw)
	}
	return ob.Stream.GRPCSettings
}

func assertGRPC(t *testing.T, g *xraycfg.GRPCSettings) {
	t.Helper()
	if g.ServiceName != "s" || !g.MultiMode || g.Authority != "a.example" {
		t.Errorf("grpcSettings = %+v, want serviceName=s multiMode authority=a.example", *g)
	}
}

func TestGRPCParams_SurviveURL(t *testing.T) {
	for _, link := range []string{
		"vless://" + grpcUUID + "@h.example:443?type=grpc&security=tls&serviceName=s&mode=multi&authority=a.example#n",
		"trojan://secret@h.example:443?type=grpc&security=tls&serviceName=s&mode=multi&authority=a.example#n",
	} {
		e, err := parseURL(link)
		if err != nil {
			t.Fatalf("parseURL(%s): %v", link, err)
		}
		assertGRPC(t, grpcSettingsOf(t, &e))

		// --dump-links writes the entry back out as a link; it must round-trip.
		enc, err := EncodeURL(&e)
		if err != nil {
			t.Fatalf("EncodeURL: %v", err)
		}
		back, err := parseURL(enc)
		if err != nil {
			t.Fatalf("parseURL(%s): %v", enc, err)
		}
		assertGRPC(t, grpcSettingsOf(t, &back))
	}
}

// In a v2rayN vmess link a gRPC transport keeps serviceName in "path", the mode
// in "type" and the authority in "host".
func TestGRPCParams_SurviveVMessJSON(t *testing.T) {
	link := vmessLink(t, map[string]interface{}{
		"v": "2", "add": "h.example", "port": "443", "id": grpcUUID,
		"net": "grpc", "type": "multi", "path": "s", "host": "a.example", "tls": "tls",
	})
	e, err := parseURL(link)
	if err != nil {
		t.Fatalf("parseURL: %v", err)
	}
	assertGRPC(t, grpcSettingsOf(t, &e))
}

func TestGRPCParams_SurviveXrayJSON(t *testing.T) {
	cfg := `[{"remarks":"g","outbounds":[
	  {"tag":"proxy","protocol":"vless","settings":{"vnext":[{"address":"h.example","port":443,
	    "users":[{"id":"` + grpcUUID + `","encryption":"none"}]}]},
	   "streamSettings":{"network":"grpc","security":"tls",
	     "grpcSettings":{"serviceName":"s","multiMode":true,"authority":"a.example"}}},
	  {"tag":"direct","protocol":"freedom"}]}]`
	entries, err := parse([]byte(cfg))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	e := entries[0]
	e.RawOutbound = nil // rebuild from the fields, the way a link would be
	assertGRPC(t, grpcSettingsOf(t, &e))
}
