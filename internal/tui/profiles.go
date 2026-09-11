package tui

// Profile-side types and helpers shared by the list screen: the whole-profile
// benchmark and config callbacks, and the helpers that give a balancer profile
// its ping-cache key and its display face. The list model lives in
// list_screen.go.

import (
	"context"

	"xray-runner/internal/subscription"
)

// ProfileBenchmarkFunc measures whole profiles through their own balancers;
// onResult fires per finished profile.
type ProfileBenchmarkFunc func(ctx context.Context, profiles []subscription.Profile, onResult func(subscription.BenchmarkResult)) []subscription.BenchmarkResult

// ProfileRefreshFunc re-fetches the subscription past the menu's cache, exactly
// as `r` does on the list screen. ctx is the screen's: it ends when the screen
// closes.
type ProfileRefreshFunc func(ctx context.Context) ([]subscription.Profile, error)

// ProfileConfigFunc renders the full xray config running the profile would
// produce: its outbounds, balancer, dns and routing rules.
type ProfileConfigFunc func(p subscription.Profile) (string, error)

// profileKey names a profile in the ping cache. Name alone is what the user
// sees, but panels repeat names across locations, so the first server's endpoint
// goes in too — the pair survives a refresh that shifts positions.
func profileKey(p subscription.Profile) string {
	return p.Name + "|" + pingKey(face(p))
}

// face is the server a profile puts on display. Behind a balancer they are
// interchangeable endpoints, so the first one stands in for the group.
func face(p subscription.Profile) subscription.SubEntry {
	if len(p.Entries) == 0 {
		return subscription.SubEntry{}
	}
	return p.Entries[0]
}
