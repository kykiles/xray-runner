package subscription

import (
	"encoding/json"
	"testing"
)

// A balancer profile groups several proxy outbounds under one balancerTag; a
// plain profile carries a single proxy outbound and no balancers.
const profileFixture = `[
  {
    "remarks": "🇸🇴Автовыбор 🔄",
    "routing": {
      "balancers": [{"tag": "Balancer", "selector": ["proxy"],
        "strategy": {"type": "leastLoad", "settings": {"maxRTT": "1s"}}, "fallbackTag": "direct"}],
      "rules": [{"type": "field", "ip": ["1.1.1.1"], "balancerTag": "Balancer"}]
    },
    "outbounds": [
      {"tag": "proxy", "protocol": "vless",
       "settings": {"vnext": [{"address": "de25.example.ru", "port": 443,
         "users": [{"id": "de6b50eb-2deb-4118-9c63-f55c7ee38d53", "flow": "xtls-rprx-vision"}]}]},
       "streamSettings": {"network": "tcp", "security": "reality",
         "realitySettings": {"serverName": "nl05.example.ru", "publicKey": "PUBKEYAAA", "shortId": "ab12"}}},
      {"tag": "proxy-2", "protocol": "shadowsocks",
       "settings": {"servers": [{"address": "to.the.moon", "port": 1234, "method": "chacha20-ietf-poly1305", "password": "sspass"}]}},
      {"tag": "direct", "protocol": "freedom", "settings": {}}
    ]
  },
  {
    "remarks": "🇲🇩 Молдова",
    "routing": {"rules": [{"type": "field", "protocol": ["bittorrent"], "outboundTag": "direct"}]},
    "outbounds": [
      {"tag": "proxy", "protocol": "vless",
       "settings": {"vnext": [{"address": "md1.example.ru", "port": 443,
         "users": [{"id": "aaaaaaaa-2deb-4118-9c63-f55c7ee38d53"}]}]},
       "streamSettings": {"network": "ws", "security": "tls",
         "tlsSettings": {"serverName": "md1.example.ru"}, "wsSettings": {"path": "/ws"}}},
      {"tag": "block", "protocol": "blackhole", "settings": {}}
    ]
  }
]`

func TestParseProfiles_SplitsByProfileAndKeepsBalancer(t *testing.T) {
	profiles, err := parseProfiles([]byte(profileFixture))
	if err != nil {
		t.Fatalf("parseProfiles: %v", err)
	}
	if len(profiles) != 2 {
		t.Fatalf("expected 2 profiles, got %d", len(profiles))
	}

	auto := profiles[0]
	if auto.Name != "🇸🇴Автовыбор 🔄" {
		t.Errorf("profile name = %q", auto.Name)
	}
	// Only proxy outbounds count: freedom/blackhole are infrastructure.
	if len(auto.Entries) != 2 {
		t.Fatalf("auto entries = %d, want 2: %+v", len(auto.Entries), auto.Entries)
	}
	if auto.Balancer == nil {
		t.Fatal("auto profile lost its balancer")
	}
	if auto.Balancer.Strategy != "leastLoad" || auto.Balancer.Tag != "Balancer" {
		t.Errorf("balancer = %+v", auto.Balancer)
	}
	if len(auto.Raw) == 0 {
		t.Error("auto profile kept no raw config to launch as-is")
	}
	if auto.Entries[0].Address != "de25.example.ru" || auto.Entries[0].Security != "reality" {
		t.Errorf("first entry mismatch: %+v", auto.Entries[0])
	}

	md := profiles[1]
	if md.Name != "🇲🇩 Молдова" {
		t.Errorf("profile name = %q", md.Name)
	}
	if md.Balancer != nil {
		t.Errorf("plain profile should have no balancer, got %+v", md.Balancer)
	}
	if len(md.Entries) != 1 || md.Entries[0].Address != "md1.example.ru" {
		t.Fatalf("md entries = %+v", md.Entries)
	}
	if md.Entries[0].Network != "ws" || md.Entries[0].Path != "/ws" {
		t.Errorf("ws fields lost: %+v", md.Entries[0])
	}
}

