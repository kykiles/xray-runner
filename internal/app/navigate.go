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
	// fromProfiles marks a server connected straight from the profile screen,
	// so "back" returns there instead of a one-row server list.
	fromProfiles bool
}

func (t target) isProfile() bool { return t.entry == nil }

// attachProfile links a single server to the profile it came from, so the
// session runs under the panel's routing and dns instead of template.json. Both
// the menu and the scripted path go through here — a server whose subscription
// has no profile structure (URL list, bare link) simply finds no owner and stays
// on the template path.
func (t *target) attachProfile(profiles []subscription.Profile) {
	t.attachOwner(subscription.ProfileFor(profiles, t.entry))
}

// attachOwner links the server to an already known profile.
func (t *target) attachOwner(owner *subscription.Profile) {
	if owner == nil {
		return
	}
	t.profileRaw = owner.Raw
	t.profileSrvs = owner.Entries
}

// serverHosts lists every server the session may connect to. TUN routing must
// keep all of them on the physical path: a balancer rotates across the whole
// list, and a server left inside the tunnel deadlocks xray's own uplink.
func (t target) serverHosts() []string {
	hosts := make([]string, 0, len(t.profileSrvs)+1)
	seen := map[string]bool{}
	if !t.isProfile() {
		// A single server still runs under the profile's routing, which may name
		// any of its sibling outbounds — those must stay outside the tunnel too.
		hosts = append(hosts, t.entry.Address)
		seen[t.entry.Address] = true
	}
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
	if t.isProfile() || t.fromProfiles {
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
	// loadedURL/loadedAt back the fetch cache: walking back to the subscription
	// list and into the same subscription again used to re-download it every
	// time. `r` on the server screen forces a fresh fetch.
	loadedURL string
	loadedAt  time.Time
	// filter is the server screen's search, kept across a session so that going
	// back from a connection shows the list the user left. The screen itself
	// clears it on ← (first press drops the filter, second one goes back), so
	// walking up a level resets it without any help from here.
	filter string
	// pings keeps the last measurement of each server for the whole run, so
	// coming back from a session shows the numbers instead of an empty column.
	// profPings does the same for the profile screen, which measures whole
	// balancers and therefore keeps its own numbers.
	pings     tui.PingCache
	profPings tui.PingCache
}

// subCacheTTL is how long a fetched subscription is reused before the menu goes
// back to the panel for it.
const subCacheTTL = 10 * time.Minute

// setProfiles stores a freshly fetched subscription and stamps the cache.
func (n *nav) setProfiles(subURL string, profiles []subscription.Profile) {
	// A fresh fetch is a new list: the old numbers belong to profiles that may no
	// longer be there, and `r` is how the user asks to measure again.
	clear(n.profPings)
	n.profiles = profiles
	n.flat = subscription.AllSingle(profiles)
	n.loadedURL = subURL
	n.loadedAt = time.Now()
}

// fresh reports whether the subscription already in hand can be shown as-is.
func (n *nav) fresh(subURL string) bool {
	return n.loadedURL == subURL && len(n.profiles) > 0 && time.Since(n.loadedAt) < subCacheTTL
}

// entries lists the servers the server screen shows: every server of the
// subscription when flat, otherwise the ones inside the chosen profile.
func (n *nav) entries() []subscription.SubEntry {
	if n.flat {
		return subscription.FlattenNamed(n.profiles)
	}
	return n.profiles[n.profIdx].Entries
}

// owningProfile returns the profile whose routing a picked server must run
// under. Outside flat mode the server screen was opened from one specific
// profile, and that is the answer — a panel that publishes an autoselect
// profile next to per-location ones lists the same endpoint several times under
// different outbound tags, so matching by endpoint would pin the tag against a
// profile the user never chose and silently connect to another server. Only the
// flat screen, which mixes every profile's servers, has no opened profile to
// go by and falls back to the endpoint match.
func (n *nav) owningProfile(e *subscription.SubEntry) *subscription.Profile {
	if !n.flat && n.profIdx < len(n.profiles) {
		return &n.profiles[n.profIdx]
	}
	return subscription.ProfileFor(n.profiles, e)
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
		Load: func(rawURL string) error {
			if a.nav.fresh(rawURL) {
				return nil
			}
			profiles, err := a.loadProfiles(rawURL)
			if err != nil {
				return err
			}
			a.nav.setProfiles(rawURL, profiles)
			a.nav.profIdx = 0
			return nil
		},
	}

	for {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		switch a.nav.level {
		case levelSubs:
			// subIdx carries the last opened subscription back in, so returning
			// from a subscription lands the cursor on it instead of the top.
			subs, choice, action, err := tui.SelectSubscription(a.nav.subs, a.nav.subIdx, cb)
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
					tui.ReleaseScreen() // readable on the normal buffer, see menuLoop
					ui.Error(err.Error())
				}
				continue
			}
			// SubsSelected: cb.Load already fetched the profiles into a.nav.
			// choice indexes the reloaded list we just stored, so subIdx stays
			// valid even after an add/delete changed the list under us.
			a.nav.subIdx = choice
			a.nav.level = levelProfiles

		case levelProfiles:
			// Nothing to choose when no profile balances anything — the screen is
			// skipped in both directions.
			if a.nav.flat {
				a.nav.profIdx = 0
				a.nav.level = levelServers
				continue
			}
			if a.nav.profPings == nil {
				a.nav.profPings = tui.PingCache{}
			}
			pb := NewProxyBenchmarker(a.template, a.binary, a.cfg)
			idx, action, err := tui.SelectProfile(ctx, a.nav.profiles, a.nav.profIdx, a.nav.profPings, pb.RunProfiles, a.profileConfig, a.saveConfig)
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
				// A profile holding one server has nothing to pick from: connect to
				// it instead of showing a one-row list. Only balancers unfold.
				if p := a.nav.profiles[idx]; p.Balancer == nil && len(p.Entries) == 1 {
					return a.singleServerTarget(p)
				}
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

// singleServerTarget connects to the only server of a profile, under that
// profile's routing and dns — the same target the server screen would build.
func (a *App) singleServerTarget(p subscription.Profile) (*target, error) {
	subURL := a.nav.subs[a.nav.subIdx].URL
	entry := p.Entries[0]
	if err := entry.Validate(); err != nil {
		return nil, fmt.Errorf("выбранный сервер невалиден: %w", err)
	}
	entry.AllowInsecure = a.cfg.AllowInsecure
	a.rememberSelection(subURL, &entry)

	t := &target{
		subURL:       subURL,
		profileName:  p.Name,
		entry:        &entry,
		fromProfiles: true,
	}
	t.attachOwner(&p)
	return t, nil
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
		a.nav.setProfiles(subURL, profiles)
		if a.nav.profIdx >= len(profiles) {
			a.nav.profIdx = 0
		}
		return a.nav.entries(), nil
	}

	// Start the cursor on the server connected to last time, so returning to the
	// list shows where the user left off. Only for the same subscription — the
	// saved address means nothing in another one.
	var lastAddress string
	var lastPort int
	if state, err := loadLastState(); err == nil && state != nil && state.SubscriptionURL == subURL {
		lastAddress, lastPort = state.ServerAddress, state.ServerPort
	}

	if a.nav.pings == nil {
		a.nav.pings = tui.PingCache{}
	}

	pb := NewProxyBenchmarker(a.template, a.binary, a.cfg)
	selected, action, filter, err := tui.SelectServer(ctx, a.nav.profileTitle(), a.nav.entries(), lastAddress, lastPort, a.nav.filter, a.nav.pings, refresh, pb.Run, a.serverConfig, a.saveConfig)
	if err != nil {
		return nil, fmt.Errorf("TUI: %w", err)
	}
	a.nav.filter = filter
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

	t := &target{
		subURL:      subURL,
		profileName: a.nav.profileTitle(),
		entry:       selected,
	}
	t.attachOwner(a.nav.owningProfile(selected))
	return t, nil
}

// serverConfig renders what connecting to this server would run: the same
// target the menu builds on Enter, so the preview carries the profile's routing.
func (a *App) serverConfig(e *subscription.SubEntry) (string, error) {
	entry := *e
	entry.AllowInsecure = a.cfg.AllowInsecure
	t := &target{profileName: a.nav.profileTitle(), entry: &entry}
	t.attachOwner(a.nav.owningProfile(&entry))
	return a.previewConfig(t)
}

// profileConfig renders what running the whole profile would launch — the only
// way to see the config of a single-server profile, which never opens a server
// screen.
func (a *App) profileConfig(p subscription.Profile) (string, error) {
	return a.previewConfig(&target{
		profileName: p.Name,
		profileRaw:  p.Raw,
		profileSrvs: p.Entries,
	})
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
