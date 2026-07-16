package subscription

import (
	"encoding/json"
	"strings"
	"testing"
)

// A subscription can return an array of full Xray client configs (one per
// "profile"), each embedding proxy outbounds. Servers repeat across profiles,
// so extraction must deduplicate.
const xrayConfigArrayFixture = `[
  {
    "remarks": "Auto",
    "outbounds": [
      {"tag": "proxy", "protocol": "vless",
       "settings": {"vnext": [{"address": "de25.example.ru", "port": 443,
         "users": [{"id": "de6b50eb-2deb-4118-9c63-f55c7ee38d53", "flow": "xtls-rprx-vision", "encryption": "none"}]}]},
       "streamSettings": {"network": "tcp", "security": "reality",
         "realitySettings": {"serverName": "nl05.example.ru", "publicKey": "PUBKEYAAA", "shortId": "ab12", "fingerprint": "firefox"}}},
      {"tag": "proxy-2", "protocol": "shadowsocks",
       "settings": {"servers": [{"address": "to.the.moon", "port": 1234, "method": "chacha20-ietf-poly1305", "password": "sspass"}]}},
      {"tag": "direct", "protocol": "freedom", "settings": {}}
    ]
  },
  {
    "remarks": "Germany",
    "outbounds": [
      {"tag": "proxy", "protocol": "vless",
       "settings": {"vnext": [{"address": "de25.example.ru", "port": 443,
         "users": [{"id": "de6b50eb-2deb-4118-9c63-f55c7ee38d53", "flow": "xtls-rprx-vision"}]}]},
       "streamSettings": {"network": "tcp", "security": "reality",
         "realitySettings": {"serverName": "nl05.example.ru", "publicKey": "PUBKEYAAA", "shortId": "ab12", "fingerprint": "firefox"}}},
      {"tag": "block", "protocol": "blackhole", "settings": {}}
    ]
  }
]`

func TestParse_XrayConfigArray_ExtractsAndDedups(t *testing.T) {
	entries, err := parse([]byte(xrayConfigArrayFixture))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	// vless server appears in both configs → 1 unique; ss → 1. freedom/blackhole skipped.
	if len(entries) != 2 {
		t.Fatalf("expected 2 unique entries, got %d: %+v", len(entries), entries)
	}

	var vless, ss *SubEntry
	for i := range entries {
		switch entries[i].Protocol {
		case "vless":
			vless = &entries[i]
		case "ss":
			ss = &entries[i]
		}
	}
	if vless == nil {
		t.Fatal("no vless entry extracted")
	}
	if vless.Address != "de25.example.ru" || vless.Port != 443 {
		t.Errorf("vless addr/port = %s:%d", vless.Address, vless.Port)
	}
	if vless.UUID != "de6b50eb-2deb-4118-9c63-f55c7ee38d53" || vless.Flow != "xtls-rprx-vision" {
		t.Errorf("vless uuid/flow mismatch: %+v", vless)
	}
	if vless.Security != "reality" || vless.PublicKey != "PUBKEYAAA" ||
		vless.ShortID != "ab12" || vless.SNI != "nl05.example.ru" || vless.Fingerprint != "firefox" {
		t.Errorf("vless reality fields mismatch: %+v", vless)
	}
	if vless.Network != "tcp" {
		t.Errorf("vless network = %q, want tcp", vless.Network)
	}

	if ss == nil {
		t.Fatal("no ss entry extracted")
	}
	if ss.Address != "to.the.moon" || ss.Port != 1234 ||
		ss.Method != "chacha20-ietf-poly1305" || ss.Password != "sspass" {
		t.Errorf("ss fields mismatch: %+v", ss)
	}
}

// A config-array server whose transport carries settings the distilled SubEntry
// cannot hold (here: xhttpSettings) must launch verbatim, not be rebuilt. The
// preserved RawOutbound is what makes that lossless.
const xhttpConfigFixture = `[
  {
    "remarks": "Germany xhttp",
    "outbounds": [
      {"tag": "proxy", "protocol": "vless",
       "settings": {"vnext": [{"address": "de.glowshine.tech", "port": 8444,
         "users": [{"id": "aaaa-bbbb", "encryption": "none"}]}]},
       "streamSettings": {"network": "xhttp", "security": "reality",
         "realitySettings": {"serverName": "example.com", "publicKey": "PBK", "shortId": "sid", "spiderX": "/spx"},
         "xhttpSettings": {"path": "/h3probe", "mode": "stream-one", "extra": {"xmux": {"maxConcurrency": 8}}}}}
    ]
  }
]`

func TestProxyOutboundJSON_RawIsLaunchedVerbatim(t *testing.T) {
	profiles, err := parseProfiles([]byte(xhttpConfigFixture))
	if err != nil {
		t.Fatalf("parseProfiles: %v", err)
	}
	if len(profiles) != 1 || len(profiles[0].Entries) != 1 {
		t.Fatalf("expected 1 profile with 1 entry, got %+v", profiles)
	}
	e := profiles[0].Entries[0]
	if len(e.RawOutbound) == 0 {
		t.Fatal("entry lost its RawOutbound")
	}

	out, err := ProxyOutboundJSON(&e)
	if err != nil {
		t.Fatalf("ProxyOutboundJSON: %v", err)
	}

	var got struct {
		Tag    string `json:"tag"`
		Stream struct {
			XHTTP struct {
				Path  string          `json:"path"`
				Mode  string          `json:"mode"`
				Extra json.RawMessage `json:"extra"`
			} `json:"xhttpSettings"`
			Reality struct {
				SpiderX string `json:"spiderX"`
			} `json:"realitySettings"`
		} `json:"streamSettings"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unmarshal outbound: %v", err)
	}
	if got.Tag != "proxy" {
		t.Errorf("tag = %q, want proxy", got.Tag)
	}
	if got.Stream.XHTTP.Path != "/h3probe" || got.Stream.XHTTP.Mode != "stream-one" {
		t.Errorf("xhttpSettings lost: %+v", got.Stream.XHTTP)
	}
	if !strings.Contains(string(got.Stream.XHTTP.Extra), "xmux") {
		t.Errorf("xmux extra lost: %s", got.Stream.XHTTP.Extra)
	}
	if got.Stream.Reality.SpiderX != "/spx" {
		t.Errorf("spiderX lost: %q", got.Stream.Reality.SpiderX)
	}
}