// Servers repeating across profiles must stay in each profile: dedup is per
// profile, not global, otherwise a profile silently loses servers.
func TestParseProfiles_DedupIsPerProfile(t *testing.T) {
	const fixture = `[
	  {"remarks": "A", "outbounds": [
	    {"tag": "proxy", "protocol": "vless", "settings": {"vnext": [{"address": "same.example.ru", "port": 443, "users": [{"id": "u1"}]}]}},
	    {"tag": "proxy-2", "protocol": "vless", "settings": {"vnext": [{"address": "same.example.ru", "port": 443, "users": [{"id": "u1"}]}]}}
	  ]},
	  {"remarks": "B", "outbounds": [
	    {"tag": "proxy", "protocol": "vless", "settings": {"vnext": [{"address": "same.example.ru", "port": 443, "users": [{"id": "u1"}]}]}}
	  ]}
	]`
	profiles, err := parseProfiles([]byte(fixture))
	if err != nil {
		t.Fatalf("parseProfiles: %v", err)
	}
	if len(profiles) != 2 {
		t.Fatalf("expected 2 profiles, got %d", len(profiles))
	}
	// Duplicate within profile A collapses to one...
	if len(profiles[0].Entries) != 1 {
		t.Errorf("profile A entries = %d, want 1 (dedup within profile)", len(profiles[0].Entries))
	}
	// ...but profile B keeps its own copy of the same server.
	if len(profiles[1].Entries) != 1 {
		t.Errorf("profile B entries = %d, want 1 (not deduped against A)", len(profiles[1].Entries))
	}
}

// A URL-list subscription has no profiles — it becomes a single unnamed one, so
// the UI can skip the profile screen.
func TestParseProfiles_URLListIsSingleUnnamedProfile(t *testing.T) {
	raw := []byte("vless://uuid@host.example.ru:443?type=tcp&security=reality&pbk=K#Server-1\n" +
		"vless://uuid@host2.example.ru:443?type=tcp#Server-2")
	profiles, err := parseProfiles(raw)
	if err != nil {
		t.Fatalf("parseProfiles: %v", err)
	}
	if len(profiles) != 1 {
		t.Fatalf("expected 1 profile, got %d", len(profiles))
	}
	if profiles[0].Name != "" {
		t.Errorf("name = %q, want empty", profiles[0].Name)
	}
	if len(profiles[0].Entries) != 2 {
		t.Errorf("entries = %d, want 2", len(profiles[0].Entries))
	}
	if profiles[0].Balancer != nil {
		t.Error("url list must not invent a balancer")
	}
}

// Panels like glowshine publish one config per location, so every "profile"
// holds a single server. The menu collapses those into a plain server list.
const singlesFixture = `[
  {
    "remarks": "🇩🇪 Германия 1",
    "outbounds": [
      {"tag": "proxy", "protocol": "vless",
       "settings": {"vnext": [{"address": "de1.example.ru", "port": 443,
         "users": [{"id": "aaaaaaaa-2deb-4118-9c63-f55c7ee38d53"}]}]},
       "streamSettings": {"network": "tcp"}}
    ]
  },
  {
    "remarks": "🇺🇸 США",
    "outbounds": [
      {"tag": "proxy", "protocol": "vless",
       "settings": {"vnext": [{"address": "us1.example.ru", "port": 443,
         "users": [{"id": "bbbbbbbb-2deb-4118-9c63-f55c7ee38d53"}]}]},
       "streamSettings": {"network": "ws"}}
    ]
  }
]`

func TestAllSingle(t *testing.T) {
	singles, err := parseProfiles([]byte(singlesFixture))
	if err != nil {
		t.Fatalf("parseProfiles: %v", err)
	}
	if !AllSingle(singles) {
		t.Error("one-server profiles must count as all-single")
	}

	// The mixed fixture carries a balancer profile, so the profile screen stays.
	mixed, err := parseProfiles([]byte(profileFixture))
	if err != nil {
		t.Fatalf("parseProfiles: %v", err)
	}
	if AllSingle(mixed) {
		t.Error("a balancer profile must keep the subscription out of the flat path")
	}
}

