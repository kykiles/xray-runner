package app

// Menu navigation: subscriptions → profiles → servers, with esc walking back up
// one level at a time. The state lives across sessions so that "back" from a
// running VPN returns to the server list without re-fetching the subscription.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"runtime"
	"time"

	"xray-runner/internal/config"
	"xray-runner/internal/subscription"
	"xray-runner/internal/tui"
	"xray-runner/internal/ui"
)

// target is what a session runs: either a single server or a whole balancer
// profile with its own routing.
type target struct {
	subURL      string
	profileName string
	entry       *subscription.SubEntry  // nil for a balancer profile
	profileRaw  json.RawMessage         // nil for a single server
	profileSrvs []subscription.SubEntry // servers behind the profile's balancer
}

func (t target) isProfile() bool { return t.entry == nil }

// serverHosts lists every server the session may connect to. TUN routing must
// keep all of them on the physical path: a balancer rotates across the whole
// list, and a server left inside the tunnel deadlocks xray's own uplink.
func (t target) serverHosts() []string {
	if !t.isProfile() {
		return []string{t.entry.Address}
	}
	hosts := make([]string, 0, len(t.profileSrvs))
	seen := map[string]bool{}
	for _, e := range t.profileSrvs {
		if e.Address != "" && !seen[e.Address] {
			seen[e.Address] = true
			hosts = append(hosts, e.Address)
		}
	}
	return hosts
}

// title names the target for the status screen.
func (t target) title() string {
	if t.isProfile() {
		return t.profileName
	}
	name := t.entry.Remarks
	if name == "" {
		name = t.entry.Address
	}
	if t.profileName != "" && t.profileName != name {
		return t.profileName + " · " + name
	}
	return name
}

type navLevel int

const (
	levelSubs navLevel = iota
	levelProfiles
	levelServers
)

// backLevel is where "back" from a running session lands. A profile was picked
// on the profile screen, so its server list is not a level the user ever passed
// through — returning there would show servers they never chose from.
func backLevel(t *target) navLevel {
	if t.isProfile() {
		return levelProfiles
	}
	return levelServers
}

// nav holds the menu position between sessions.
type nav struct {
	subs     []subscription.NamedSubscription
	subIdx   int
	profiles []subscription.Profile
	profIdx  int
	level    navLevel
	// flat marks a subscription whose profiles balance nothing: a URL list, or
	// a panel publishing one config per location. Its profile screen would be a
	// server list in disguise, so the menu shows the servers directly.
	flat bool
}

// entries lists the servers the server screen shows: every server of the
// subscription when flat, otherwise the ones inside the chosen profile.
func (n *nav) entries() []subscription.SubEntry {
	if n.flat {
		return subscription.FlattenNamed(n.profiles)
	}
	return n.profiles[n.profIdx].Entries
}

// profileTitle names the profile the servers came from; a flat subscription has
// no profile to name.
func (n *nav) profileTitle() string {
	if n.flat {
		return ""
	}
	return n.profiles[n.profIdx].Name
}

