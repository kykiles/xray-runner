package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
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

var supportedProtocols = map[string]bool{
	"vless":     true,
	"vmess":     true,
	"ss":        true,
	"hysteria2": true,
	"hysteria":  true,
}

// Fixed column widths for the server and profile tables: the last column before
// PING (HOST, BALANCER) is padded only while the PING column is shown, PING fits
// the widest cell ("timeout").
const (
	hostWidth     = 28
	balancerWidth = 20 // "balancer/leastPing" and friends
	pingWidth     = 7
)

type benchResultMsg subscription.BenchmarkResult
type benchDoneMsg struct{}
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
	order      []int                                // display order over entries
	cursor     int                                  // position within visible rows
	filter     textinput.Model
	filtering  bool
	benching   bool
	benchDone  int
	refreshing bool
	status     string
	width      int // terminal width; 0 until the first WindowSizeMsg
	height     int // terminal height; 0 until the first WindowSizeMsg

	action ServerAction
	choice int

	benchCh chan subscription.BenchmarkResult
}

// SelectServer shows the server list and returns the chosen entry, or the
// action that ended the screen (back/quit). title names the profile the servers
// came from; it may be empty. lastAddress/lastPort name the server connected to
// last time, so returning to the list lands the cursor back on it; an empty
// address or no match starts at the top.
func SelectServer(ctx context.Context, title string, entries []subscription.SubEntry, lastAddress string, lastPort int, refresh func() ([]subscription.SubEntry, error), bench BenchmarkFunc) (*subscription.SubEntry, ServerAction, error) {
	fi := textinput.New()
	fi.Placeholder = "поиск по всем столбцам"
	fi.CharLimit = 64
	fi.Width = 40

	m := serversModel{
		ctx:     ctx,
		title:   title,
		entries: entries,
		refresh: refresh,
		bench:   bench,
		results: map[int]subscription.BenchmarkResult{},
		order:   identityOrder(len(entries)),
		cursor:  indexOfServer(entries, lastAddress, lastPort),
		filter:  fi,
		action:  ServerQuit,
		choice:  -1,
	}

	res, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
	if err != nil {
		return nil, ServerQuit, err
	}
	final := res.(serversModel)
	if final.action == ServerSelected && final.choice >= 0 {
		return &final.entries[final.choice], ServerSelected, nil
	}
	return nil, final.action, nil
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

func identityOrder(n int) []int {
	order := make([]int, n)
	for i := range order {
		order[i] = i
	}
	return order
}

func (m serversModel) Init() tea.Cmd { return nil }

// entryHaystack is the entry flattened to one lowercase string spanning every
// column, so the filter can match a substring anywhere: name, host, port,
// protocol or transport.
func entryHaystack(e subscription.SubEntry) string {
	return strings.ToLower(fmt.Sprintf("%s %s %d %s %s",
		e.Remarks, e.Address, e.Port, e.Protocol, e.Network))
}

// matchAll reports whether every space-separated term appears somewhere in the
// entry. Terms narrow the list (AND); each is a plain substring — typing "vless"
// finds the protocol column, "ws" the transport, part of a name the name column.
func matchAll(e subscription.SubEntry, terms []string) bool {
	hay := entryHaystack(e)
	for _, t := range terms {
		if !strings.Contains(hay, t) {
			return false
		}
	}
	return true
}

// visible returns entry indices matching the filter, in display order.
func (m serversModel) visible() []int {
	terms := strings.Fields(strings.ToLower(strings.TrimSpace(m.filter.Value())))
	if len(terms) == 0 {
		return m.order
	}
	var out []int
	for _, idx := range m.order {
		if matchAll(m.entries[idx], terms) {
			out = append(out, idx)
		}
	}
	return out
}

func (m serversModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil
	case benchResultMsg:
		m.results[msg.Index] = subscription.BenchmarkResult(msg)
		m.benchDone++
		return m, m.waitBenchResult()
	case benchDoneMsg:
		m.benching = false
		m.sortByLatency()
		m.status = okStyle.Render("Пинг завершён")
		return m, nil
	case refreshDoneMsg:
		m.refreshing = false
		if msg.err != nil {
			m.status = errStyle.Render(fmt.Sprintf("Ошибка обновления: %v", msg.err))
			return m, nil
		}
		m.entries = msg.entries
		m.order = identityOrder(len(m.entries))
		m.results = map[int]subscription.BenchmarkResult{}
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

	if m.filtering {
		switch key.Type {
		case tea.KeyEsc:
			m.filtering = false
			m.filter.SetValue("")
			m.filter.Blur()
			return m, nil
		case tea.KeyEnter:
			m.filtering = false
			m.filter.Blur()
			return m, nil
		}
		var cmd tea.Cmd
		m.filter, cmd = m.filter.Update(key)
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
		if m.filter.Value() != "" {
			m.filter.SetValue("")
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
		m.filtering = true
		m.filter.Focus()
		m.status = ""
	case "b", "и":
		if m.benching || m.bench == nil {
			return m, nil
		}
		m.benching = true
		m.benchDone = 0
		m.status = ""
		return m, m.startBenchmark()
	case "r", "к":
		// H-1: refreshing mid-benchmark swaps the entry list out from under the
		// running measurement, and results streaming in with the old indices then
		// land on whatever server now sits at that position.
		if m.refresh == nil || m.refreshing || m.benching {
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

func (m *serversModel) clampCursor() {
	if n := len(m.visible()); m.cursor >= n {
		m.cursor = max(0, n-1)
	}
}

// startBenchmark launches the measurement goroutine; results stream back into
// Update through benchCh so rows update live (WS-8).
func (m *serversModel) startBenchmark() tea.Cmd {
	ch := make(chan subscription.BenchmarkResult, len(m.entries)+1)
	m.benchCh = ch
	entries := m.entries
	bench := m.bench
	ctx := m.ctx
	go func() {
		bench(ctx, entries, func(r subscription.BenchmarkResult) {
			ch <- r
		})
		close(ch)
	}()
	return m.waitBenchResult()
}

func (m serversModel) waitBenchResult() tea.Cmd {
	ch := m.benchCh
	return func() tea.Msg {
		r, ok := <-ch
		if !ok {
			return benchDoneMsg{}
		}
		return benchResultMsg(r)
	}
}

func (m serversModel) startRefresh() tea.Cmd {
	refresh := m.refresh
	return func() tea.Msg {
		entries, err := refresh()
		return refreshDoneMsg{entries: entries, err: err}
	}
}

// sortByLatency reorders rows: measured servers ascending, then unmeasured/
// failed ones in original order. The cursor follows its entry.
func (m *serversModel) sortByLatency() {
	current := -1
	if vis := m.visible(); len(vis) > 0 && m.cursor < len(vis) {
		current = vis[m.cursor]
	}

	order := identityOrder(len(m.entries))
	sort.SliceStable(order, func(a, b int) bool {
		ra, okA := m.results[order[a]]
		rb, okB := m.results[order[b]]
		goodA := okA && ra.Error == nil
		goodB := okB && rb.Error == nil
		if goodA != goodB {
			return goodA
		}
		if !goodA {
			return false
		}
		return ra.Latency < rb.Latency
	})
	m.order = order

	if current >= 0 {
		for pos, idx := range m.visible() {
			if idx == current {
				m.cursor = pos
				break
			}
		}
	}
	m.clampCursor()
}

func (m serversModel) View() string {
	var b strings.Builder
	title := "Серверы"
	if m.title != "" {
		title = m.title + " · серверы"
	}
	b.WriteString(titleStyle.Render(title) + "\n\n")

	if m.filtering || m.filter.Value() != "" {
		b.WriteString("  Фильтр: " + m.filter.View() + "\n\n")
	}

	keys := "  ↑/↓ выбор · → подключить · / фильтр · ← назад · q выход"
	if m.bench != nil {
		keys = "  ↑/↓ выбор · → подключить · b пинг · r обновить · / фильтр · ← назад · q выход"
	}
	if m.filtering {
		keys = "  фильтр: поиск по всем столбцам · enter применить · esc сбросить"
	}

	// The PING column appears only once a measurement is running or done, so the
	// list stays narrow until there is anything to show there.
	showPing := m.benching || len(m.results) > 0

	vis := m.visible()
	start, end := 0, len(vis)
	var above, below int
	if len(vis) == 0 {
		b.WriteString(dimStyle.Render("  Ничего не найдено") + "\n")
	} else {
		head := fmt.Sprintf("  %s %s %s %s",
			pad("NAME", 26), pad("PROTOCOL", 9), pad("TRANSPORT", 9), pad("HOST", hostWidth))
		if showPing {
			head += "  " + padLeft("PING", pingWidth)
		}
		b.WriteString("  " + header(head))

		// Fit the row list into the terminal, reserving the fixed chrome so the
		// legend never gets pushed off the bottom (task #3): title(2) + header(1)
		// + legend + two indicator lines + the status block, plus the filter when
		// shown. The status block is reserved even while empty, so starting a ping
		// does not shrink the list out from under the cursor (task #4).
		budget := len(vis)
		if m.height > 0 {
			reserved := 2 + 1 + legendHeight(m.width, keys) + 2 + 2
			if m.filtering || m.filter.Value() != "" {
				reserved += 2
			}
			if budget = m.height - reserved; budget < 1 {
				budget = 1
			}
		}
		start, end, above, below = window(len(vis), m.cursor, budget)
	}

	b.WriteString(moreUp(above))
	for pos := start; pos < end; pos++ {
		idx := vis[pos]
		e := m.entries[idx]
		cursor := "  "
		if pos == m.cursor {
			cursor = cursorStyle.Render("▸ ")
		}

		name := flagSpace(orDash(mark(e)))
		hostPort := fmt.Sprintf("%s:%d", e.Address, e.Port)
		host := truncate(hostPort, hostWidth)
		if showPing {
			// Pad HOST to a fixed width so the PING numbers behind it share one
			// right-aligned column instead of drifting with the host length.
			host = pad(host, hostWidth)
		}
		line := fmt.Sprintf("%s %s %s %s",
			pad(truncate(name, 26), 26),
			pad(e.Protocol, 9),
			pad(orDash(e.Network), 9),
			host)
		if pos == m.cursor {
			line = selectedStyle.Render(line)
		}

		if r, ok := m.results[idx]; ok {
			mark := okStyle
			if r.Error != nil {
				mark = errStyle
			}
			line += "  " + mark.Render(padLeft(r.String(), pingWidth))
		} else if m.benching {
			line += "  " + dimStyle.Render(padLeft("...", pingWidth))
		}

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
	case m.benching:
		b.WriteString("\n  " + dimStyle.Render(fmt.Sprintf("⏳ Замер latency... %d/%d", m.benchDone, len(m.entries))) + "\n")
	case m.refreshing:
		b.WriteString("\n  " + dimStyle.Render("⏳ Обновление подписки...") + "\n")
	default:
		b.WriteString("\n  " + m.status + "\n")
	}

	b.WriteString(legend(m.width, keys))
	return b.String()
}

// orDash keeps empty table cells visible as a placeholder instead of a hole.
func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
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
