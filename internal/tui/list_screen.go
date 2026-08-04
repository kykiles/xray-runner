package tui

// One list screen for every subscription shape (task #3). A flat subscription
// (URL list, JSON without balancers, one-config-per-location panel) shows a
// plain server list; a subscription with balancers shows the balancer rows,
// each unfolding in place into the servers behind it. The two used to be two
// near-identical screens (profilesModel / serversModel); this is the single
// model both collapse into.

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"xray-runner/internal/subscription"
)

// ListAction is the outcome of the list screen.
type ListAction int

const (
	ListConnect     ListAction = iota // connect the server under the cursor
	ListRunBalancer                   // launch a balancer profile as-is
	ListBack                          // back to the subscription list
	ListQuit
)

// listKind tags what a rendered row stands for.
type listKind int

const (
	rowServer   listKind = iota // a single server (flat, child, or single-server profile)
	rowBalancer                 // a balancer profile — gold, unfolds into its servers
	rowGroup                    // a non-balancer multi-server profile — unfolds too
)

// listRow is one visible line: a server or a foldable profile header.
type listRow struct {
	kind    listKind
	profIdx int                   // profile this row belongs to; -1 for flat servers
	entry   subscription.SubEntry // the server (rowServer) or the profile's face
	name    string
	depth   int // 0 top level, 1 a server nested under an unfolded profile
}

// benchTarget routes a streamed result back to the right cache: a balancer is
// measured as a whole (profPings), a server one by one (pings).
type benchTarget struct {
	key       string
	isProfile bool
}

// listRefreshDoneMsg carries a re-fetched subscription back to the screen.
type listRefreshDoneMsg struct {
	profiles []subscription.Profile
	err      error
}

type listModel struct {
	ctx      context.Context
	profiles []subscription.Profile
	flat     bool         // no balancer anywhere: render as a plain server list
	expanded map[int]bool // profile index -> its servers are unfolded

	refresh     ProfileRefreshFunc
	srvBench    BenchmarkFunc
	profBench   ProfileBenchmarkFunc
	srvPreview  ServerConfigFunc
	profPreview ProfileConfigFunc
	saveCfg     SaveConfigFunc

	cfg    cfgView
	cursor int // position within the visible rows
	filter filterState
	run    benchState

	// Results live in the two shared caches, keyed by endpoint / profile, so they
	// survive a session and a reorder. The screen reads them straight for display.
	pings     PingCache // per-server, keyed by pingKey
	profPings PingCache // per-balancer, keyed by profileKey
	// benchTargets is parallel to the current run's global result indices.
	benchTargets []benchTarget

	refreshing  bool
	lastRefresh time.Time // when the list last came from the panel — see refreshCooldown
	status      string
	width       int
	height      int

	action ListAction
	choice int // profile index for ListRunBalancer, -1 otherwise
	entry  *subscription.SubEntry
}

// SelectList shows the unified list and returns what to do with the pick. The
// cursor starts on the last connected server (lastAddress:lastPort) or the last
// run balancer (lastProfIdx), unfolding its profile if the server is nested.
// filter is restored on the way in and handed back out on exit.
func SelectList(
	ctx context.Context,
	profiles []subscription.Profile,
	lastAddress string, lastPort, lastProfIdx int,
	filter string,
	pings, profPings PingCache,
	refresh ProfileRefreshFunc,
	srvBench BenchmarkFunc,
	profBench ProfileBenchmarkFunc,
	srvPreview ServerConfigFunc,
	profPreview ProfileConfigFunc,
	save SaveConfigFunc,
) (ListAction, int, *subscription.SubEntry, string, error) {
	// Leaving the screen ends its benchmark — see benchState.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	if pings == nil {
		pings = PingCache{}
	}
	if profPings == nil {
		profPings = PingCache{}
	}
	fi := newFilter()
	fi.input.SetValue(filter)

	m := listModel{
		ctx:         ctx,
		profiles:    profiles,
		flat:        subscription.AllSingle(profiles),
		expanded:    map[int]bool{},
		refresh:     refresh,
		srvBench:    srvBench,
		profBench:   profBench,
		srvPreview:  srvPreview,
		profPreview: profPreview,
		saveCfg:     save,
		filter:      fi,
		pings:       pings,
		profPings:   profPings,
		action:      ListQuit,
		choice:      -1,
		// The list handed in was just fetched, so the cooldown starts here: `r`
		// pressed right after opening a subscription used to re-fetch it a second
		// later, two panel hits (and two HWID registrations) for one list.
		lastRefresh: time.Now(),
	}
	m.restoreCursor(lastAddress, lastPort, lastProfIdx)

	res, err := runScreen(m)
	if err != nil {
		return ListQuit, -1, nil, "", err
	}
	final := res.(listModel)
	return final.action, final.choice, final.entry, final.filter.value(), nil
}

