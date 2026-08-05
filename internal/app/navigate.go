package app

// Menu navigation: subscriptions → the unified server/balancer list, with esc
// walking back up one level at a time. The state lives across sessions so that
// "back" from a running VPN returns to the list without re-fetching the
// subscription.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"runtime"
	"slices"
	"time"

	"xray-runner/internal/config"
	"xray-runner/internal/subscription"
	"xray-runner/internal/system"
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
	levelList          // the unified server/balancer list — one screen for every shape
)

// nav holds the menu position between sessions.
type nav struct {
	subs     []subscription.NamedSubscription
	subIdx   int
	profiles []subscription.Profile
	profIdx  int // profile behind the current balancer target, for the status screen
	level    navLevel
	// loadedURL/loadedAt back the fetch cache: walking back to the subscription
	// list and into the same subscription again used to re-download it every
	// time. `r` on the server screen forces a fresh fetch.
	loadedURL string
	loadedAt  time.Time
	// filter is the list screen's search, kept across a session so that going
	// back from a connection shows the list the user left. The screen itself
	// clears it on ← (first press drops the filter, second one goes back), so
	// walking up a level resets it without any help from here.
	filter string
	// pings keeps the last per-server measurement for the whole run, so coming
	// back from a session shows the numbers instead of an empty column. profPings
	// does the same for whole balancers, which are measured as a group and keep
	// their own numbers.
	pings     tui.PingCache
	profPings tui.PingCache
	// geo is what the loaded subscription wants its rules resolved against. It
	// is kept because the cache below can hand the subscription back without a
	// fetch, and the databases in use must still follow the subscription.
	geo subscription.PanelInfo
}

// subCacheTTL is how long a fetched subscription is reused before the menu goes
// back to the panel for it.
const subCacheTTL = 10 * time.Minute

// setProfiles stores a freshly fetched subscription and stamps the cache.
func (n *nav) setProfiles(subURL string, profiles []subscription.Profile, geo subscription.PanelInfo) {
	// A fresh fetch is a new list: the old numbers belong to profiles that may no
	// longer be there, and `r` is how the user asks to measure again.
	clear(n.profPings)
	n.profiles = profiles
	n.geo = geo
	n.loadedURL = subURL
	n.loadedAt = time.Now()
}

// fresh reports whether the subscription already in hand can be shown as-is.
func (n *nav) fresh(subURL string) bool {
	return n.loadedURL == subURL && len(n.profiles) > 0 && time.Since(n.loadedAt) < subCacheTTL
}

