package subscription

import (
	"encoding/json"
	"fmt"
)

// A Profile is one entry of a JSON subscription: a named group of servers that
// the panel intends to be used together, often behind a balancer and with its
// own routing rules. URL-list subscriptions have no such grouping and collapse
// into a single unnamed profile.
type Profile struct {
	Name     string          // remarks from the config, empty for URL lists
	Entries  []SubEntry      // proxy servers inside the profile
	Balancer *BalancerInfo   // nil when the profile has a single outbound
	Raw      json.RawMessage // original config, used to launch the profile as-is
}

// BalancerInfo describes how the profile spreads traffic across its servers.
type BalancerInfo struct {
	Tag      string
	Strategy string // leastLoad, leastPing, random, roundRobin...
}

// Mode renders the balancing strategy for the profile list.
func (p Profile) Mode() string {
	if p.Balancer == nil {
		return "single"
	}
	if p.Balancer.Strategy == "" {
		return "balancer"
	}
	return "balancer/" + p.Balancer.Strategy
}

// FetchProfiles loads a subscription preserving its profile structure. Fetch
// stays flat for callers that only need the server list (--dump-links, scripted
// selection).
func FetchProfiles(rawURL string, opts ...FetchOption) ([]Profile, error) {
	rawURL, err := unwrapHapp(rawURL)
	if err != nil {
		return nil, err
	}

	// A bare link is one server and nothing to fetch. It becomes an unnamed
	// profile with no Raw, so the menu skips the profile screen and the session
	// runs it as a single server (with the template's routing) rather than as a
	// panel profile.
	if IsBareLink(rawURL) {
		e, err := ParseBareLink(rawURL)
		if err != nil {
			return nil, err
		}
		return []Profile{{Entries: []SubEntry{*e}}}, nil
	}

	body, err := fetchBody(rawURL, opts...)
	if err != nil {
		return nil, err
	}
	return parseProfiles(body)
}

// FetchProfilesWithHWID mirrors FetchWithHWID for the profile-aware path.
func FetchProfilesWithHWID(rawURL, hwid, deviceOS, deviceModel string) ([]Profile, error) {
	return FetchProfiles(rawURL, WithHWID(hwid, deviceOS, deviceModel))
}

func parseProfiles(raw []byte) ([]Profile, error) {
	decoded, err := tryBase64Decode(raw)
	if err != nil {
		decoded = raw
	}

	var arr []json.RawMessage
	if err := json.Unmarshal(decoded, &arr); err == nil && isXrayConfigArray(arr) {
		return parseXrayConfigProfiles(arr)
	}

	// Any other shape has no profile structure — reuse the flat parser.
	entries, err := parse(decoded)
	if err != nil {
		return nil, err
	}
	return []Profile{{Entries: entries}}, nil
}

// AllSingle reports whether no profile balances anything. Panels that publish
// one config per location produce exactly this: a list of "profiles" that each
// hold a single server, which is a server list wearing a profile costume.
func AllSingle(profiles []Profile) bool {
	for _, p := range profiles {
		if p.Balancer != nil {
			return false
		}
	}
	return true
}

// FlattenUnique builds the flat server list the scripted path indexes with
// --server N. Panel configs repeat the same server across profiles on purpose,
// so those are deduplicated; a URL list is left exactly as written, since its
// repeats are the user's own and dropping one would shift every index below it.
func FlattenUnique(profiles []Profile) []SubEntry {
	fromPanel := false
	for _, p := range profiles {
		if len(p.Raw) > 0 {
			fromPanel = true
			break
		}
	}
	if !fromPanel {
		return Flatten(profiles)
	}

	var entries []SubEntry
	seen := map[string]bool{}
	for _, e := range Flatten(profiles) {
		key := entryKey(e)
		if seen[key] {
			continue
		}
		seen[key] = true
		entries = append(entries, e)
	}
	return entries
}

// ProfileFor finds the profile a server came from. The flat server screen mixes
// servers from every profile, and the session needs the owner to run under its
// routing. Matching is by endpoint and credentials: flattening rewrites Remarks.
// nil when the entry belongs to no profile.
func ProfileFor(profiles []Profile, e *SubEntry) *Profile {
	key := entryKey(*e)
	for i := range profiles {
		for _, p := range profiles[i].Entries {
			if entryKey(p) == key {
				return &profiles[i]
			}
		}
	}
	return nil
}

// FlattenNamed merges profiles into a server list, naming a lone server after
// its profile. The Xray-config parser seeds Remarks with the address, so the
// location name the panel put in "remarks" would otherwise be lost — unlike
// Flatten, which only fills an empty Remarks. Profiles holding several servers
// keep per-server Remarks: one shared name would make them indistinguishable.
func FlattenNamed(profiles []Profile) []SubEntry {
	var out []SubEntry
	for _, p := range profiles {
		for _, e := range p.Entries {
			if p.Name != "" && len(p.Entries) == 1 {
				e.Remarks = p.Name
			}
			out = append(out, e)
		}
	}
	return out
}

// Flatten merges profile servers into one list, tagging each server with its
// profile name so a flat consumer can still tell them apart.
func Flatten(profiles []Profile) []SubEntry {
	var out []SubEntry
	for _, p := range profiles {
		for _, e := range p.Entries {
			if p.Name != "" && e.Remarks == "" {
				e.Remarks = p.Name
			}
			out = append(out, e)
		}
	}
	return out
}

func profileError(n int) error {
	return fmt.Errorf("no usable profiles found in subscription (%d configs)", n)
}
