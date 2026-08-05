package tui

import (
	"errors"
	"fmt"
	"strings"
	"testing"

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
		Load: func(rawURL string) error { loadedURL = rawURL; return nil },
	}

	var m tea.Model = subsModel{subs: stored, cb: cb, choice: -1, input: textinput.New()}
	m = feed(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("+")}) // open add prompt

	// Add a second subscription: Reload now returns two entries.
	stored = append(stored, subscription.NamedSubscription{Name: "new", URL: "https://new.example/sub"})
	m = feed(m, typeURL("https://new.example/sub")...)
	m = feed(m, tea.KeyMsg{Type: tea.KeyEnter}) // confirm add → cursor lands on "new"

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
		if !strings.Contains(final.note.view(), "подписки не читаются") {
			t.Errorf("status = %q, want the reload error", final.note.view())
		}
		if strings.Contains(final.note.view(), "удалена") {
			t.Errorf("status claims success despite a stale list: %q", final.note.view())
		}
	})

	t.Run("after_add", func(t *testing.T) {
		m := subsModel{subs: stored, cb: cb, choice: -1, mode: subsAdding, input: textinput.New()}
		m.input.SetValue("https://three.example/sub")
		got, _ := m.updateAdding(tea.KeyMsg{Type: tea.KeyEnter})
		final := got.(subsModel)
		if !strings.Contains(final.note.view(), "подписки не читаются") {
			t.Errorf("status = %q, want the reload error", final.note.view())
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
