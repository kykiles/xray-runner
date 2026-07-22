package tui

import (
	"context"
	"fmt"
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"xray-runner/internal/subscription"
)

// ServerAction is the outcome of the server screen.
type ServerAction int

const (
	ServerSelected ServerAction = iota
	ServerBack                  // back to the subscription list
	ServerQuit
)

// BenchmarkFunc mirrors the app benchmarker: onResult fires per finished entry.
type BenchmarkFunc func(ctx context.Context, entries []subscription.SubEntry, onResult func(subscription.BenchmarkResult)) []subscription.BenchmarkResult

// ServerConfigFunc renders the full xray config a session with this server
// would run — the panel's dns and routing included, not just the outbound.
type ServerConfigFunc func(e *subscription.SubEntry) (string, error)

// SaveConfigFunc writes a shown config to disk under the given name and returns
// the path it landed at.
type SaveConfigFunc func(name, text string) (string, error)

// PingCache holds the last measurement of every server pinged this run, so
// returning to the list after a connection still shows what was measured
// instead of an empty column — a ping run is slow, and the numbers are only
// there to say roughly where each server stands. It is keyed by endpoint, not
// by position: a refresh or another profile shifts the indices, the endpoint
// stays. `r` on the server screen drops it — that is the way to force a fresh
// measurement.
type PingCache map[string]subscription.BenchmarkResult

func pingKey(e subscription.SubEntry) string {
	return fmt.Sprintf("%s:%d", e.Address, e.Port)
}

// restore maps the cache onto the entry list currently on screen.
func (c PingCache) restore(entries []subscription.SubEntry) map[int]subscription.BenchmarkResult {
	out := make(map[int]subscription.BenchmarkResult, len(c))
	for i, e := range entries {
		if r, ok := c[pingKey(e)]; ok {
			r.Index = i
			out[i] = r
		}
	}
	return out
}

var supportedProtocols = map[string]bool{
	"vless":     true,
	"vmess":     true,
	"ss":        true,
	"trojan":    true,
	"hysteria2": true,
	"hysteria":  true,
}

type benchResultMsg struct {
	gen int
	subscription.BenchmarkResult
}
type benchDoneMsg struct{ gen int }
type refreshDoneMsg struct {
	entries []subscription.SubEntry
	err     error
}

type serversModel struct {
	ctx     context.Context
	title   string // profile name, empty for a flat subscription
	entries []subscription.SubEntry
	refresh func() ([]subscription.SubEntry, error)
	bench   BenchmarkFunc

	results    map[int]subscription.BenchmarkResult // key: entry index
	pings      PingCache                            // the same results, kept across screens
	cursor     int                                  // position within visible rows
	filter     filterState
	run        benchState
	refreshing bool
	status     string
	width      int // terminal width; 0 until the first WindowSizeMsg
	height     int // terminal height; 0 until the first WindowSizeMsg

	// cfg shows the full session config of the server under the cursor; while
	// open it takes over the screen.
	cfg     cfgView
	preview ServerConfigFunc
	saveCfg SaveConfigFunc

	action ServerAction
	choice int
}

// SelectServer shows the server list and returns the chosen entry, or the
// action that ended the screen (back/quit). title names the profile the servers
// came from; it may be empty. lastAddress/lastPort name the server connected to
// last time, so returning to the list lands the cursor back on it; an empty
// address or no match starts at the top. filter is the search the screen was
// left with, restored on the way back in and handed out again on exit. pings
// carries the measurements across screens; the screen writes into it as results
// arrive, so nothing is lost on any exit path.
func SelectServer(ctx context.Context, title string, entries []subscription.SubEntry, lastAddress string, lastPort int, filter string, pings PingCache, refresh func() ([]subscription.SubEntry, error), bench BenchmarkFunc, preview ServerConfigFunc, save SaveConfigFunc) (*subscription.SubEntry, ServerAction, string, error) {
	// Leaving the screen ends its benchmark: the measurement runs in a goroutine
	// nobody waits for, and its results land in a model that no longer exists.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	fi := newFilter()
	fi.input.SetValue(filter)

	if pings == nil {
		pings = PingCache{}
	}

	m := serversModel{
		ctx:     ctx,
		title:   title,
		entries: entries,
		refresh: refresh,
		bench:   bench,
		preview: preview,
		saveCfg: save,
		results: pings.restore(entries),
		pings:   pings,
		filter:  fi,
		action:  ServerQuit,
		choice:  -1,
	}
	m.cursor = m.cursorAt(lastAddress, lastPort)

	res, err := runScreen(m)
	if err != nil {
		return nil, ServerQuit, "", err
	}
	final := res.(serversModel)
	if final.action == ServerSelected && final.choice >= 0 {
		return &final.entries[final.choice], ServerSelected, final.filter.value(), nil
	}
	return nil, final.action, final.filter.value(), nil
}

