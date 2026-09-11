package tui

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"xray-runner/internal/subscription"
)

// Regression: after adding a subscription the model's list grows, so the
// enter/open path must key Load off the model's live list — not an index into
// the caller's stale copy, which used to panic with index-out-of-range.
func TestSubs_LoadUsesLiveListAfterAdd(t *testing.T) {
	stored := []subscription.NamedSubscription{
		{Name: "old", URL: "https://old.example/sub"},
	}
	var loadedURL string
	cb := SubsCallbacks{
		Add: func(string) error { return nil },
		Reload: func() ([]subscription.NamedSubscription, error) {
			return append([]subscription.NamedSubscription(nil), stored...), nil
		},
		Load: func(_ context.Context, rawURL string) error { loadedURL = rawURL; return nil },
	}

	var m tea.Model = subsModel{subs: stored, cb: cb, choice: -1, input: textinput.New()}
	m = feed(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("+")}) // open add prompt

	// Add a second subscription: Reload now returns two entries.
	stored = append(stored, subscription.NamedSubscription{Name: "new", URL: "https://new.example/sub"})
	m = feed(m, typeURL("https://new.example/sub")...)
	m = confirmAdd(t, m) // confirm add → cursor lands on "new"

	// Open the highlighted (newly added) subscription.
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected a Load command on enter")
	}
	if _, ok := cmd().(loadedMsg); !ok {
		t.Fatalf("expected loadedMsg, got %T", cmd())
	}
	_ = next
	if loadedURL != "https://new.example/sub" {
		t.Fatalf("Load got %q, want the newly added subscription URL", loadedURL)
	}
}

// confirmAdd presses enter on the add prompt and delivers the result. Adding
// goes to the panel for the subscription's name, so it lands as a message
// rather than finishing inside the key handler.
func confirmAdd(t *testing.T, m tea.Model) tea.Model {
	t.Helper()
	m, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter on the add prompt scheduled nothing")
	}
	msg := cmd()
	if _, ok := msg.(addedMsg); !ok {
		t.Fatalf("expected addedMsg, got %T", msg)
	}
	return feed(m, msg)
}

func feed(m tea.Model, msgs ...tea.Msg) tea.Model {
	for _, msg := range msgs {
		m, _ = m.Update(msg)
	}
	return m
}

