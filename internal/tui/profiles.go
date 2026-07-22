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

type profilesModel struct {
	ctx      context.Context
	profiles []subscription.Profile
	bench    ProfileBenchmarkFunc
	cursor   int
	action   ProfileAction
	choice   int

	results   map[int]subscription.BenchmarkResult // key: profile index
	benching  bool
	benchDone int
	status    string
	width     int // terminal width; 0 until the first WindowSizeMsg
	height    int // terminal height; 0 until the first WindowSizeMsg
	benchCh   chan subscription.BenchmarkResult
}

// SelectProfile shows the profiles of a JSON subscription. It returns the chosen
// profile index together with what the user wants done with it.
func SelectProfile(ctx context.Context, profiles []subscription.Profile, bench ProfileBenchmarkFunc) (int, ProfileAction, error) {
	m := profilesModel{
		ctx:      ctx,
		profiles: profiles,
		bench:    bench,
		action:   ProfileQuit,
		choice:   -1,
		results:  map[int]subscription.BenchmarkResult{},
	}

	res, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
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
		m.results[msg.Index] = subscription.BenchmarkResult(msg)
		m.benchDone++
		return m, waitBench(m.benchCh)
	case benchDoneMsg:
		m.benching = false
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

	// Cyrillic twins mirror the Russian layout (task #7): q→й, b→и, j→о, k→л, l→д.
	switch key.String() {
	case "q", "й":
		m.action = ProfileQuit
		return m, tea.Quit
	case "esc", "left":
		m.action = ProfileBack
		return m, tea.Quit
	case "up", "k", "л":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j", "о":
		if m.cursor < len(m.profiles)-1 {
			m.cursor++
		}
	case "b", "и":
		if m.benching || m.bench == nil || len(m.profiles) == 0 {
			return m, nil
		}
		m.benching = true
		m.benchDone = 0
		m.status = ""
		profiles, bench, ctx := m.profiles, m.bench, m.ctx
		var cmd tea.Cmd
		m.benchCh, cmd = startBench(len(profiles), func(on func(subscription.BenchmarkResult)) {
			bench(ctx, profiles, on)
		})
		return m, cmd
	case "s", "ы":
		// Peek inside a balancer: see, ping and pick its servers one by one. A
		// single-server profile has nothing to unfold.
		if len(m.profiles) == 0 || m.profiles[m.cursor].Balancer == nil {
			return m, nil
		}
		m.action = ProfileExpand
		m.choice = m.cursor
		return m, tea.Quit
	case "enter", "right", "l", "д":
		if len(m.profiles) == 0 {
			return m, nil
		}
		m.choice = m.cursor
		// A single-server profile has nothing to balance: go straight to its
		// server rather than launching a pointless one-outbound config.
		if m.profiles[m.cursor].Balancer == nil {
			m.action = ProfileExpand
		} else {
			m.action = ProfileRun
		}
		return m, tea.Quit
	}
	return m, nil
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
	keys := "  ↑/↓ выбор · → подключить"
	if m.cursor < len(m.profiles) && m.profiles[m.cursor].Balancer != nil {
		keys += " · s серверы"
	}
	if m.bench != nil {
		keys += " · b пинг"
	}
	return keys + " · ← назад · q выход"
}

func (m profilesModel) View() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("Серверы") + "\n\n")

	// The PING column appears only once a measurement is running or done, so the
	// list stays narrow until there is anything to show there.
	showPing := m.benching || len(m.results) > 0

	b.WriteString(tableHead(showPing))

	keys := m.keys()

	budget := rowBudget(m.height, m.width, keys, len(m.profiles), 0)
	start, end, above, below := window(len(m.profiles), m.cursor, budget)
	best := bestResult(m.results)

	b.WriteString(moreUp(above))
	for i := start; i < end; i++ {
		p := m.profiles[i]
		cursor := "  "
		if i == m.cursor {
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
			line = goldStyle.Bold(i == m.cursor).Render(line)
		case i == m.cursor:
			line = selectedStyle.Render(line)
		}

		r, measured := m.results[i]
		line += pingCell(r, measured, m.benching, i == best)
		b.WriteString("  " + cursor + clip(line, m.width-4) + "\n")
	}
	b.WriteString(moreDown(below))

	// The block always occupies its two lines, empty or not — see the budget above.
	if m.benching {
		b.WriteString("\n  " + dimStyle.Render(fmt.Sprintf("⏳ Замер latency... %d/%d", m.benchDone, len(m.profiles))) + "\n")
	} else {
		b.WriteString("\n  " + m.status + "\n")
	}

	b.WriteString(legend(m.width, keys))
	return b.String()
}
