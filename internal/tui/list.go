package tui

import (
	"context"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"xray-runner/internal/subscription"
)

// Профили и серверы — один и тот же список: фильтр над строками и замер,
// который стримит в них результаты. Обе части состояния живут здесь, чтобы
// правка ложилась сразу на оба экрана, а не на тот, про который вспомнили.

type filterState struct {
	input  textinput.Model
	typing bool
}

func newFilter() filterState {
	fi := textinput.New()
	fi.Placeholder = "поиск по всем столбцам"
	fi.CharLimit = 64
	fi.Width = 40
	return filterState{input: fi}
}

func (f filterState) value() string { return f.input.Value() }

// shown reports whether the filter row belongs on screen: either it is being
// typed into or it holds a query that narrows the list.
func (f filterState) shown() bool { return f.typing || f.value() != "" }

func (f *filterState) start() {
	f.typing = true
	f.input.Focus()
}

// clear drops the query; false when there was none, and ← then means "back".
func (f *filterState) clear() bool {
	if f.value() == "" {
		return false
	}
	f.input.SetValue("")
	return true
}

// key feeds the keystroke to the input while typing. handled is false when the
// screen should deal with the key itself.
func (f *filterState) key(key tea.KeyMsg) (handled bool, cmd tea.Cmd) {
	if !f.typing {
		return false, nil
	}
	switch key.Type {
	case tea.KeyEsc:
		f.typing = false
		f.input.SetValue("")
		f.input.Blur()
	case tea.KeyEnter:
		f.typing = false
		f.input.Blur()
	default:
		f.input, cmd = f.input.Update(key)
	}
	return true, cmd
}

// visible returns the indices of the n rows matching the filter, in the list's
// own order; hay yields the searchable text of a row.
func (f filterState) visible(n int, hay func(int) string) []int {
	terms := strings.Fields(strings.ToLower(strings.TrimSpace(f.value())))
	out := make([]int, 0, n)
	for i := range n {
		if matchTerms(hay(i), terms) {
			out = append(out, i)
		}
	}
	return out
}

// matchTerms reports whether every term appears in hay. Terms narrow the list
// (AND); each is a plain substring, so "vless" finds the protocol column, "ws"
// the transport, part of a name the name column.
func matchTerms(hay string, terms []string) bool {
	for _, t := range terms {
		if !strings.Contains(hay, t) {
			return false
		}
	}
	return true
}

// benchState is one benchmark run in flight.
type benchState struct {
	ch      chan subscription.BenchmarkResult
	cancel  context.CancelFunc
	gen     int // bumped per run; results of older runs are dropped
	running bool
	done    int
	total   int
}

// start cancels whatever run is in flight and launches a new one. Results of
// the old run carry the old generation and are dropped on arrival: pressing b
// again after changing the filter must measure the new selection, not keep
// finishing the old one.
func (b *benchState) start(ctx context.Context, n int, run func(context.Context, func(subscription.BenchmarkResult))) tea.Cmd {
	b.stop()
	ctx, cancel := context.WithCancel(ctx)
	b.cancel, b.running, b.done, b.total = cancel, true, 0, n
	b.gen++
	gen := b.gen

	// Buffered for the whole run so a cancelled goroutine nobody reads from any
	// more still finishes and closes instead of blocking on the send.
	ch := make(chan subscription.BenchmarkResult, n+1)
	b.ch = ch
	go func() {
		run(ctx, func(r subscription.BenchmarkResult) { ch <- r })
		close(ch)
	}()
	return waitBench(ch, gen)
}

// stop ends the running measurement; safe on an idle state.
func (b *benchState) stop() {
	if b.cancel != nil {
		b.cancel()
		b.cancel = nil
	}
	b.running = false
}

// accept reports whether a message belongs to the current run.
func (b benchState) accept(gen int) bool { return gen == b.gen }

// benchLine is the progress line both screens show while measuring.
func benchLine(b benchState) string {
	return dimStyle.Render(fmt.Sprintf("⏳ Замер latency... %d/%d", b.done, b.total))
}

// filterKeys replaces the legend while the filter is being typed into.
const filterKeys = "  фильтр: поиск по всем столбцам · enter применить · esc сбросить"
