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

type benchResultMsg subscription.BenchmarkResult
type benchDoneMsg struct{}
type refreshDoneMsg struct {
	entries []subscription.SubEntry
	err     error
}

type serversModel struct {
	ctx     context.Context
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

	action ServerAction
	choice int

	benchCh chan subscription.BenchmarkResult
}

// SelectServer shows the server list and returns the chosen entry, or the
// action that ended the screen (back/quit).
func SelectServer(ctx context.Context, entries []subscription.SubEntry, refresh func() ([]subscription.SubEntry, error), bench BenchmarkFunc) (*subscription.SubEntry, ServerAction, error) {
	fi := textinput.New()
	fi.Placeholder = "имя или хост"
	fi.CharLimit = 64
	fi.Width = 40

	m := serversModel{
		ctx:     ctx,
		entries: entries,
		refresh: refresh,
		bench:   bench,
		results: map[int]subscription.BenchmarkResult{},
		order:   identityOrder(len(entries)),
		filter:  fi,
		action:  ServerQuit,
		choice:  -1,
	}

	res, err := tea.NewProgram(m).Run()
	if err != nil {
		return nil, ServerQuit, err
	}
	final := res.(serversModel)
	if final.action == ServerSelected && final.choice >= 0 {
		return &final.entries[final.choice], ServerSelected, nil
	}
	return nil, final.action, nil
}

func identityOrder(n int) []int {
	order := make([]int, n)
	for i := range order {
		order[i] = i
	}
	return order
}

func (m serversModel) Init() tea.Cmd { return nil }

// visible returns entry indices matching the filter, in display order.
func (m serversModel) visible() []int {
	q := strings.ToLower(strings.TrimSpace(m.filter.Value()))
	if q == "" {
		return m.order
	}
	var out []int
	for _, idx := range m.order {
		e := m.entries[idx]
		if strings.Contains(strings.ToLower(e.Remarks), q) || strings.Contains(strings.ToLower(e.Address), q) {
			out = append(out, idx)
		}
	}
	return out
}

func (m serversModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case benchResultMsg:
		m.results[msg.Index] = subscription.BenchmarkResult(msg)
		m.benchDone++
		return m, m.waitBenchResult()
	case benchDoneMsg:
		m.benching = false
		m.sortByLatency()
		m.status = okStyle.Render("Бенчмарк завершён")
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

	switch key.String() {
	case "q":
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
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < len(m.visible())-1 {
			m.cursor++
		}
	case "/", "f":
		m.filtering = true
		m.filter.Focus()
		m.status = ""
	case "b":
		if m.benching || m.bench == nil {
			return m, nil
		}
		m.benching = true
		m.benchDone = 0
		m.status = ""
		return m, m.startBenchmark()
	case "r":
		if m.refresh == nil || m.refreshing {
			return m, nil
		}
		m.refreshing = true
		m.status = ""
		return m, m.startRefresh()
	case "enter":
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
	b.WriteString(titleStyle.Render("── Серверы") + "\n\n")

	if m.filtering || m.filter.Value() != "" {
		b.WriteString("  Фильтр: " + m.filter.View() + "\n\n")
	}

	vis := m.visible()
	if len(vis) == 0 {
		b.WriteString(dimStyle.Render("  Ничего не найдено") + "\n")
	}
	for pos, idx := range vis {
		e := m.entries[idx]
		cursor := "  "
		if pos == m.cursor {
			cursor = cursorStyle.Render("▸ ")
		}

		hostPort := fmt.Sprintf("%s:%d", e.Address, e.Port)
		remark := e.Remarks
		if remark != "" {
			remark = " [" + remark + "]"
		}
		line := fmt.Sprintf("%-28s %-9s %-4s%s", hostPort, e.Protocol, e.Network, remark)
		if pos == m.cursor {
			line = selectedStyle.Render(line)
		}

		if r, ok := m.results[idx]; ok {
			mark := okStyle
			if r.Error != nil {
				mark = errStyle
			}
			line += "  " + mark.Render("▸ "+r.String())
		} else if m.benching {
			line += "  " + dimStyle.Render("▸ ...")
		}

		if !supportedProtocols[e.Protocol] {
			line += "  " + warnStyle.Render("⚠ не поддерживается")
		} else if e.Validate() != nil {
			line += "  " + warnStyle.Render("⚠ invalid")
		}
		b.WriteString("  " + cursor + line + "\n")
	}

	if m.benching {
		b.WriteString("\n  " + dimStyle.Render(fmt.Sprintf("⏳ Замер latency... %d/%d", m.benchDone, len(m.entries))) + "\n")
	} else if m.refreshing {
		b.WriteString("\n  " + dimStyle.Render("⏳ Обновление подписки...") + "\n")
	} else if m.status != "" {
		b.WriteString("\n  " + m.status + "\n")
	}

	keys := "  ↑/↓ выбор · enter подключить · / фильтр · esc назад · q выход"
	if m.bench != nil {
		keys = "  ↑/↓ выбор · enter подключить · b бенчмарк · r обновить · / фильтр · esc назад · q выход"
	}
	if m.filtering {
		keys = "  ввод — фильтр по имени/хосту · enter применить · esc сбросить"
	}
	b.WriteString(legend(keys))
	return b.String()
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