func typeURL(s string) []tea.Msg {
	msgs := make([]tea.Msg, 0, len(s))
	for _, r := range s {
		msgs = append(msgs, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	return msgs
}

// M-1: a failed Reload used to be swallowed, leaving the list stale — a deleted
// subscription stayed on screen and could still be opened, while the status
// line cheerfully reported success.
func TestSubs_ReloadFailureIsReported(t *testing.T) {
	stored := []subscription.NamedSubscription{
		{Name: "one", URL: "https://one.example/sub"},
		{Name: "two", URL: "https://two.example/sub"},
	}
	cb := SubsCallbacks{
		Add:    func(string) error { return nil },
		Delete: func(int) error { return nil },
		Reload: func() ([]subscription.NamedSubscription, error) {
			return nil, errors.New("подписки не читаются")
		},
	}

	t.Run("after_delete", func(t *testing.T) {
		m := subsModel{subs: stored, cb: cb, choice: -1, input: textinput.New()}
		got, _ := m.updateConfirmDelete(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
		final := got.(subsModel)
		if !strings.Contains(final.note.view(), "не перечитан") {
			t.Errorf("status = %q, want the reload failure", final.note.view())
		}
		if strings.Contains(final.note.view(), "удалена") {
			t.Errorf("status claims success despite a stale list: %q", final.note.view())
		}
	})

	t.Run("after_add", func(t *testing.T) {
		m := subsModel{subs: stored, cb: cb, choice: -1, mode: subsAdding, input: textinput.New()}
		m.input.SetValue("https://three.example/sub")
		final := confirmAdd(t, m).(subsModel)
		if !strings.Contains(final.note.view(), "не перечитан") {
			t.Errorf("status = %q, want the reload failure", final.note.view())
		}
	})
}

// A happ://crypt5 link is ~840 characters; a CharLimit would truncate it on
// paste and the decrypt at add time would fail with a confusing error.
func TestSubsInputTakesLongHappLink(t *testing.T) {
	link := "happ://crypt5/" + strings.Repeat("A", 900)
	ti := newSubsInput()
	ti.SetValue(link)
	if ti.Value() != link {
		t.Fatalf("ссылка обрезана: %d из %d символов", len(ti.Value()), len(link))
	}
}

// Task #2: many subscriptions used to be printed in full, so the terminal
// scrolled and took the wordmark off the top with it. The rows now scroll
// inside their own window: the view fits the terminal, the logo survives, and
// the cursor stays on screen wherever it goes.
func TestSubs_ScrollsInsteadOfEatingTheLogo(t *testing.T) {
	subs := make([]subscription.NamedSubscription, 30)
	for i := range subs {
		subs[i] = subscription.NamedSubscription{
			Name: fmt.Sprintf("Подписка %02d", i),
			URL:  fmt.Sprintf("https://p%02d.example/sub", i),
		}
	}

	const height = 24
	var m tea.Model = subsModel{subs: subs, choice: -1, input: textinput.New()}
	m = feed(m, tea.WindowSizeMsg{Width: 100, Height: height})

	for _, cursor := range []int{0, 15, 29} {
		sm := m.(subsModel)
		sm.cursor = cursor
		view := sm.View()

		if got := lipgloss.Height(view); got > height {
			t.Errorf("cursor %d: view is %d lines, does not fit %d", cursor, got, height)
		}
		if !strings.Contains(view, "██") {
			t.Errorf("cursor %d: wordmark scrolled off the view", cursor)
		}
		if want := subs[cursor].Name; !strings.Contains(view, want) {
			t.Errorf("cursor %d: %q is not visible", cursor, want)
		}
		if !strings.Contains(view, "выход") {
			t.Errorf("cursor %d: legend pushed off the view", cursor)
		}
	}
}

// A list that fits needs no indicators, and every row stays on screen.
func TestSubs_ShortListIsNotWindowed(t *testing.T) {
	subs := []subscription.NamedSubscription{
		{Name: "первая", URL: "https://a.example/sub"},
		{Name: "вторая", URL: "https://b.example/sub"},
	}
	var m tea.Model = subsModel{subs: subs, choice: -1, input: textinput.New()}
	m = feed(m, tea.WindowSizeMsg{Width: 100, Height: 24})

	view := m.(subsModel).View()
	for _, s := range subs {
		if !strings.Contains(view, s.Name) {
			t.Errorf("%q is missing from a list that fits", s.Name)
		}
	}
	if strings.Contains(view, "ещё") {
		t.Error("a list that fits must not show scroll indicators")
	}
}

// Task #6: adding now goes to the panel for a name, so the screen must say so
// instead of freezing, and a validation failure must hand the typed URL back
// rather than dropping the user into the list.
func TestSubs_AddShowsTheWaitAndKeepsTheUrlOnFailure(t *testing.T) {
	cb := SubsCallbacks{
		Add:    func(string) error { return errors.New("не похоже на ссылку подписки") },
		Reload: func() ([]subscription.NamedSubscription, error) { return nil, nil },
	}
	m := subsModel{cb: cb, choice: -1, mode: subsAdding, input: textinput.New()}
	m.input.SetValue("мусор")

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	waiting := next.(subsModel)
	if waiting.mode != subsLoading {
		t.Errorf("mode = %v while the panel is being asked, want subsLoading", waiting.mode)
	}
	if view := waiting.View(); !strings.Contains(view, "Получение названия") {
		t.Errorf("the wait is not on screen:\n%s", view)
	}

	final := feed(waiting, cmd()).(subsModel)
	if final.mode != subsAdding {
		t.Errorf("mode = %v after a rejected URL, want back at the prompt", final.mode)
	}
	if final.input.Value() != "мусор" {
		t.Errorf("typed URL was dropped: %q", final.input.Value())
	}
	// Audit #3: the reason goes to the log, the screen keeps one short line.
	if !strings.Contains(final.note.view(), "Подписка не добавлена") {
		t.Errorf("notice = %q, want the short failure line", final.note.view())
	}
}

// Opening a subscription and adding one both wait on the network, and the two
// waits must not describe each other.
func TestSubs_WaitLinesAreDistinct(t *testing.T) {
	cb := SubsCallbacks{Load: func(context.Context, string) error { return nil }}
	m := subsModel{
		subs:   []subscription.NamedSubscription{{Name: "панель", URL: "https://x.example/sub"}},
		cb:     cb,
		choice: -1,
		input:  textinput.New(),
	}
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if view := next.(subsModel).View(); !strings.Contains(view, "Загрузка серверов") {
		t.Errorf("opening a subscription shows the wrong wait:\n%s", view)
	}
}

// A08: Esc during "Загрузка серверов…" backs out and stops the fetch instead of
// leaving the user to sit out its timeout, and the late result is ignored.
func TestSubs_EscCancelsLoad(t *testing.T) {
	started := make(chan context.Context, 1)
	cb := SubsCallbacks{Load: func(ctx context.Context, _ string) error {
		started <- ctx
		<-ctx.Done()
		return ctx.Err()
	}}
	var m tea.Model = subsModel{
		subs:   []subscription.NamedSubscription{{Name: "панель", URL: "https://x.example/sub"}},
		cb:     cb,
		action: SubsQuit, // as SelectSubscription starts it; the zero value is SubsSelected
		choice: -1,
		input:  textinput.New(),
	}
	m, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	ctx := <-started

	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("Esc did not stop the fetch")
	}
	if got := m.(subsModel).mode; got != subsList {
		t.Errorf("mode = %v after Esc, want back at the list", got)
	}

	// The user opens it again at once; the cancelled load reports in only now
	// and must not be taken for the new one.
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	final := feed(m, <-done).(subsModel)
	if final.action == SubsSelected || final.mode != subsLoading {
		t.Errorf("a cancelled load answered for the new one: action=%v mode=%v", final.action, final.mode)
	}
}

// Audit #6: s unfolds the URL as a child row under its subscription, not as a
// tail on the same line, and the row it belongs to is the one under the cursor.
func TestSubs_RevealUnfoldsUnderTheRow(t *testing.T) {
	m := subsModel{
		subs: []subscription.NamedSubscription{
			{Name: "первая", URL: "https://one.example/sub?token=a"},
			{Name: "вторая", URL: "https://two.example/sub?token=b"},
		},
		cursor: 1,
		reveal: true,
		choice: -1,
		input:  textinput.New(),
	}
	lines := strings.Split(m.View(), "\n")
	i := slices.IndexFunc(lines, func(l string) bool { return strings.Contains(l, "вторая") })
	if i < 0 {
		t.Fatalf("the selected subscription is not on screen:\n%s", m.View())
	}
	if strings.Contains(lines[i], "two.example") {
		t.Errorf("the URL is still glued to the name row: %q", lines[i])
	}
	if !strings.Contains(lines[i+1], "└─") || !strings.Contains(lines[i+1], "two.example") {
		t.Errorf("row below the cursor = %q, want the URL unfolded under it", lines[i+1])
	}
	if strings.Contains(m.View(), "one.example") {
		t.Error("a row without the cursor revealed its URL")
	}
}
