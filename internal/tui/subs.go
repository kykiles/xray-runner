package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"xray-runner/internal/subscription"
)

// SubsAction is the outcome of the subscription screen.
type SubsAction int

const (
	SubsSelected SubsAction = iota
	SubsUpdate              // open the core/geo update screen
	SubsQuit
)

// SubsCallbacks wire the model to storage without importing app internals.
type SubsCallbacks struct {
	Add    func(rawURL string) error                        // validate + save
	Delete func(index int) error                            // remove by index
	Reload func() ([]subscription.NamedSubscription, error) // re-read the list
	Mask   func(rawURL string) string                       // S-3 masking
	// Load fetches the chosen subscription's servers and stashes them for the
	// next screen. Running it here, still inside the alt-screen, keeps the shell
	// from flashing between menus during the network fetch (task #3). It takes the
	// URL rather than an index so it reads the model's live list, which may have
	// grown or shrunk (add/delete) since the caller's copy was captured.
	Load func(rawURL string) error
}

type subsMode int

const (
	subsList subsMode = iota
	subsAdding
	subsConfirmDelete
	subsLoading
)

// loadedMsg carries the result of SubsCallbacks.Load back into the model.
type loadedMsg struct{ err error }

type subsModel struct {
	subs   []subscription.NamedSubscription
	cb     SubsCallbacks
	cursor int
	mode   subsMode
	input  textinput.Model
	status string
	action SubsAction
	choice int
	// reveal shows the selected subscription's full URL. Masking stays on by
	// default so the personal token does not sit on screen (S-3); revealing is
	// per-row and deliberate.
	reveal bool
	width  int // terminal width; 0 until the first WindowSizeMsg
	height int // terminal height; 0 until the first WindowSizeMsg
}

// SelectSubscription shows the subscription list. On SubsSelected the returned
// index points into the (possibly reloaded) list, which is also returned.
// cursor is where the highlight starts: coming back from a subscription lands
// on the one just used, so the last-used one is visible at a glance. An index
// past the end (the list shrank) falls back to the top.
func SelectSubscription(subs []subscription.NamedSubscription, cursor int, cb SubsCallbacks) ([]subscription.NamedSubscription, int, SubsAction, error) {
	ti := textinput.New()
	ti.Placeholder = "https://... или vless://..."
	ti.CharLimit = 512
	ti.Width = 60

	if cursor < 0 || cursor >= len(subs) {
		cursor = 0
	}
	m := subsModel{subs: subs, cb: cb, cursor: cursor, input: ti, action: SubsQuit, choice: -1}
	// Nothing to select yet — go straight to the add prompt.
	if len(subs) == 0 {
		m.mode = subsAdding
		m.input.Focus()
	}

	res, err := runScreen(m)
	if err != nil {
		return subs, -1, SubsQuit, err
	}
	final := res.(subsModel)
	return final.subs, final.choice, final.action, nil
}

func (m subsModel) Init() tea.Cmd { return nil }

func (m subsModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if lm, ok := msg.(loadedMsg); ok {
		if lm.err != nil {
			m.mode = subsList
			m.status = errStyle.Render(lm.err.Error())
			return m, nil
		}
		m.action = SubsSelected
		return m, tea.Quit
	}

	if ws, ok := msg.(tea.WindowSizeMsg); ok {
		m.width, m.height = ws.Width, ws.Height
		return m, nil
	}

	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}

	// U-1: Ctrl+C always quits, from any mode.
	if key.Type == tea.KeyCtrlC {
		m.action = SubsQuit
		return m, tea.Quit
	}

	switch m.mode {
	case subsAdding:
		return m.updateAdding(key)
	case subsConfirmDelete:
		return m.updateConfirmDelete(key)
	case subsLoading:
		// Ignore input while the subscription is being fetched.
		return m, nil
	}
	return m.updateList(key)
}