// The scripted path indexes servers with --server N, so switching it to the
// profile-aware fetch must produce byte-for-byte the list it produced before.
func TestFlattenUnique_MatchesTheFlatParser(t *testing.T) {
	for name, fixture := range map[string]string{"profiles": profileFixture, "singles": singlesFixture} {
		var arr []json.RawMessage
		if err := json.Unmarshal([]byte(fixture), &arr); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		want, err := parseXrayConfigArray(arr)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		profiles, err := parseXrayConfigProfiles(arr)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		got := FlattenUnique(profiles)
		if len(got) != len(want) {
			t.Fatalf("%s: %d entries, want %d", name, len(got), len(want))
		}
		for i := range got {
			if entryKey(got[i]) != entryKey(want[i]) || got[i].Remarks != want[i].Remarks {
				t.Errorf("%s: entry %d = %+v, want %+v", name, i, got[i], want[i])
			}
		}
	}
}

// A URL list has no profile structure, and its duplicates are the user's own.
// Deduplicating them would shift every --server index below the duplicate.
func TestFlattenUnique_KeepsURLListAsIs(t *testing.T) {
	dup := SubEntry{Protocol: "vless", Address: "a.invalid", Port: 443, UUID: "u"}
	profiles := []Profile{{Entries: []SubEntry{dup, dup}}}
	if got := FlattenUnique(profiles); len(got) != 2 {
		t.Errorf("entries = %d, want the URL list untouched (2)", len(got))
	}
}

// A server chosen from a flat list must lead back to the profile it came from:
// that profile carries the routing the session has to run under.
func TestProfileFor(t *testing.T) {
	profiles, err := parseProfiles([]byte(singlesFixture))
	if err != nil {
		t.Fatalf("parseProfiles: %v", err)
	}
	entries := FlattenNamed(profiles)

	owner := ProfileFor(profiles, &entries[1])
	if owner == nil {
		t.Fatal("ProfileFor returned nil for a server that came from the list")
	}
	if owner.Name != "🇺🇸 США" {
		t.Errorf("owner = %q, want the second profile", owner.Name)
	}

	// Flattening renames a lone server, so matching must ignore Remarks.
	renamed := entries[0]
	renamed.Remarks = "whatever the UI shows"
	if got := ProfileFor(profiles, &renamed); got == nil || got.Name != "🇩🇪 Германия 1" {
		t.Errorf("ProfileFor ignored the renamed entry: %v", got)
	}

	stranger := SubEntry{Protocol: "vless", Address: "nowhere.invalid", Port: 443}
	if got := ProfileFor(profiles, &stranger); got != nil {
		t.Errorf("ProfileFor = %v for an entry from no profile, want nil", got.Name)
	}
}

func TestFlattenNamed_LoneServerTakesProfileName(t *testing.T) {
	profiles, err := parseProfiles([]byte(singlesFixture))
	if err != nil {
		t.Fatalf("parseProfiles: %v", err)
	}
	entries := FlattenNamed(profiles)
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(entries))
	}
	// Without the override Remarks would still be the address (S-2 of the task).
	if entries[0].Remarks != "🇩🇪 Германия 1" {
		t.Errorf("entries[0].Remarks = %q, want the profile name", entries[0].Remarks)
	}
	if entries[1].Remarks != "🇺🇸 США" {
		t.Errorf("entries[1].Remarks = %q, want the profile name", entries[1].Remarks)
	}
	if entries[0].Address != "de1.example.ru" {
		t.Errorf("entries[0].Address = %q", entries[0].Address)
	}
}

// A profile with several servers must not hand them all the same name: the
// address is the only thing telling them apart.
func TestFlattenNamed_MultiServerProfileKeepsPerServerRemarks(t *testing.T) {
	profiles, err := parseProfiles([]byte(profileFixture))
	if err != nil {
		t.Fatalf("parseProfiles: %v", err)
	}
	entries := FlattenNamed(profiles)
	if len(entries) != 3 {
		t.Fatalf("entries = %d, want 3", len(entries))
	}
	if entries[0].Remarks == entries[1].Remarks {
		t.Errorf("two servers of one profile share Remarks %q", entries[0].Remarks)
	}
	// The lone server of the second profile still gets its location name.
	if entries[2].Remarks != "🇲🇩 Молдова" {
		t.Errorf("entries[2].Remarks = %q, want the profile name", entries[2].Remarks)
	}
}
