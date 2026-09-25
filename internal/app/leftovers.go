package app

import (
	"fmt"
	"log/slog"
)

// recoverLeftovers takes down what a run that died without its teardown left
// in the routing table and the firewall (H06), before this run changes either,
// and says on the status screen what it found. A leftover that would not come
// down is reported and left for the next run, which tries again. The system
// proxy is not among them: warnDeadLoopbackProxy sees to it, since the setting
// itself shows whether it is a leftover.
func (a *App) recoverLeftovers() {
	note, err := a.recoverJournal()
	if err != nil {
		slog.Error("остатки аварийно завершённого запуска не сняты", "error", err)
		a.noteOnStatus(fmt.Sprintf("Внимание: %v. Следующий запуск попробует снова, перезагрузка снимет их наверняка.", err))
		return
	}
	if note != "" {
		slog.Warn("сняты остатки аварийно завершённого запуска", "note", note)
		a.noteOnStatus(note)
	}
}
