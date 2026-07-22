package tui

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"xray-runner/internal/subscription"
)

// ProfileBenchmarkFunc measures whole profiles through their own balancers;
// onResult fires per finished profile.
type ProfileBenchmarkFunc func(ctx context.Context, profiles []subscription.Profile, onResult func(subscription.BenchmarkResult)) []subscription.BenchmarkResult

// ProfileAction is the outcome of the profile screen.
type ProfileAction int

const (
	ProfileRun    ProfileAction = iota // launch the profile as-is (balancer + its routing)
	ProfileExpand                      // drill down into the profile's servers
	ProfileBack                        // back to the subscription list
	ProfileQuit
)

// ProfileConfigFunc renders the full xray config running the profile would
// produce: its outbounds, balancer, dns and routing rules.
type ProfileConfigFunc func(p subscription.Profile) (string, error)

type profilesModel struct {
	ctx      context.Context
	profiles []subscription.Profile
	bench    ProfileBenchmarkFunc
	preview  ProfileConfigFunc
	saveCfg  SaveConfigFunc
	cfg      cfgView
	cursor   int
	action   ProfileAction
	choice   int

	results map[int]subscription.BenchmarkResult // key: profile index
	pings   PingCache                            // the same results, kept across screens
	filter  filterState
	run     benchState
	status  string
	width   int // terminal width; 0 until the first WindowSizeMsg
	height  int // terminal height; 0 until the first WindowSizeMsg
}

// SelectProfile shows the profiles of a JSON subscription. It returns the chosen
// profile index together with what the user wants done with it. cursor is the
// profile picked last time, so coming back lands on it instead of the top.
// pings carries the measurements across screens, exactly as on the server list:
// the screen writes into it as results arrive, so connecting and coming back
// shows the numbers instead of an empty column.
func SelectProfile(ctx context.Context, profiles []subscription.Profile, cursor int, pings PingCache, bench ProfileBenchmarkFunc, preview ProfileConfigFunc, save SaveConfigFunc) (int, ProfileAction, error) {
	// Leaving the screen ends its benchmark — see SelectServer.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	if pings == nil {
		pings = PingCache{}
	}

	m := profilesModel{
		ctx:      ctx,
		profiles: profiles,
		bench:    bench,
		preview:  preview,
		saveCfg:  save,
		action:   ProfileQuit,
		choice:   -1,
		results:  profilePings(profiles, pings),
		pings:    pings,
		filter:   newFilter(),
	}
	if cursor > 0 && cursor < len(profiles) {
		m.cursor = cursor
	}

	res, err := runScreen(m)
	if err != nil {
		return -1, ProfileQuit, err
	}
	final := res.(profilesModel)
	return final.choice, final.action, nil
}

func (m profilesModel) Init() tea.Cmd { return nil }

func (m profilesModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil
	case benchResultMsg:
		// A cancelled run keeps emitting for a moment; its numbers belong to a
		// selection the user has already moved on from.
		if !m.run.accept(msg.gen) {
			return m, nil
		}
		m.results[msg.Index] = msg.BenchmarkResult
		m.pings[profileKey(m.profiles[msg.Index])] = msg.BenchmarkResult
		m.run.done++
		return m, waitBench(m.run.ch, msg.gen)
	case benchDoneMsg:
		if !m.run.accept(msg.gen) {
			return m, nil
		}
		m.run.stop()
		m.status = okStyle.Render("Пинг завершён")
		return m, nil
	case tea.KeyMsg:
		return m.updateKey(msg)
	}
	return m, nil
}

func (m profilesModel) updateKey(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	// U-1: Ctrl+C always quits.
	if key.Type == tea.KeyCtrlC {
		m.action = ProfileQuit
		return m, tea.Quit
	}

	// The config viewer takes over the screen: it only scrolls and closes.
	if m.cfg.open() {
		if m.cfg.key(key.String(), m.height, m.width) {
			m.action = ProfileQuit
			return m, tea.Quit
		}
		return m, nil
	}

	if handled, cmd := m.filter.key(key); handled {
		m.clampCursor()
		return m, cmd
	}

	// Cyrillic twins mirror the Russian layout (task #7): q→й, b→и, j→о, k→л, l→д.
	switch key.String() {
	case "q", "й":
		m.action = ProfileQuit
		return m, tea.Quit
	case "esc", "left":
		if m.filter.clear() {
			m.clampCursor()
			return m, nil
		}
		m.action = ProfileBack
		return m, tea.Quit
	case "up", "k", "л":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j", "о":
		if m.cursor < len(m.visible())-1 {
			m.cursor++
		}
	case "/", "f", "а":
		m.filter.start()
		m.status = ""
	case "b", "и":
		if m.bench == nil {
			return m, nil
		}
		// Ping what is on screen, and restart on a second press — same as the
		// server list.
		vis := m.visible()
		if len(vis) == 0 {
			return m, nil
		}
		profiles := make([]subscription.Profile, len(vis))
		for i, idx := range vis {
			profiles[i] = m.profiles[idx]
		}
		m.status = ""
		bench := m.bench
		return m, m.run.start(m.ctx, len(profiles), func(ctx context.Context, on func(subscription.BenchmarkResult)) {
			// The benchmark indexes the slice it got; map back to profile indices,
			// which is what m.results is keyed by.
			bench(ctx, profiles, func(r subscription.BenchmarkResult) {
				r.Index = vis[r.Index]
				on(r)
			})
		})
	case "c", "с":
		m.showConfig()
	case "s", "ы":
		// Peek inside a balancer: see, ping and pick its servers one by one. A
		// single-server profile has nothing to unfold.
		p, idx, ok := m.current()
		if !ok || p.Balancer == nil {
			return m, nil
		}
		m.action = ProfileExpand
		m.choice = idx
		return m, tea.Quit
	case "enter", "right", "l", "д":
		p, idx, ok := m.current()
		if !ok {
			return m, nil
		}
		m.choice = idx
		// A profile without a balancer has nothing to balance: hand it over as
		// its servers rather than launching a pointless one-outbound config. A
		// lone server connects straight away — see App.singleServerTarget.
		if p.Balancer == nil {
			m.action = ProfileExpand
		} else {
			m.action = ProfileRun
		}
		return m, tea.Quit
	}
	return m, nil
}