func (m subsModel) updateList(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Cyrillic twins mirror the Russian layout (task #7): q→й, j→о, k→л, s→ы,
	// a→ф, d→в, u→г.
	switch key.String() {
	case "q", "й":
		// Top level: only q leaves the program — ← has nowhere further back to go,
		// and quitting on it would make the "← назад" reflex an accidental exit.
		m.action = SubsQuit
		return m, tea.Quit
	case "up", "k", "л":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j", "о":
		if m.cursor < len(m.subs)-1 {
			m.cursor++
		}
	case "enter", "right":
		// → opens the subscription, mirroring "into/forward" across the menus.
		if len(m.subs) == 0 {
			return m, nil
		}
		// Fetch the subscription here, in the loading state, so the alt-screen
		// stays up and the shell does not flash before the next menu (task #3).
		m.choice = m.cursor
		m.mode = subsLoading
		m.status = ""
		url := m.subs[m.cursor].URL
		load := m.cb.Load
		return m, func() tea.Msg { return loadedMsg{err: load(url)} }
	case "s", "ы":
		m.reveal = !m.reveal
	case "+", "a", "ф":
		m.mode = subsAdding
		m.input.SetValue("")
		m.input.Focus()
		m.status = ""
	case "d", "в":
		if len(m.subs) == 0 {
			m.status = errStyle.Render("Нет подписок для удаления")
			return m, nil
		}
		m.mode = subsConfirmDelete
	case "u", "г":
		m.action = SubsUpdate
		return m, tea.Quit
	}
	return m, nil
}

func (m subsModel) updateAdding(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.Type {
	case tea.KeyEsc:
		if len(m.subs) == 0 {
			m.action = SubsQuit
			return m, tea.Quit
		}
		m.mode = subsList
		m.status = ""
		return m, nil
	case tea.KeyEnter:
		rawURL := strings.TrimSpace(m.input.Value())
		if rawURL == "" {
			m.status = errStyle.Render("URL не может быть пустым")
			return m, nil
		}
		if err := m.cb.Add(rawURL); err != nil {
			m.status = errStyle.Render(err.Error())
			return m, nil
		}
		m.mode = subsList
		// M-1: a stale list is worse than a visible error — the new subscription
		// would be missing while the status claimed it was added.
		subs, err := m.cb.Reload()
		if err != nil {
			m.status = errStyle.Render(fmt.Sprintf("Список подписок не перечитан: %v", err))
			return m, nil
		}
		m.subs = subs
		m.cursor = len(m.subs) - 1
		m.status = okStyle.Render("Подписка добавлена")
		return m, nil
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(key)
	return m, cmd
}

func (m subsModel) updateConfirmDelete(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "y", "Y", "н", "Н", "enter":
		name := m.subs[m.cursor].Name
		switch err := m.cb.Delete(m.cursor); {
		case err != nil:
			m.status = errStyle.Render(fmt.Sprintf("Ошибка удаления: %v", err))
		default:
			// M-1: without the reloaded list the deleted subscription stays on
			// screen and can still be opened, so report the failure instead of
			// claiming success.
			subs, rerr := m.cb.Reload()
			if rerr != nil {
				m.status = errStyle.Render(fmt.Sprintf("Список подписок не перечитан: %v", rerr))
				break
			}
			m.subs = subs
			if m.cursor >= len(m.subs) && m.cursor > 0 {
				m.cursor--
			}
			m.status = okStyle.Render(fmt.Sprintf("Подписка «%s» удалена", name))
		}
	}
	m.mode = subsList
	return m, nil
}

func (m subsModel) View() string {
	var b strings.Builder
	b.WriteString(renderLogo(m.width, m.height) + "\n\n")

	if m.mode == subsAdding {
		b.WriteString("  Вставьте URL подписки (http/https) или ссылку на сервер\n")
		b.WriteString("  (vless/vmess/ss/hysteria2):\n")
		b.WriteString("  " + m.input.View() + "\n")
		if m.status != "" {
			b.WriteString("\n  " + m.status + "\n")
		}
		b.WriteString(legend(m.width, "  enter добавить · esc назад · ctrl+c выход"))
		return b.String()
	}

	if len(m.subs) == 0 {
		b.WriteString(dimStyle.Render("  Подписок нет — нажмите + чтобы добавить") + "\n")
	}
	for i, s := range m.subs {
		cursor := "  "
		line := fmt.Sprintf("%-28s %s", s.Name, dimStyle.Render(m.cb.Mask(s.URL)))
		if i == m.cursor {
			cursor = cursorStyle.Render("▸ ")
			line = selectedStyle.Render(fmt.Sprintf("%-28s ", s.Name)) + dimStyle.Render(m.cb.Mask(s.URL))
		}
		// The revealed URL replaces the masked preview in place — same row, running
		// off to the right untruncated so it stays a working link — instead of
		// dropping to a new line, which made the list jump vertically.
		if m.reveal && i == m.cursor {
			line = selectedStyle.Render(fmt.Sprintf("%-28s ", s.Name)) + urlStyle.Render(s.URL)
			b.WriteString("  " + cursor + line + "\n")
			continue
		}
		b.WriteString("  " + cursor + line + "\n")
	}

	if m.mode == subsLoading {
		b.WriteString("\n  " + dimStyle.Render("⏳ Загрузка серверов…") + "\n")
		return b.String()
	}

	if m.mode == subsConfirmDelete {
		b.WriteString("\n  " + warnStyle.Render(fmt.Sprintf("Удалить «%s»? (y/n)", m.subs[m.cursor].Name)) + "\n")
	} else if m.status != "" {
		b.WriteString("\n  " + m.status + "\n")
	}

	keys := "  ↑/↓ выбор · → открыть · s показать URL · + добавить · d удалить · u обновить · q выход"
	if m.reveal {
		keys = "  ↑/↓ выбор · → открыть · s скрыть URL · + добавить · d удалить · u обновить · q выход"
	}
	b.WriteString(legend(m.width, keys))
	return b.String()
}