// indexOfServer finds the entry matching address:port, or 0 when there is no
// match — address:port identifies a server across a subscription refresh, where
// names may change and positions shift.
func indexOfServer(entries []subscription.SubEntry, address string, port int) int {
	if address == "" {
		return 0
	}
	for i, e := range entries {
		if e.Address == address && e.Port == port {
			return i
		}
	}
	return 0
}

// cursorAt is where the cursor starts: the row of the last connected server.
// The cursor counts visible rows, so with a filter restored the server has to be
// looked up among those, not among all entries.
func (m serversModel) cursorAt(address string, port int) int {
	return max(0, slices.Index(m.visible(), indexOfServer(m.entries, address, port)))
}

func (m serversModel) Init() tea.Cmd { return nil }

// entryHaystack is the entry flattened to one lowercase string spanning every
// column, so the filter can match a substring anywhere: name, host, port,
// protocol or transport.
func entryHaystack(e subscription.SubEntry) string {
	return strings.ToLower(fmt.Sprintf("%s %s %d %s %s",
		e.Remarks, e.Address, e.Port, e.Protocol, e.Network))
}

// visible returns entry indices matching the filter. The subscription's own
// order is kept as-is — it is the order the panel meant, and a ping run must
// not shuffle it (task #2); the fastest server is called out by color instead.
func (m serversModel) visible() []int {
	return m.filter.visible(len(m.entries), func(i int) string { return entryHaystack(m.entries[i]) })
}

func (m serversModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
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
		m.pings[pingKey(m.entries[msg.Index])] = msg.BenchmarkResult
		m.run.done++
		return m, waitBench(m.run.ch, msg.gen)
	case benchDoneMsg:
		if !m.run.accept(msg.gen) {
			return m, nil
		}
		m.run.stop()
		m.status = okStyle.Render("Пинг завершён")
		return m, nil
	case refreshDoneMsg:
		m.refreshing = false
		if msg.err != nil {
			m.status = errStyle.Render(fmt.Sprintf("Ошибка обновления: %v", msg.err))
			return m, nil
		}
		m.entries = msg.entries
		m.results = map[int]subscription.BenchmarkResult{}
		clear(m.pings) // the list is new; keeping old numbers would outlive their servers
		m.cursor = 0
		m.status = okStyle.Render(fmt.Sprintf("Подписка обновлена: %d серверов", len(m.entries)))
		return m, nil
	case tea.KeyMsg:
		return m.updateKey(msg)
	}
	return m, nil
}

func (m serversModel) updateKey(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	// U-1: Ctrl+C always quits.
	if key.Type == tea.KeyCtrlC {
		m.action = ServerQuit
		return m, tea.Quit
	}

	// The config viewer takes over the screen: it only scrolls and closes.
	if m.cfg.open() {
		if m.cfg.key(key.String(), m.height, m.width) {
			m.action = ServerQuit
			return m, tea.Quit
		}
		return m, nil
	}

	if handled, cmd := m.filter.key(key); handled {
		m.clampCursor()
		return m, cmd
	}

	// Letter hotkeys also accept their Cyrillic twin on a Russian layout, where
	// the same physical key emits й/и/к/о/л/а instead of q/b/r/j/k/f.
	switch key.String() {
	case "q", "й":
		m.action = ServerQuit
		return m, tea.Quit
	case "esc", "left":
		if m.filter.clear() {
			m.clampCursor()
			return m, nil
		}
		m.action = ServerBack
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
	case "c", "с":
		m.showConfig()
	case "b", "и":
		if m.bench == nil {
			return m, nil
		}
		// Ping what is on screen: with a filter applied the rest is not the list
		// the user is looking at. Pressing b again restarts the measurement on the
		// current selection — the old run is cancelled, not waited out.
		vis := m.visible()
		if len(vis) == 0 {
			return m, nil
		}
		entries := make([]subscription.SubEntry, len(vis))
		for i, idx := range vis {
			entries[i] = m.entries[idx]
		}
		m.status = ""
		bench := m.bench
		return m, m.run.start(m.ctx, len(entries), func(ctx context.Context, on func(subscription.BenchmarkResult)) {
			// The benchmark indexes the slice it got; map back to entry indices,
			// which is what m.results is keyed by.
			bench(ctx, entries, func(r subscription.BenchmarkResult) {
				r.Index = vis[r.Index]
				on(r)
			})
		})
	case "r", "к":
		// H-1: refreshing mid-benchmark swaps the entry list out from under the
		// running measurement, and results streaming in with the old indices then
		// land on whatever server now sits at that position.
		if m.refresh == nil || m.refreshing || m.run.running {
			return m, nil
		}
		m.refreshing = true
		m.status = ""
		return m, m.startRefresh()
	case "enter", "right":
		// → connects, mirroring "into/forward" used across the menus (task #1).
		vis := m.visible()
		if len(vis) == 0 {
			return m, nil
		}
		idx := vis[m.cursor]
		e := m.entries[idx]
		if !supportedProtocols[e.Protocol] {
			m.status = warnStyle.Render(fmt.Sprintf("Протокол %s не поддерживается — выберите другой", e.Protocol))
			return m, nil
		}
		if err := e.Validate(); err != nil {
			m.status = warnStyle.Render(fmt.Sprintf("Запись невалидна: %v — выберите другую", err))
			return m, nil
		}
		m.action = ServerSelected
		m.choice = idx
		return m, tea.Quit
	}
	return m, nil
}

