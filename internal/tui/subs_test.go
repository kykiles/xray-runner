package tui

import (
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
		Mask: func(s string) string { return s },
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
