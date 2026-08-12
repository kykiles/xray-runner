package tui

import (
	"strings"
	"testing"

	"xray-runner/internal/subscription"
)

// Emoji are stripped where a name enters a screen, not where one is drawn, so
// that a screen added later cannot forget the call. These are those entrances:
// every name a model holds is already display-ready.
func TestEmojiStrippedAtTheBoundary(t *testing.T) {
	m := newList([]subscription.Profile{
		{Name: "🇩🇪 Германия", Balancer: &subscription.BalancerInfo{Strategy: "leastPing"}, Entries: []subscription.SubEntry{
			{Remarks: "⚡DE-1", Address: "de1.example.ru", Port: 443, Protocol: "vless", Network: "ws", UUID: validUUID},
		}},
	})
	m.expanded[0] = true
	for _, r := range m.rows() {
		if hasEmoji(r.name) {
			t.Errorf("row name reached the model with an emoji: %q", r.name)
		}
	}

	var subs subsModel
	subs.setSubs([]subscription.NamedSubscription{{URL: "https://p.example/s", Name: "🚀 Панель"}})
	if hasEmoji(subs.subs[0].Name) {
		t.Errorf("subscription name reached the model with an emoji: %q", subs.subs[0].Name)
	}

	var cfg cfgView
	cfg.show("🇩🇪 Германия", "{}", nil)
	if hasEmoji(cfg.title) {
		t.Errorf("config viewer title kept an emoji: %q", cfg.title)
	}

	// The connecting screen is the one this was found on: its title used to go
	// straight to the spinner line.
	cm := connectingModel{title: stripEmoji("🇩🇪 Германия")}
	if hasEmoji(cm.View()) {
		t.Errorf("connecting screen drew an emoji: %q", cm.View())
	}
}

// setSubs must not rewrite the caller's slice — storage writes the real names
// back from it.
func TestSetSubsLeavesTheCallersListAlone(t *testing.T) {
	stored := []subscription.NamedSubscription{{URL: "https://p.example/s", Name: "🚀 Панель"}}
	var m subsModel
	m.setSubs(stored)
	if stored[0].Name != "🚀 Панель" {
		t.Errorf("stored name was rewritten: %q", stored[0].Name)
	}
}

func hasEmoji(s string) bool {
	return strings.ContainsFunc(s, isEmoji)
}