// showConfig opens the viewer on the full config a session with the server
// under the cursor would launch — the panel's dns and routing rules included,
// so what is on screen is what actually runs.
func (m *serversModel) showConfig() {
	vis := m.visible()
	if len(vis) == 0 || m.preview == nil {
		return
	}
	e := m.entries[vis[m.cursor]]
	text, err := m.preview(&e)
	if err != nil {
		m.status = errStyle.Render(fmt.Sprintf("Конфиг недоступен: %v", err))
		return
	}
	title := orDash(mark(e))
	if title == "-" {
		title = e.Address
	}
	m.cfg.show(title, text, saver(m.saveCfg, title, text))
}

func (m *serversModel) clampCursor() {
	if n := len(m.visible()); m.cursor >= n {
		m.cursor = max(0, n-1)
	}
}

func (m serversModel) startRefresh() tea.Cmd {
	refresh := m.refresh
	return func() tea.Msg {
		entries, err := refresh()
		return refreshDoneMsg{entries: entries, err: err}
	}
}

func (m serversModel) View() string {
	if m.cfg.open() {
		return m.cfg.view(m.width, m.height)
	}
	var b strings.Builder
	title := "Серверы"
	if m.title != "" {
		title = m.title + " · серверы"
	}
	b.WriteString(titleStyle.Render(title) + "\n\n")

	if m.filter.shown() {
		b.WriteString("  Фильтр: " + m.filter.input.View() + "\n\n")
	}

	keys := "  ↑/↓ выбор · → подключить · c конфиг · / фильтр · ← назад · q выход"
	if m.bench != nil {
		keys = "  ↑/↓ выбор · → подключить · c конфиг · b пинг · r обновить · / фильтр · ← назад · q выход"
	}
	if m.filter.typing {
		keys = filterKeys
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

		extra := 0
		if m.filter.shown() {
			extra = 2
		}
		budget := rowBudget(m.height, m.width, keys, len(vis), extra)
		start, end, above, below = window(len(vis), m.cursor, budget)
	}

	best := bestResult(m.results)

	b.WriteString(moreUp(above))
	for pos := start; pos < end; pos++ {
		idx := vis[pos]
		e := m.entries[idx]
		cursor := "  "
		if pos == m.cursor {
			cursor = cursorStyle.Render("▸ ")
		}

		line := tableRow(orDash(mark(e)), e, showPing)
		if pos == m.cursor {
			line = selectedStyle.Render(line)
		}

		r, measured := m.results[idx]
		line += pingCell(r, measured, m.run.running, idx == best)

		if !supportedProtocols[e.Protocol] {
			line += "  " + warnStyle.Render("⚠ не поддерживается")
		} else if e.Validate() != nil {
			line += "  " + warnStyle.Render("⚠ invalid")
		}
		// Keep the row on one line even with a long name + latency tail (task #2):
		// clip to the terminal width, less the 4-column left gutter.
		b.WriteString("  " + cursor + clip(line, m.width-4) + "\n")
	}
	b.WriteString(moreDown(below))

	// The block always occupies its two lines, empty or not — see the budget above.
	switch {
	case m.run.running:
		b.WriteString("\n  " + benchLine(m.run) + "\n")
	case m.refreshing:
		b.WriteString("\n  " + dimStyle.Render("⏳ Обновление подписки...") + "\n")
	default:
		b.WriteString("\n  " + m.status + "\n")
	}

	b.WriteString(legend(m.width, keys))
	return b.String()
}

// mark is the server's name from the subscription. Xray-config subscriptions
// carry no per-server name, so the parser falls back to the address — repeating
// it in the NAME column would just duplicate HOST.
func mark(e subscription.SubEntry) string {
	if e.Remarks == e.Address {
		return ""
	}
	return e.Remarks
}