// chooseTarget runs the menu until the user picks something to connect to.
// It starts at nav.level, so resuming after a session reopens the screen the
// user came from.
func (a *App) chooseTarget(ctx context.Context) (*target, error) {
	if a.nav.subs == nil {
		subs, err := subscription.LoadSubscriptions()
		if err != nil {
			return nil, fmt.Errorf("load subscriptions: %w", err)
		}
		a.nav.subs = subs
	}

	cb := tui.SubsCallbacks{
		Add:    addSubscription,
		Delete: subscription.RemoveSubscription,
		Reload: subscription.LoadSubscriptions,
		Mask:   func(raw string) string { return maskURL(raw, a.cfg.MaskCreds) },
		// Load fetches the chosen subscription's profiles from inside the
		// subscription screen, so the shell does not flash during the network
		// fetch (task #3). Results are stashed in a.nav for the next level.
		Load: func(index int) error {
			profiles, err := a.loadProfiles(a.nav.subs[index].URL)
			if err != nil {
				return err
			}
			a.nav.subIdx = index
			a.nav.profiles = profiles
			a.nav.profIdx = 0
			a.nav.flat = subscription.AllSingle(profiles)
			return nil
		},
	}

	for {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		switch a.nav.level {
		case levelSubs:
			subs, _, action, err := tui.SelectSubscription(a.nav.subs, cb)
			if err != nil {
				return nil, fmt.Errorf("TUI: %w", err)
			}
			a.nav.subs = subs
			if action == tui.SubsQuit {
				// ErrUserQuit propagates up to main for a single clean exit (R-4).
				return nil, ErrUserQuit
			}
			if action == tui.SubsUpdate {
				// Update core/geo, then return to the subscription list.
				if err := tui.RunUpdate(ctx, a.binary, coreVersion(a.binary)); err != nil {
					ui.Error(err.Error())
				}
				continue
			}
			// SubsSelected: cb.Load already fetched the profiles into a.nav.
			a.nav.level = levelProfiles

		case levelProfiles:
			// Nothing to choose when no profile balances anything — the screen is
			// skipped in both directions.
			if a.nav.flat {
				a.nav.profIdx = 0
				a.nav.level = levelServers
				continue
			}
			pb := NewProxyBenchmarker(a.template, a.binary, 3, 8*time.Second, a.cfg.AllowInsecure)
			idx, action, err := tui.SelectProfile(ctx, a.nav.profiles, pb.RunProfiles)
			if err != nil {
				return nil, fmt.Errorf("TUI: %w", err)
			}
			switch action {
			case tui.ProfileQuit:
				return nil, ErrUserQuit
			case tui.ProfileBack:
				a.nav.level = levelSubs
			case tui.ProfileExpand:
				a.nav.profIdx = idx
				a.nav.level = levelServers
			case tui.ProfileRun:
				p := a.nav.profiles[idx]
				a.nav.profIdx = idx
				return &target{
					subURL:      a.nav.subs[a.nav.subIdx].URL,
					profileName: p.Name,
					profileRaw:  p.Raw,
					profileSrvs: p.Entries,
				}, nil
			}

		case levelServers:
			t, err := a.selectServerInProfile(ctx)
			if err != nil {
				return nil, err
			}
			if t == nil {
				continue // the level already moved (back)
			}
			return t, nil
		}
	}
}

func (a *App) selectServerInProfile(ctx context.Context) (*target, error) {
	subURL := a.nav.subs[a.nav.subIdx].URL

	// A-4: refresh re-fetches with the same HWID headers as the initial load,
	// and keeps the profile structure so the same profile is shown again.
	refresh := func() ([]subscription.SubEntry, error) {
		profiles, err := a.loadProfiles(subURL)
		if err != nil {
			return nil, err
		}
		a.nav.profiles = profiles
		a.nav.flat = subscription.AllSingle(profiles)
		if a.nav.profIdx >= len(profiles) {
			a.nav.profIdx = 0
		}
		return a.nav.entries(), nil
	}

	pb := NewProxyBenchmarker(a.template, a.binary, 3, 8*time.Second, a.cfg.AllowInsecure)
	selected, action, err := tui.SelectServer(ctx, a.nav.profileTitle(), a.nav.entries(), refresh, pb.Run)
	if err != nil {
		return nil, fmt.Errorf("TUI: %w", err)
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	switch action {
	case tui.ServerQuit:
		// U-1: q/Ctrl+C exits the whole app, not just the screen.
		return nil, ErrUserQuit
	case tui.ServerBack:
		// Nothing to go back to when the profile screen was skipped.
		if a.nav.flat {
			a.nav.level = levelSubs
		} else {
			a.nav.level = levelProfiles
		}
		return nil, nil
	}

	if err := selected.Validate(); err != nil {
		return nil, fmt.Errorf("выбранный сервер невалиден: %w", err)
	}
	selected.AllowInsecure = a.cfg.AllowInsecure
	a.rememberSelection(subURL, selected)

	return &target{
		subURL:      subURL,
		profileName: a.nav.profileTitle(),
		entry:       selected,
	}, nil
}

// loadProfiles fetches between two full-screen menus, so it prints nothing: the
// next screen would overwrite a progress line anyway.
func (a *App) loadProfiles(subURL string) ([]subscription.Profile, error) {
	hwid := config.GetOrCreateHWID(a.cfg.HWID)
	profiles, err := subscription.FetchProfilesWithHWID(subURL, hwid, runtime.GOOS, a.cfg.HWIDDeviceModel)
	if err != nil {
		return nil, fmt.Errorf("загрузка подписки: %w", err)
	}
	slog.Info("subscription loaded", "profiles", len(profiles), "servers", len(subscription.Flatten(profiles)))
	return profiles, nil
}