func (m listModel) Init() tea.Cmd { return nil }

// rows expands the profiles into the ordered row list, honoring the fold state.
func (m listModel) rows() []listRow {
	if m.flat {
		entries := subscription.FlattenNamed(m.profiles)
		out := make([]listRow, len(entries))
		for i, e := range entries {
			out[i] = listRow{kind: rowServer, profIdx: -1, entry: e, name: orDash(mark(e))}
		}
		return out
	}

	var out []listRow
	for pi, p := range m.profiles {
		switch {
		case p.Balancer != nil:
			out = append(out, listRow{kind: rowBalancer, profIdx: pi, entry: face(p), name: profileName(p)})
			out = append(out, m.children(pi, p)...)
		case len(p.Entries) == 1:
			// A single-server profile is just a server, named after the profile.
			e := p.Entries[0]
			e.Remarks = profileName(p)
			out = append(out, listRow{kind: rowServer, profIdx: pi, entry: e, name: e.Remarks})
		default:
			out = append(out, listRow{kind: rowGroup, profIdx: pi, entry: face(p), name: profileName(p)})
			out = append(out, m.children(pi, p)...)
		}
	}
	return out
}

// children are the server rows unfolded under a balancer/group profile.
func (m listModel) children(pi int, p subscription.Profile) []listRow {
	if !m.expanded[pi] {
		return nil
	}
	out := make([]listRow, len(p.Entries))
	for ei, e := range p.Entries {
		out[ei] = listRow{kind: rowServer, profIdx: pi, entry: e, name: orDash(mark(e)), depth: 1}
	}
	return out
}

// visibleRows are the rows matching the filter, over the same columns the table
// shows.
func (m listModel) visibleRows() []listRow {
	all := m.rows()
	terms := strings.Fields(strings.ToLower(strings.TrimSpace(m.filter.value())))
	if len(terms) == 0 {
		return all
	}
	out := make([]listRow, 0, len(all))
	for _, r := range all {
		if matchTerms(strings.ToLower(r.name)+" "+entryHaystack(r.entry), terms) {
			out = append(out, r)
		}
	}
	return out
}

// profileName is the profile's display name, with a placeholder for an empty one.
func profileName(p subscription.Profile) string {
	if p.Name == "" {
		return "(без имени)"
	}
	return p.Name
}

// restoreCursor unfolds the profile holding the last connected server, then
// lands the cursor on that server, or on the last run balancer, or at the top.
func (m *listModel) restoreCursor(address string, port, profIdx int) {
	if address != "" && !m.flat {
		// Unfold whichever profile holds it, so the row is visible.
		for pi := range m.profiles {
			for _, e := range m.profiles[pi].Entries {
				if e.Address == address && e.Port == port {
					if m.profiles[pi].Balancer != nil || len(m.profiles[pi].Entries) > 1 {
						m.expanded[pi] = true
					}
				}
			}
		}
	}
	vis := m.visibleRows()
	for i, r := range vis {
		if r.kind == rowServer && r.entry.Address == address && r.entry.Port == port && address != "" {
			m.cursor = i
			return
		}
		if r.kind == rowBalancer && r.profIdx == profIdx && profIdx >= 0 {
			m.cursor = i
			return
		}
	}
	m.cursor = 0
}

func (m listModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case benchResultMsg:
		// A cancelled run keeps emitting for a moment; drop what it sends.
		if !m.run.accept(msg.gen) {
			return m, nil
		}
		if i := msg.Index; i >= 0 && i < len(m.benchTargets) {
			if bt := m.benchTargets[i]; bt.isProfile {
				m.profPings[bt.key] = msg.BenchmarkResult
			} else {
				m.pings[bt.key] = msg.BenchmarkResult
			}
		}
		m.run.done++
		return m, waitBench(m.run.ch, msg.gen)
	case benchDoneMsg:
		if !m.run.accept(msg.gen) {
			return m, nil
		}
		m.run.stop()
		m.status = okStyle.Render("Пинг завершён")
		return m, nil
	case listRefreshDoneMsg:
		m.refreshing = false
		m.lastRefresh = time.Now()
		if msg.err != nil {
			m.status = errStyle.Render(fmt.Sprintf("Ошибка обновления: %v", msg.err))
			return m, nil
		}
		m.profiles = msg.profiles
		m.flat = subscription.AllSingle(msg.profiles)
		m.expanded = map[int]bool{}
		clear(m.pings)     // the list is new; old numbers would outlive their servers
		clear(m.profPings) // the same for balancers
		m.cursor = 0
		m.status = okStyle.Render(fmt.Sprintf("Подписка обновлена: %d серверов", len(subscription.Flatten(msg.profiles))))
		return m, nil
	case tea.KeyMsg:
		return m.updateKey(msg)
	}
	return m, nil
}

