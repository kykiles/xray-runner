package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"xray-runner/internal/subscription"
)

// ProfileAction is the outcome of the profile screen.
type ProfileAction int

const (
	ProfileRun    ProfileAction = iota // launch the profile as-is (balancer + its routing)
	ProfileExpand                      // drill down into the profile's servers
	ProfileBack                        // back to the subscription list
	ProfileQuit
)

type profilesModel struct {
	profiles []subscription.Profile
	cursor   int
	action   ProfileAction
	choice   int
}

// SelectProfile shows the profiles of a JSON subscription. It returns the chosen
// profile index together with what the user wants done with it.
func SelectProfile(profiles []subscription.Profile) (int, ProfileAction, error) {
	m := profilesModel{profiles: profiles, action: ProfileQuit, choice: -1}

	res, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
	if err != nil {
		return -1, ProfileQuit, err
	}
	final := res.(profilesModel)
	return final.choice, final.action, nil
}

func (m profilesModel) Init() tea.Cmd { return nil }

func (m profilesModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}

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

func (m profilesModel) View() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("── Мои серверы") + "\n\n")

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
		b.WriteString("  " + cursor + line + "\n")
	}

	b.WriteString(legend("  ↑/↓ выбор · enter запустить профиль · → раскрыть серверы · esc назад · q выход"))
	return b.String()
}
