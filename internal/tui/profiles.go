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
		return m, m.waitBenchResult()
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
		return m, m.startBenchmark()
	case "right", "l", "д":
		if len(m.profiles) == 0 {
			return m, nil
		}
		m.action = ProfileExpand
		m.choice = m.cursor
		return m, tea.Quit
	case "enter":
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

// startBenchmark launches the measurement goroutine; results stream back into
// Update through benchCh so rows update live.
func (m *profilesModel) startBenchmark() tea.Cmd {
	ch := make(chan subscription.BenchmarkResult, len(m.profiles)+1)
	m.benchCh = ch
	profiles := m.profiles
	bench := m.bench
	ctx := m.ctx
	go func() {
		bench(ctx, profiles, func(r subscription.BenchmarkResult) {
			ch <- r
		})
		close(ch)
	}()
	return m.waitBenchResult()
}

func (m profilesModel) waitBenchResult() tea.Cmd {
	ch := m.benchCh
	return func() tea.Msg {
		r, ok := <-ch
		if !ok {
			return benchDoneMsg{}
		}
		return benchResultMsg(r)
	}
}

func (m profilesModel) View() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("Серверы") + "\n\n")

	b.WriteString("  " + header(fmt.Sprintf("  %s %s %s",
		pad("NAME", 30), pad("SERVERS", 8), "BALANCER")))

	keys := "  ↑/↓ выбор · enter подключиться · → раскрыть серверы · ← назад · q выход"
	if m.bench != nil {
		keys = "  ↑/↓ выбор · enter подключиться · → раскрыть серверы · b пинг · ← назад · q выход"
	}

	// Fit the row list into the terminal, reserving the fixed chrome (task #3):
	// title(2) + header(1) + legend + two indicator lines + the status block. The
	// status block is reserved even while empty, so starting a ping does not
	// shrink the list out from under the cursor (task #4).
	budget := len(m.profiles)
	if m.height > 0 {
		reserved := 2 + 1 + legendHeight(m.width, keys) + 2 + 2
		if budget = m.height - reserved; budget < 1 {
			budget = 1
		}
	}
	start, end, above, below := window(len(m.profiles), m.cursor, budget)

	b.WriteString(moreUp(above))
	for i := start; i < end; i++ {
		p := m.profiles[i]
		cursor := "  "
		if i == m.cursor {
			cursor = cursorStyle.Render("▸ ")
		}

		name := flagSpace(p.Name)
		if name == "" {
			name = "(без имени)"
		}
		line := fmt.Sprintf("%s %s %s",
			pad(truncate(name, 30), 30),
			pad(fmt.Sprintf("%d", len(p.Entries)), 8),
			p.Mode())
		if i == m.cursor {
			line = selectedStyle.Render(line)
		}

		if r, ok := m.results[i]; ok {
			style := okStyle
			if r.Error != nil {
				style = errStyle
			}
			line += "  " + style.Render("▸ "+r.String())
		} else if m.benching {
			line += "  " + dimStyle.Render("▸ ...")
		}
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