func (m listModel) updateKey(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	// U-1: Ctrl+C always quits.
	if key.Type == tea.KeyCtrlC {
		m.action = ListQuit
		return m, tea.Quit
	}

	// The config viewer takes over the screen: it only scrolls and closes.
	if m.cfg.open() {
		if m.cfg.key(key.String(), m.height, m.width) {
			m.action = ListQuit
			return m, tea.Quit
		}
		return m, nil
	}

	if handled, cmd := m.filter.key(key); handled {
		m.clampCursor()
		return m, cmd
	}

	// Cyrillic twins mirror the Russian layout: q→й, b→и, r→к, j→о, k→л, s→ы,
	// f→а, l→д, c→с.
	switch key.String() {
	case "q", "й":
		m.action = ListQuit
		return m, tea.Quit
	case "esc", "left":
		if m.filter.clear() {
			m.clampCursor()
			return m, nil
		}
		m.action = ListBack
		return m, tea.Quit
	case "up", "k", "л":
		// Task #5: a completed-ping notice must not linger while the user keeps
		// using the screen, or it reads as a fresh measurement.
		m.clearBenchStatus()
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j", "о":
		m.clearBenchStatus()
		if m.cursor < len(m.visibleRows())-1 {
			m.cursor++
		}
	case "/", "f", "а":
		m.filter.start()
		m.status = ""
	case "s", "ы":
		m.clearBenchStatus()
		m.toggleFold()
	case "c", "с":
		m.clearBenchStatus()
		m.showConfig()
	case "b", "и":
		return m.startBench()
	case "r", "к":
		return m.startRefresh()
	case "enter", "right", "l", "д":
		return m.activate()
	}
	return m, nil
}

// current is the row under the cursor.
func (m listModel) current() (listRow, bool) {
	vis := m.visibleRows()
	if m.cursor < 0 || m.cursor >= len(vis) {
		return listRow{}, false
	}
	return vis[m.cursor], true
}

func (m *listModel) clampCursor() {
	if n := len(m.visibleRows()); m.cursor >= n {
		m.cursor = max(0, n-1)
	}
}

// clearBenchStatus drops the "Пинг завершён" notice; pings themselves stay.
func (m *listModel) clearBenchStatus() {
	if !m.run.running {
		m.status = ""
	}
}

// toggleFold unfolds/folds a balancer or group row.
func (m *listModel) toggleFold() {
	r, ok := m.current()
	if !ok || r.kind == rowServer {
		return
	}
	m.expanded[r.profIdx] = !m.expanded[r.profIdx]
	m.clampCursor()
}

// activate: connect a server, run a balancer, or unfold a group.
func (m listModel) activate() (tea.Model, tea.Cmd) {
	r, ok := m.current()
	if !ok {
		return m, nil
	}
	switch r.kind {
	case rowBalancer:
		m.action = ListRunBalancer
		m.choice = r.profIdx
		return m, tea.Quit
	case rowGroup:
		m.toggleFold()
		return m, nil
	default: // rowServer
		e := r.entry
		if !supportedProtocols[e.Protocol] {
			m.status = warnStyle.Render(fmt.Sprintf("Протокол %s не поддерживается — выберите другой", e.Protocol))
			return m, nil
		}
		if err := e.Validate(); err != nil {
			m.status = warnStyle.Render(fmt.Sprintf("Запись невалидна: %v — выберите другую", err))
			return m, nil
		}
		m.action = ListConnect
		m.choice = r.profIdx
		m.entry = &e
		return m, tea.Quit
	}
}

