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

	switch key.String() {
	case "q":
		m.action = ProfileQuit
		return m, tea.Quit
	case "esc", "left":
		m.action = ProfileBack
		return m, tea.Quit
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < len(m.profiles)-1 {
			m.cursor++
		}
	case "b":
		if m.benching || m.bench == nil || len(m.profiles) == 0 {
			return m, nil
		}
		m.benching = true
		m.benchDone = 0
		m.status = ""
		return m, m.startBenchmark()
	case "right", "l":
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
	b.WriteString(titleStyle.Render("── Серверы") + "\n\n")

	b.WriteString("  " + header(fmt.Sprintf("  %s %s %s",
		pad("ПРОФИЛЬ", 30), pad("СЕРВЕРОВ", 8), "РЕЖИМ")))

	for i, p := range m.profiles {
		cursor := "  "
		if i == m.cursor {
			cursor = cursorStyle.Render("▸ ")
		}

		name := p.Name
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
		b.WriteString("  " + cursor + line + "\n")
	}

	if m.benching {
		b.WriteString("\n  " + dimStyle.Render(fmt.Sprintf("⏳ Замер latency... %d/%d", m.benchDone, len(m.profiles))) + "\n")
	} else if m.status != "" {
		b.WriteString("\n  " + m.status + "\n")
	}

	keys := "  ↑/↓ выбор · enter запустить профиль · → раскрыть серверы · esc назад · q выход"
	if m.bench != nil {
		keys = "  ↑/↓ выбор · enter запустить профиль · → раскрыть серверы · b пинг · esc назад · q выход"
	}
	b.WriteString(legend(keys))
	return b.String()
}
