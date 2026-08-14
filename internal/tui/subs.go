package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"xray-runner/internal/subscription"
)

// SubsAction is the outcome of the subscription screen.
type SubsAction int

const (
	SubsSelected SubsAction = iota
	SubsUpdate              // open the core/geo update screen
	SubsApps                // open the split-tunnel process picker
	SubsQuit
)

// SubsCallbacks wire the model to storage without importing app internals.
type SubsCallbacks struct {
	Add    func(rawURL string) error                        // validate + save
	Delete func(index int) error                            // remove by index
	Reload func() ([]subscription.NamedSubscription, error) // re-read the list
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

// addedMsg carries the result of SubsCallbacks.Add back into the model. Adding
// now goes to the panel for the subscription's name, so it cannot run inside
// the key handler without freezing the screen for the length of a request.
type addedMsg struct{ err error }

type subsModel struct {
	subs   []subscription.NamedSubscription
	cb     SubsCallbacks
	cursor int
	mode   subsMode
	input  textinput.Model
	note   notice
	// busy is what the waiting line says while a callback is in flight. Both
	// waits look the same on screen and differ only in wording.
	busy   string
	action SubsAction
	choice int
	// reveal shows the selected subscription's full URL. Masking stays on by
	// default so the personal token does not sit on screen (S-3); revealing is
	// per-row and deliberate.
	reveal bool
	width  int // terminal width; 0 until the first WindowSizeMsg
	height int // terminal height; 0 until the first WindowSizeMsg
}

// setSubs takes a freshly read list and keeps it display-ready: a panel names
// its subscription with emoji the console font cannot draw, so they are stripped
// once on the way in rather than at every line that prints a name — a screen
// added later would have to remember the call. The list is copied, so the
// caller's slice keeps the names storage writes back.
func (m *subsModel) setSubs(subs []subscription.NamedSubscription) {
	m.subs = make([]subscription.NamedSubscription, len(subs))
	for i, s := range subs {
		s.Name = stripEmoji(s.Name)
		m.subs[i] = s
	}
}

// newSubsInput builds the add-subscription field. No CharLimit: a happ://crypt5
// link runs well past 800 characters, and a truncated one fails to decrypt.
func newSubsInput() textinput.Model {
	ti := textinput.New()
	ti.Placeholder = "https://... или vless://..."
	ti.Width = 60
	return ti
}

// SelectSubscription shows the subscription list. On SubsSelected the returned
// index points into the (possibly reloaded) list, which is also returned.
// cursor is where the highlight starts: coming back from a subscription lands
// on the one just used, so the last-used one is visible at a glance. An index
// past the end (the list shrank) falls back to the top. The returned list is the
// display copy — see setSubs — so it is not what storage should be written from.
func SelectSubscription(subs []subscription.NamedSubscription, cursor int, cb SubsCallbacks) ([]subscription.NamedSubscription, int, SubsAction, error) {
	ti := newSubsInput()
	if cursor < 0 || cursor >= len(subs) {
		cursor = 0
	}
	m := subsModel{cb: cb, cursor: cursor, input: ti, action: SubsQuit, choice: -1}
	m.setSubs(subs)
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
			return m, m.note.failErr("Не удалось загрузить подписку — подробности в логе", lm.err)
		}
		m.action = SubsSelected
		return m, tea.Quit
	}

	if ws, ok := msg.(tea.WindowSizeMsg); ok {
		m.width, m.height = ws.Width, ws.Height
		return m, onResize()
	}

	if tick, ok := msg.(noticeTickMsg); ok {
		return m, m.note.tick(tick)
	}

	if am, ok := msg.(addedMsg); ok {
		if am.err != nil {
			// Back to the prompt with the typed URL intact, so a typo can be fixed
			// instead of retyped.
			m.mode = subsAdding
			return m, m.note.failErr("Подписка не добавлена — подробности в логе", am.err)
		}
		m.mode = subsList
		// M-1: a stale list is worse than a visible error — the new subscription
		// would be missing while the notice claimed it was added.
		subs, err := m.cb.Reload()
		if err != nil {
			return m, m.note.failErr("Список подписок не перечитан", err)
		}
		m.setSubs(subs)
		m.cursor = len(m.subs) - 1
		return m, m.note.ok("Подписка добавлена")
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
		m.busy = "Загрузка серверов…"
		m.note.clear()
		url := m.subs[m.cursor].URL
		load := m.cb.Load
		return m, func() tea.Msg { return loadedMsg{err: load(url)} }
	case "s", "ы":
		m.reveal = !m.reveal
	case "+", "a", "ф":
		m.mode = subsAdding
		m.input.SetValue("")
		m.input.Focus()
		m.note.clear()
	case "d", "в":
		if len(m.subs) == 0 {
			return m, m.note.fail("Нет подписок для удаления")
		}
		m.mode = subsConfirmDelete
	case "u", "г":
		m.action = SubsUpdate
		return m, tea.Quit
	case "p", "з":
		m.action = SubsApps
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
		m.note.clear()
		return m, nil
	case tea.KeyEnter:
		rawURL := strings.TrimSpace(m.input.Value())
		if rawURL == "" {
			return m, m.note.fail("URL не может быть пустым")
		}
		// Saving is instant; asking the panel what it calls itself is not, so the
		// wait happens here in the loading state rather than behind a frozen screen.
		m.mode = subsLoading
		m.busy = "Получение названия…"
		m.note.clear()
		add := m.cb.Add
		return m, func() tea.Msg { return addedMsg{err: add(rawURL)} }
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(key)
	return m, cmd
}

