package subscription

import "testing"

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