// startBench measures what is on screen: balancers as a whole, servers one by
// one (task #3, variant A). Both benchmarkers stream into one run, their indices
// mapped onto a shared range routed back by benchTargets.
func (m listModel) startBench() (tea.Model, tea.Cmd) {
	vis := m.visibleRows()
	var srvEntries []subscription.SubEntry
	var profs []subscription.Profile
	var targets []benchTarget
	seen := map[string]bool{}
	addSrv := func(e subscription.SubEntry) {
		key := pingKey(e)
		if seen[key] {
			return
		}
		seen[key] = true
		srvEntries = append(srvEntries, e)
		targets = append(targets, benchTarget{key: key})
	}
	for _, r := range vis {
		switch {
		case r.kind == rowServer:
			addSrv(r.entry)
		case m.expanded[r.profIdx]:
			// The children are visible rows of their own and were added above.
		case r.kind == rowBalancer:
			// A folded balancer is measured as a whole and nothing else: the number
			// on its row is the profile's, and its servers cost N extra measurements
			// nobody is looking at. Unfold it and press b again for those.
		default: // a folded group has no measurement of its own — its servers do
			for _, e := range m.profiles[r.profIdx].Entries {
				addSrv(e)
			}
		}
	}
	// Balancer targets follow the server ones, matching the run order below.
	for _, r := range vis {
		if r.kind == rowBalancer {
			profs = append(profs, m.profiles[r.profIdx])
			targets = append(targets, benchTarget{key: profileKey(m.profiles[r.profIdx]), isProfile: true})
		}
	}
	n := len(srvEntries) + len(profs)
	if n == 0 {
		return m, nil
	}
	m.benchTargets = targets
	m.status = ""

	srvBench, profBench := m.srvBench, m.profBench
	base := len(srvEntries)
	cmd := m.run.start(m.ctx, n, func(ctx context.Context, on func(subscription.BenchmarkResult)) {
		// ponytail: server group then balancer group, sequentially — a mixed screen
		// is uncommon and running both benchmarkers at once would double the live
		// xray instances (2×BENCH_CONCURRENCY). Split into goroutines if it bites.
		if srvBench != nil && len(srvEntries) > 0 {
			srvBench(ctx, srvEntries, on) // result.Index is already 0..len-1
		}
		if profBench != nil && len(profs) > 0 {
			profBench(ctx, profs, func(r subscription.BenchmarkResult) {
				r.Index = base + r.Index
				on(r)
			})
		}
	})
	return m, cmd
}

// refreshCooldown is the shortest gap between two forced re-fetches. `r` is the
// one button that must always hit the panel, so it is rate-limited rather than
// cached (task #5): held down, it would otherwise fire a request per keypress.
const refreshCooldown = 5 * time.Second

func (m listModel) startRefresh() (tea.Model, tea.Cmd) {
	// Refreshing mid-benchmark would swap the list out from under the running
	// measurement (H-1).
	if m.refresh == nil || m.refreshing || m.run.running {
		return m, nil
	}
	if wait := refreshCooldown - time.Since(m.lastRefresh); wait > 0 {
		m.status = warnStyle.Render(fmt.Sprintf("Только что обновлялось — подождите %ds", int(wait.Seconds())+1))
		return m, nil
	}
	m.refreshing = true
	m.status = ""
	refresh := m.refresh
	return m, func() tea.Msg {
		profiles, err := refresh()
		return listRefreshDoneMsg{profiles: profiles, err: err}
	}
}

// showConfig opens the viewer on the config the current row would launch: a
// server's session config, or a balancer's whole-profile config. A group row
// has no config of its own — its servers do.
func (m *listModel) showConfig() {
	r, ok := m.current()
	if !ok {
		return
	}
	switch r.kind {
	case rowServer:
		if m.srvPreview == nil {
			return
		}
		e := r.entry
		text, err := m.srvPreview(&e, r.profIdx)
		if err != nil {
			m.status = errStyle.Render(fmt.Sprintf("Конфиг недоступен: %v", err))
			return
		}
		title := orDash(mark(e))
		if title == "-" {
			title = e.Address
		}
		m.cfg.show(title, text, saver(m.saveCfg, title, text))
	case rowBalancer:
		if m.profPreview == nil {
			return
		}
		p := m.profiles[r.profIdx]
		text, err := m.profPreview(p)
		if err != nil {
			m.status = errStyle.Render(fmt.Sprintf("Конфиг недоступен: %v", err))
			return
		}
		m.cfg.show(profileName(p), text, saver(m.saveCfg, profileName(p), text))
	}
}