// visible returns profile indices matching the filter, searched over the same
// columns the table shows.
func (m profilesModel) visible() []int {
	return m.filter.visible(len(m.profiles), func(i int) string {
		return strings.ToLower(m.profiles[i].Name) + " " + entryHaystack(face(m.profiles[i]))
	})
}

// current is the profile under the cursor together with its index in the full
// list — the cursor counts visible rows, everything else counts profiles.
func (m profilesModel) current() (subscription.Profile, int, bool) {
	vis := m.visible()
	if m.cursor >= len(vis) {
		return subscription.Profile{}, -1, false
	}
	idx := vis[m.cursor]
	return m.profiles[idx], idx, true
}

func (m *profilesModel) clampCursor() {
	if n := len(m.visible()); m.cursor >= n {
		m.cursor = max(0, n-1)
	}
}

// showConfig opens the viewer on the full config the profile under the cursor
// would launch — including the single-server profiles, which have no server
// screen to open it from.
func (m *profilesModel) showConfig() {
	p, _, ok := m.current()
	if !ok || m.preview == nil {
		return
	}
	text, err := m.preview(p)
	if err != nil {
		m.status = errStyle.Render(fmt.Sprintf("Конфиг недоступен: %v", err))
		return
	}
	name := p.Name
	if name == "" {
		name = "(без имени)"
	}
	m.cfg.show(name, text, saver(m.saveCfg, name, text))
}

// profileKey names a profile in the ping cache. Name alone is what the user
// sees, but panels repeat names across locations, so the first server's endpoint
// goes in too — the pair survives a refresh that shifts positions.
func profileKey(p subscription.Profile) string {
	return p.Name + "|" + pingKey(face(p))
}

func profilePings(profiles []subscription.Profile, c PingCache) map[int]subscription.BenchmarkResult {
	return c.restoreBy(len(profiles), func(i int) string { return profileKey(profiles[i]) })
}

// face is the server a profile puts on display. Behind a balancer they are
// interchangeable endpoints, so the first one stands in for the group.
func face(p subscription.Profile) subscription.SubEntry {
	if len(p.Entries) == 0 {
		return subscription.SubEntry{}
	}
	return p.Entries[0]
}

// keys is the legend. "s серверы" shows up only on a balancer row — that is the
// only place there is anything to unfold.
func (m profilesModel) keys() string {
	if m.filter.typing {
		return filterKeys
	}
	keys := "  ↑/↓ выбор · → подключить"
	if p, _, ok := m.current(); ok && p.Balancer != nil {
		keys += " · s серверы"
	}
	keys += " · c конфиг"
	if m.bench != nil {
		keys += " · b пинг"
	}
	return keys + " · / фильтр · ← назад · q выход"
}

func (m profilesModel) View() string {
	if m.cfg.open() {
		return m.cfg.view(m.width, m.height)
	}
	var b strings.Builder
	b.WriteString(titleStyle.Render("Серверы") + "\n\n")

	keys := m.keys()

	// The filter row costs the same two lines it does on the server screen.
	extra := 0
	if m.filter.shown() {
		b.WriteString("  Фильтр: " + m.filter.input.View() + "\n\n")
		extra = 2
	}

	// The PING column appears only once a measurement is running or done, so the
	// list stays narrow until there is anything to show there.
	showPing := m.run.running || len(m.results) > 0

	vis := m.visible()
	start, end := 0, len(vis)
	var above, below int
	if len(vis) == 0 {
		b.WriteString(dimStyle.Render("  Ничего не найдено") + "\n")
	} else {
		b.WriteString(tableHead(showPing))
		budget := rowBudget(m.height, m.width, keys, len(vis), extra)
		start, end, above, below = window(len(vis), m.cursor, budget)
	}
	best := bestResult(m.results)

	b.WriteString(moreUp(above))
	for pos := start; pos < end; pos++ {
		idx := vis[pos]
		p := m.profiles[idx]
		cursor := "  "
		if pos == m.cursor {
			cursor = cursorStyle.Render("▸ ")
		}

		name := p.Name
		if name == "" {
			name = "(без имени)"
		}
		// Gold marks a profile that hides a balancer — the rows worth opening with
		// s, and the only ones that unfold into a server list.
		line := tableRow(name, face(p), showPing)
		switch {
		case p.Balancer != nil:
			line = goldStyle.Bold(pos == m.cursor).Render(line)
		case pos == m.cursor:
			line = selectedStyle.Render(line)
		}

		r, measured := m.results[idx]
		line += pingCell(r, measured, m.run.running, idx == best)
		b.WriteString("  " + cursor + clip(line, m.width-4) + "\n")
	}
	b.WriteString(moreDown(below))

	// The block always occupies its two lines, empty or not — see the budget above.
	if m.run.running {
		b.WriteString("\n  " + benchLine(m.run) + "\n")
	} else {
		b.WriteString("\n  " + m.status + "\n")
	}

	b.WriteString(legend(m.width, keys))
	return b.String()
}
