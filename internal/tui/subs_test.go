package tui

import (
	"errors"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

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
		if !strings.Contains(final.status, "подписки не читаются") {
			t.Errorf("status = %q, want the reload error", final.status)
		}
		if strings.Contains(final.status, "удалена") {
			t.Errorf("status claims success despite a stale list: %q", final.status)
		}
	})

	t.Run("after_add", func(t *testing.T) {
		m := subsModel{subs: stored, cb: cb, choice: -1, mode: subsAdding, input: textinput.New()}
		m.input.SetValue("https://three.example/sub")
		got, _ := m.updateAdding(tea.KeyMsg{Type: tea.KeyEnter})
		final := got.(subsModel)
		if !strings.Contains(final.status, "подписки не читаются") {
			t.Errorf("status = %q, want the reload error", final.status)
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
