package tui

import (
	"fmt"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"xray-runner/internal/system"
)

// The picker for split tunnelling: which running processes go through the VPN
// in proxy mode. It is a checklist over the same machinery the server list uses
// — filter, row window, legend — so the two screens keep behaving alike.

type appRow struct {
	name string
	pids int // 0: listed in apps.txt but not running right now
	on   bool
}

type appsModel struct {
	rows   []appRow
	filter filterState
	cursor int
	saved  bool
	width  int
	height int
}

// SelectApps shows the checklist over the running processes, with the saved
// list pre-ticked. It reports the chosen names and whether the user asked to
// save them — Esc leaves apps.txt alone.
//
// Names in the saved list that are not running are shown too, at the bottom:
// without them there would be no way to untick an app that happens to be closed.
func SelectApps(procs []system.Process, selected []string) ([]string, bool, error) {
	on := map[string]bool{}
	for _, s := range selected {
		on[strings.ToLower(s)] = true
	}

	rows := make([]appRow, 0, len(procs)+len(selected))
	running := map[string]bool{}
	for _, p := range procs {
		running[strings.ToLower(p.Name)] = true
		rows = append(rows, appRow{name: p.Name, pids: p.PIDs, on: on[strings.ToLower(p.Name)]})
	}

	var absent []appRow
	for _, s := range selected {
		if !running[strings.ToLower(s)] {
			absent = append(absent, appRow{name: s, on: true})
		}
	}
	sort.Slice(absent, func(i, j int) bool { return absent[i].name < absent[j].name })

	m := appsModel{rows: append(rows, absent...), filter: newFilter()}
	res, err := runScreen(m)
	if err != nil {
		return selected, false, err
	}
	final := res.(appsModel)
	if !final.saved {
		return selected, false, nil
	}
	return final.chosen(), true, nil
}

// chosen collects the ticked names, ignoring the filter: a query narrows what is
// on screen, never what is saved.
func (m appsModel) chosen() []string {
	var out []string
	for _, r := range m.rows {
		if r.on {
			out = append(out, r.name)
		}
	}
	return out
}

func (m appsModel) Init() tea.Cmd { return nil }

// visible are the rows matching the filter, by process name.
func (m appsModel) visible() []int {
	terms := strings.Fields(strings.ToLower(strings.TrimSpace(m.filter.value())))
	idx := make([]int, 0, len(m.rows))
	for i, r := range m.rows {
		if len(terms) == 0 || matchTerms(strings.ToLower(r.name), terms) {
			idx = append(idx, i)
		}
	}
	return idx
}

func (m appsModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if ws, ok := msg.(tea.WindowSizeMsg); ok {
		m.width, m.height = ws.Width, ws.Height
		return m, nil
	}

	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	if key.Type == tea.KeyCtrlC {
		return m, tea.Quit
	}
	if handled, cmd := m.filter.key(key); handled {
		// Narrowing the list can strand the cursor past its end.
		if n := len(m.visible()); m.cursor >= n {
			m.cursor = max(n-1, 0)
		}
		return m, cmd
	}

	vis := m.visible()
	switch key.String() {
	case "up", "k", "л":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j", "о":
		if m.cursor < len(vis)-1 {
			m.cursor++
		}
	case " ":
		if m.cursor < len(vis) {
			i := vis[m.cursor]
			m.rows[i].on = !m.rows[i].on
		}
	case "/":
		m.filter.start()
	case "enter", "right":
		m.saved = true
		return m, tea.Quit
	case "esc", "left", "q", "й":
		// ← clears the filter first, the same two-step the server list uses.
		if m.filter.clear() {
			m.cursor = 0
			return m, nil
		}
		return m, tea.Quit
	}
	return m, nil
}

const appsKeys = "  ↑/↓ выбор · space отметить · / фильтр · enter сохранить · esc отмена"

func (m appsModel) View() string {
	var b strings.Builder
	b.WriteString(renderLogo(m.width, m.height) + "\n\n")
	b.WriteString(titleStyle.Render("  Процессы через VPN") + "\n\n")

	keys := appsKeys
	if m.filter.typing {
		keys = filterKeys
	}
	extra := 0
	if m.filter.shown() {
		b.WriteString("  Фильтр: " + m.filter.input.View() + "\n\n")
		extra = 2
	}

	b.WriteString(header("  "+pad("", 4)+pad("ПРОЦЕСС", 32)+"PID") + "\n")

	vis := m.visible()
	budget := rowBudget(m.height, m.width, keys, len(vis), extra)
	start, end, above, below := window(len(vis), m.cursor, budget)

	b.WriteString(moreUp(above))
	if len(vis) == 0 {
		b.WriteString("  " + dimStyle.Render("Ничего не найдено") + "\n")
	}
	for pos := start; pos < end; pos++ {
		r := m.rows[vis[pos]]
		mark := "[ ]"
		if r.on {
			mark = "[x]"
		}
		// A process that is not running is kept selectable but visibly inert: the
		// rule for it is simply skipped until it starts.
		count := dimStyle.Render("не запущен")
		if r.pids > 0 {
			count = textStyle.Render(fmt.Sprint(r.pids))
		}

		cursor := "  "
		line := textStyle.Render(pad(mark, 4) + pad(r.name, 32))
		if pos == m.cursor {
			cursor = cursorStyle.Render("▸ ")
			line = selectedStyle.Render(pad(mark, 4) + pad(r.name, 32))
		}
		b.WriteString("  " + cursor + line + count + "\n")
	}
	b.WriteString(moreDown(below))

	n := len(m.chosen())
	switch n {
	case 0:
		b.WriteString("  " + dimStyle.Render("Ничего не выбрано — VPN пойдёт только через системный прокси") + "\n")
	default:
		b.WriteString("  " + textStyle.Render(fmt.Sprintf("Выбрано процессов: %d", n)) + "\n")
	}

	b.WriteString(legend(m.width, keys))
	return b.String()
}
