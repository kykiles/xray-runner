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
