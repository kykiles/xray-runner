package app

import (
	"errors"
	"strings"
	"testing"

	"xray-runner/internal/config"
)

// H06: what a crashed run left in the routing table and the firewall is taken
// down before this run changes either, and the status screen says so — next to
// a note already queued, not in its place. One that would not come down is
// reported with what to expect.
func TestRecoverLeftovers(t *testing.T) {
	for name, tc := range map[string]struct {
		note string
		err  error
		want []string
	}{
		"nothing left":   {want: []string{"Прошлый режим TUN недоступен."}},
		"taken down":     {note: "Сняты остатки аварийно завершённого запуска: kill switch.", want: []string{"Прошлый режим TUN недоступен.", "kill switch"}},
		"not taken down": {err: errors.New("остатки аварийно завершённого запуска (kill switch) сняты не полностью"), want: []string{"Прошлый режим TUN недоступен.", "сняты не полностью", "Следующий запуск попробует снова"}},
	} {
		t.Run(name, func(t *testing.T) {
			a := New(&config.Config{}, Options{})
			a.recoverJournal = func() (string, error) { return tc.note, tc.err }
			a.pendingNote = "Прошлый режим TUN недоступен."

			a.recoverLeftovers()

			for _, s := range tc.want {
				if !strings.Contains(a.pendingNote, s) {
					t.Errorf("note %q lacks %q", a.pendingNote, s)
				}
			}
			if tc.note == "" && tc.err == nil && a.pendingNote != "Прошлый режим TUN недоступен." {
				t.Errorf("note %q with nothing left behind", a.pendingNote)
			}
		})
	}
}