// profileOwner returns the profile whose routing a picked server must run
// under. The list screen knows exactly which profile a row came from and hands
// its index back, so there is no endpoint guessing — a panel that lists the same
// endpoint under several profiles no longer risks pinning it to the wrong one.
// A flat server (profIdx < 0) has no owning profile in the tree and falls back
// to the endpoint match.
func (n *nav) profileOwner(profIdx int, e *subscription.SubEntry) *subscription.Profile {
	if profIdx >= 0 && profIdx < len(n.profiles) {
		return &n.profiles[profIdx]
	}
	return subscription.ProfileFor(n.profiles, e)
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
		Add:    a.addAndName,
		Delete: subscription.RemoveSubscription,
		Reload: subscription.LoadSubscriptions,
		// Load fetches the chosen subscription's profiles from inside the
		// subscription screen, so the shell does not flash during the network
		// fetch (task #3). Results are stashed in a.nav for the next level.
		Load: func(rawURL string) error {
			if a.nav.fresh(rawURL) {
				// Cached profiles skip the fetch, but the databases in use are
				// global: another subscription opened in between has left its own
				// in place.
				a.useGeoAssets(rawURL, a.nav.geo)
				return nil
			}
			profiles, geo, err := a.loadProfiles(rawURL, "open")
			if err != nil {
				return err
			}
			a.nav.setProfiles(rawURL, profiles, geo)
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
			_, choice, action, err := tui.SelectSubscription(a.nav.subs, a.nav.subIdx, cb)
			if err != nil {
				return nil, fmt.Errorf("TUI: %w", err)
			}
			// The list comes back from disk, not from the screen's copy of it.
			// The screen captured its copy before cb.Load ran, and Load is where
			// adoptPanelTitle learns the panel's name for the subscription — taking
			// the screen's copy here threw that name away, so it only showed up
			// after an explicit refresh in the server list (task #6). Everything
			// that changes the list (add, delete, rename) writes to disk first, so
			// disk is the one copy that is never behind.
			if subs, err := subscription.LoadSubscriptions(); err == nil {
				a.nav.subs = subs
			}
			if action == tui.SubsQuit {
				// ErrUserQuit propagates up to main for a single clean exit (R-4).
				return nil, ErrUserQuit
			}
			if action == tui.SubsApps {
				if err := a.pickApps(); err != nil {
					tui.ReleaseScreen() // readable on the normal buffer, see menuLoop
					ui.Error(err.Error())
				}
				continue
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
			a.nav.level = levelList

		case levelList:
			t, err := a.selectFromList(ctx)
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

// pickApps runs the split-tunnel process picker and saves the result. The list
// is read fresh every time: the point of the screen is to tick what is running
// right now, and a cached one would offer processes that have since exited.
func (a *App) pickApps() error {
	procs, err := system.ListProcesses()
	if err != nil {
		return fmt.Errorf("список процессов: %w", err)
	}
	appsPath := config.Path(system.AppsFile)
	saved, err := system.LoadApps(appsPath)
	if err != nil {
		return fmt.Errorf("%s: %w", appsPath, err)
	}

	chosen, save, err := tui.SelectApps(procs, saved)
	if err != nil {
		return fmt.Errorf("TUI: %w", err)
	}
	if !save || slices.Equal(chosen, saved) {
		return nil
	}
	if err := system.SaveApps(appsPath, chosen); err != nil {
		return fmt.Errorf("сохранить %s: %w", appsPath, err)
	}
	// The running session, if any, keeps its own rules: they were installed at
	// connect and are torn down with it. The new list applies on the next one.
	a.splitApps = chosen
	return nil
}

// selectFromList runs the unified list screen: a flat server list, or the
// balancer tree, whichever the subscription is. It returns the target to connect
// (a server or a whole balancer), or nil when the user walked back to the
// subscription list.
func (a *App) selectFromList(ctx context.Context) (*target, error) {
	subURL := a.nav.subs[a.nav.subIdx].URL

	if a.nav.pings == nil {
		a.nav.pings = tui.PingCache{}
	}
	if a.nav.profPings == nil {
		a.nav.profPings = tui.PingCache{}
	}

	// A-4: `r` re-fetches with the same HWID headers as the initial load, keeping
	// the profile structure so the same list is shown again.
	refresh := func() ([]subscription.Profile, error) {
		profiles, info, err := a.loadProfiles(subURL, "refresh")
		if err != nil {
			return nil, err
		}
		a.nav.setProfiles(subURL, profiles, info)
		a.nav.profIdx = 0
		return profiles, nil
	}

	// Start the cursor on the server connected to last time, so returning to the
	// list shows where the user left off. Only for the same subscription — the
	// saved address means nothing in another one.
	var lastAddress string
	var lastPort int
	if state, err := loadLastState(); err == nil && state != nil && state.SubscriptionURL == subURL {
		lastAddress, lastPort = state.ServerAddress, state.ServerPort
	}

	pb := NewProxyBenchmarker(a.template, a.binary, a.cfg)
	action, profIdx, entry, filter, err := tui.SelectList(
		ctx, a.nav.profiles, lastAddress, lastPort, a.nav.profIdx, a.nav.filter,
		a.nav.pings, a.nav.profPings, refresh, pb.Run, pb.RunProfiles,
		a.serverConfig, a.profileConfig, a.saveConfig,
	)
	if err != nil {
		return nil, fmt.Errorf("TUI: %w", err)
	}
	a.nav.filter = filter
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	switch action {
	case tui.ListQuit:
		// U-1: q/Ctrl+C exits the whole app, not just the screen.
		return nil, ErrUserQuit
	case tui.ListBack:
		a.nav.level = levelSubs
		return nil, nil
	case tui.ListRunBalancer:
		p := a.nav.profiles[profIdx]
		a.nav.profIdx = profIdx
		return &target{
			subURL:      subURL,
			profileName: p.Name,
			profileRaw:  p.Raw,
			profileSrvs: p.Entries,
		}, nil
	}

	// ListConnect: a single server, under its owning profile's routing.
	if err := entry.Validate(); err != nil {
		return nil, fmt.Errorf("выбранный сервер невалиден: %w", err)
	}
	entry.AllowInsecure = a.cfg.AllowInsecure
	a.rememberSelection(subURL, entry)

	owner := a.nav.profileOwner(profIdx, entry)
	t := &target{subURL: subURL, entry: entry}
	if owner != nil {
		t.profileName = owner.Name
	}
	t.attachOwner(owner)
	return t, nil
}

// serverConfig renders what connecting to this server would run: the same
// target the menu builds on Enter, so the preview carries the profile's routing.
// profIdx names the profile the row came from (-1 for a flat server), so the
// preview runs under the same owner the connection would.
func (a *App) serverConfig(e *subscription.SubEntry, profIdx int) (string, error) {
	entry := *e
	entry.AllowInsecure = a.cfg.AllowInsecure
	owner := a.nav.profileOwner(profIdx, &entry)
	t := &target{entry: &entry}
	if owner != nil {
		t.profileName = owner.Name
	}
	t.attachOwner(owner)
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
// next screen would overwrite a progress line anyway. why names what asked for
// the fetch: two "subscription loaded" lines in a row are only diagnosable if
// the log says which of them was the user pressing `r`.
func (a *App) loadProfiles(subURL, why string) ([]subscription.Profile, subscription.PanelInfo, error) {
	hwid := config.GetOrCreateHWID(a.cfg.HWID)
	profiles, info, err := subscription.FetchProfilesWithHWID(subURL, hwid, runtime.GOOS, a.cfg.HWIDDeviceModel)
	if err != nil {
		return nil, info, fmt.Errorf("загрузка подписки: %w", err)
	}
	// The panel's rules are written against the panel's geo databases, so switch
	// to them before anything builds a config from this subscription.
	a.useGeoAssets(subURL, info)
	a.adoptPanelTitle(subURL, info.Title)
	slog.Info("subscription loaded", "profiles", len(profiles), "servers", len(subscription.Flatten(profiles)), "why", why)
	return profiles, info, nil
}

// adoptPanelTitle names the subscription the way the panel does, so the list
// shows "🤝 alohavpnbot" instead of the host the URL happens to have. A name the
// user gave it stays theirs — NameSubscription only fills in a missing one.
func (a *App) adoptPanelTitle(subURL, title string) {
	if title == "" {
		return
	}
	if err := subscription.NameSubscription(subURL, title); err != nil {
		slog.Warn("название подписки не сохранено", "error", err)
		return
	}
	// The menu holds its own copy of the list; re-read it so the new name shows
	// on the way back instead of after a restart.
	if subs, err := subscription.LoadSubscriptions(); err == nil {
		a.nav.subs = subs
	}
}
