package tui

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"

	"xray-runner/internal/subscription"
)

// waitBench turns the next result off the channel into a message; a closed
// channel ends the run. gen tags the message with the run it came from, so a
// screen can drop what a cancelled run is still emitting.
func waitBench(ch chan subscription.BenchmarkResult, gen int) tea.Cmd {
	return func() tea.Msg {
		r, ok := <-ch
		if !ok {
			return benchDoneMsg{gen: gen}
		}
		return benchResultMsg{gen: gen, BenchmarkResult: r}
	}
}

// Both list screens draw one and the same table — NAME PROTOCOL TRANSPORT HOST,
// plus a right-aligned PING once a measurement runs — so a layout change lands
// on both at once instead of on whichever one was remembered. A profile row
// borrows protocol/transport/host from its first server: behind a balancer they
// are interchangeable endpoints, and that is what the Happ client puts on
// display too.
const (
	nameWidth  = 26
	protoWidth = 9
	netWidth   = 9
	hostWidth  = 28
	pingWidth  = 7
)

func tableHead(showPing bool) string {
	head := fmt.Sprintf("  %s %s %s %s",
		pad("NAME", nameWidth), pad("PROTOCOL", protoWidth),
		pad("TRANSPORT", netWidth), pad("HOST", hostWidth))
	if showPing {
		head += "  " + padLeft("PING", pingWidth)
	}
	return "  " + header(head)
}

// tableRow renders one row. showPing pads HOST to its full width so the PING
// numbers behind it share one right-aligned column instead of drifting with the
// host length.
func tableRow(name string, e subscription.SubEntry, showPing bool) string {
	host := "—"
	if e.Address != "" {
		host = truncate(fmt.Sprintf("%s:%d", e.Address, e.Port), hostWidth)
	}
	if showPing {
		host = pad(host, hostWidth)
	}
	return fmt.Sprintf("%s %s %s %s",
		pad(truncate(stripEmoji(name), nameWidth), nameWidth),
		pad(orDash(e.Protocol), protoWidth),
		pad(orDash(e.Network), netWidth),
		host)
}

// pingCell is the row's PING cell: the measured result, or a placeholder while
// the benchmark is still working through the list. Empty when neither applies.
func pingCell(r subscription.BenchmarkResult, measured, benching, best bool) string {
	switch {
	case measured:
		style := okStyle
		switch {
		case r.Error != nil:
			style = errStyle
		case best:
			style = bestStyle
		}
		return "  " + style.Render(padLeft(r.String(), pingWidth))
	case benching:
		return "  " + dimStyle.Render(padLeft("...", pingWidth))
	}
	return ""
}

// orDash keeps empty table cells visible as a placeholder instead of a hole.
func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}