func (m subsModel) updateConfirmDelete(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	switch key.String() {
	case "y", "Y", "н", "Н", "enter":
		name := m.subs[m.cursor].Name
		switch err := m.cb.Delete(m.cursor); {
		case err != nil:
			cmd = m.note.failErr("Подписка не удалена", err)
		default:
			// M-1: without the reloaded list the deleted subscription stays on
			// screen and can still be opened, so report the failure instead of
			// claiming success.
			subs, rerr := m.cb.Reload()
			if rerr != nil {
				cmd = m.note.failErr("Список подписок не перечитан", rerr)
				break
			}
			m.setSubs(subs)
			if m.cursor >= len(m.subs) && m.cursor > 0 {
				m.cursor--
			}
			cmd = m.note.ok(fmt.Sprintf("Подписка «%s» удалена", name))
		}
	}
	m.mode = subsList
	return m, cmd
}

func (m subsModel) View() string {
	var b strings.Builder
	logo := renderLogo(m.width, m.height)
	b.WriteString(logo + "\n\n")

	if m.mode == subsAdding {
		b.WriteString("  " + textStyle.Render("Вставьте URL подписки (http/https) или ссылку на сервер") + "\n")
		b.WriteString("  " + textStyle.Render("(vless/vmess/ss/hysteria2):") + "\n")
		b.WriteString("  " + m.input.View() + "\n")
		b.WriteString("\n  " + m.note.view() + "\n")
		b.WriteString(legend(m.width, "  enter добавить · esc назад · ctrl+c выход"))
		return b.String()
	}

	keys := "  ↑/↓ выбор · → открыть · s показать URL · + добавить · d удалить · p процессы · u обновить · q выход"
	if m.reveal {
		keys = "  ↑/↓ выбор · → открыть · s скрыть URL · + добавить · d удалить · p процессы · u обновить · q выход"
	}

	if len(m.subs) == 0 {
		b.WriteString(dimStyle.Render("  Подписок нет — нажмите + чтобы добавить") + "\n")
	}
	// The rows scroll inside their own window, the way the server list does:
	// printing every subscription made the terminal scroll instead, and the
	// wordmark went off the top with it. Chrome around the rows is the logo's
	// blank line, the two scroll indicators and the notice — four lines, plus
	// however many the legend wraps to.
	rows := len(m.subs)
	if m.height > 0 {
		// A revealed URL unfolds as a line of its own under the cursor's row, so
		// the window gives one row back to it.
		reveal := 0
		if m.reveal {
			reveal = 1
		}
		rows = max(1, m.height-(lipgloss.Height(logo)+4+reveal+legendHeight(m.width, keys)))
	}
	start, end, above, below := window(len(m.subs), m.cursor, rows)

	b.WriteString(moreUp(above))
	for i := start; i < end; i++ {
		s := m.subs[i]
		cursor := "  "
		// Task #1: the list shows only the panel name, never the URL — the token
		// stays off screen until asked for with s. The panel writes its name with
		// emoji; they are stripped here so the revealed URL stays in line.
		name := pad(s.Name, 28)
		line := textStyle.Render(name)
		if i == m.cursor {
			cursor = cursorStyle.Render("▸ ")
			line = selectedStyle.Render(name)
		}
		b.WriteString("  " + cursor + line + "\n")
		// s unfolds the full URL as a child row under the subscription it belongs
		// to, so scrolling shows each one's link in turn. Untruncated, running off
		// to the right if it has to, so it stays a working link.
		if m.reveal && i == m.cursor {
			b.WriteString("      " + dimStyle.Render("└─ ") + urlStyle.Render(s.URL) + "\n")
		}
	}
	// moreDown always occupies its line, so the notice below keeps its place
	// instead of jumping as the window scrolls past the end of the list — and it
	// gives the notice the same air the server screen has (task #3).
	b.WriteString(moreDown(below))

	if m.mode == subsLoading {
		b.WriteString("  " + dimStyle.Render(m.busy) + "\n")
		return b.String()
	}

	if m.mode == subsConfirmDelete {
		b.WriteString("  " + warnStyle.Render(fmt.Sprintf("Удалить «%s»? (y/n)", m.subs[m.cursor].Name)) + "\n")
	} else {
		b.WriteString("  " + m.note.view() + "\n")
	}

	b.WriteString(legend(m.width, keys))
	return b.String()
}