// keys is the bottom legend, adapting to the row under the cursor.
func (m listModel) keys() string {
	if m.filter.typing {
		return filterKeys
	}
	keys := "  ↑/↓ выбор · → подключить"
	r, ok := m.current()
	if ok && (r.kind == rowBalancer || r.kind == rowGroup) {
		keys += " · s серверы"
	}
	if ok && r.kind != rowGroup {
		keys += " · c конфиг"
	}
	if m.srvBench != nil || m.profBench != nil {
		keys += " · b пинг"
	}
	if m.refresh != nil {
		keys += " · r обновить"
	}
	return keys + " · / фильтр · ← назад · q выход"
}

func (m listModel) View() string {
	if m.cfg.open() {
		return m.cfg.view(m.width, m.height)
	}
	var b strings.Builder
	// The title carries the same two-column indent as the rows and legend, so the
	// whole screen lines up on one left edge (task #7).
	b.WriteString(titleStyle.Render("  Серверы") + "\n\n")

	keys := m.keys()

	extra := 0
	if m.filter.shown() {
		b.WriteString("  Фильтр: " + m.filter.input.View() + "\n\n")
		extra = 2
	}

	showPing := m.run.running || len(m.pings) > 0 || len(m.profPings) > 0

	vis := m.visibleRows()
	start, end := 0, len(vis)
	var above, below int
	if len(vis) == 0 {
		b.WriteString(dimStyle.Render("  Ничего не найдено") + "\n")
	} else {
		b.WriteString(tableHead(showPing))
		budget := rowBudget(m.height, m.width, keys, len(vis), extra)
		start, end, above, below = window(len(vis), m.cursor, budget)
	}

	bestSrv, bestProf := bestKey(m.pings), bestKey(m.profPings)

	b.WriteString(moreUp(above))
	for pos := start; pos < end; pos++ {
		r := vis[pos]
		cursor := "  "
		if pos == m.cursor {
			cursor = cursorStyle.Render("▸ ")
		}
		// Task #1: a star beside the cursor says the row it points at is a
		// balancer — the gold alone is a small difference to catch. It goes in the
		// left margin every row already carries, so no column moves when it shows
		// up and the table keeps its full width on a narrow terminal.
		margin := "  "
		if pos == m.cursor && r.kind == rowBalancer {
			margin = goldStyle.Render("★ ")
		}

		line := tableRow(r.name, r.entry, showPing)
		switch r.kind {
		case rowBalancer:
			// Gold marks a balancer — the rows that unfold and run as a whole.
			line = goldStyle.Bold(pos == m.cursor).Render(line)
		case rowGroup:
			line = textStyle.Bold(pos == m.cursor).Render(line)
		default:
			if pos == m.cursor {
				line = selectedStyle.Render(line)
			} else {
				line = textStyle.Render(line)
			}
		}

		// Ping cell from whichever cache owns this row.
		if r.kind == rowServer {
			pr, measured := m.pings[pingKey(r.entry)]
			line += pingCell(pr, measured, m.run.running, measured && pingKey(r.entry) == bestSrv)
			if !supportedProtocols[r.entry.Protocol] {
				line += "  " + warnStyle.Render("⚠ не поддерживается")
			} else if r.entry.Validate() != nil {
				line += "  " + warnStyle.Render("⚠ invalid")
			}
		} else if r.kind == rowBalancer && !m.expanded[r.profIdx] {
			// Task #2: unfolded, the servers below carry the numbers and the balancer
			// row gives its cell up; folded, its own measurement is back. A group has
			// no balancer and so no number of its own — only its servers do.
			key := profileKey(m.profiles[r.profIdx])
			pr, measured := m.profPings[key]
			line += pingCell(pr, measured, m.run.running, measured && key == bestProf)
		}

		indent := strings.Repeat("  ", r.depth)
		b.WriteString(margin + cursor + indent + clip(line, m.width-4-2*r.depth) + "\n")
	}
	b.WriteString(moreDown(below))

	// The notice keeps its one line, empty or not — see the budget. Task #3: it
	// used to be two, and the blank one only pushed the whole screen down.
	switch {
	case m.run.running:
		b.WriteString("  " + benchLine(m.run) + "\n")
	case m.refreshing:
		b.WriteString("  " + dimStyle.Render("⏳ Обновление подписки...") + "\n")
	default:
		b.WriteString("  " + m.status + "\n")
	}

	b.WriteString(legend(m.width, keys))
	return b.String()
}

// bestKey names the lowest successful latency in a cache, or "" when none.
func bestKey(c PingCache) string {
	best := ""
	for k, r := range c {
		if r.Error != nil {
			continue
		}
		if best == "" || r.Latency < c[best].Latency {
			best = k
		}
	}
	return best
}
