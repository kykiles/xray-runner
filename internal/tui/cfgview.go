package tui

import "strings"

// cfgView is the full-config viewer shared by the profile and server screens:
// pretty-printed JSON of the config a session would launch — inbounds, dns,
// routing rules, outbounds — scrolled in place over the list. The zero value is
// closed.
type cfgView struct {
	lines  []string
	title  string
	top    int
	save   func() (string, error) // writes the config to disk, returns the path
	status string
}

func (v cfgView) open() bool { return v.lines != nil }

// saver binds the shown config to the screen's save callback, or nil when the
// screen has nowhere to write it.
func saver(save SaveConfigFunc, name, text string) func() (string, error) {
	if save == nil {
		return nil
	}
	return func() (string, error) { return save(name, text) }
}

func (v *cfgView) show(title, text string, save func() (string, error)) {
	v.lines = strings.Split(text, "\n")
	v.title = stripEmoji(title)
	v.top = 0
	v.save = save
	v.status = ""
}

// key scrolls or closes the viewer. It reports whether the whole screen should
// quit — the only key the viewer passes on.
func (v *cfgView) key(s string, height, width int) (quit bool) {
	switch s {
	case "esc", "left", "c", "с":
		v.lines = nil
	case "q", "й":
		return true
	case "up", "k", "л":
		if v.top > 0 {
			v.top--
		}
	case "down", "j", "о":
		if v.top < len(v.lines)-v.budget(height, width) {
			v.top++
		}
	case "pgup":
		v.top = max(0, v.top-v.budget(height, width))
	case "pgdown":
		v.top = min(max(0, len(v.lines)-v.budget(height, width)), v.top+v.budget(height, width))
	case "s", "ы":
		if v.save == nil {
			break
		}
		path, err := v.save()
		if err != nil {
			v.status = errStyle.Render("Не сохранено: " + err.Error())
		} else {
			v.status = okStyle.Render("Сохранено: " + path)
		}
	}
	return false
}

const cfgKeys = "  ↑/↓ прокрутка · PgUp/PgDn страница · s сохранить · ← назад · q выход"

// budget is how many JSON lines fit on screen: title(2) + both scroll
// indicators + the status line + the legend.
func (v cfgView) budget(height, width int) int {
	if height <= 0 {
		return len(v.lines)
	}
	return max(1, height-(2+2+1+legendHeight(width, cfgKeys)))
}

func (v cfgView) view(width, height int) string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("  "+v.title+" · конфиг") + "\n\n")

	end := min(v.top+v.budget(height, width), len(v.lines))
	b.WriteString(moreUp(v.top))
	for _, line := range v.lines[v.top:end] {
		b.WriteString("  " + clip(line, width-2) + "\n")
	}
	b.WriteString(moreDown(len(v.lines) - end))

	// The status line always occupies its row, empty or not — see the budget.
	b.WriteString("  " + v.status + "\n")
	b.WriteString(legend(width, cfgKeys))
	return b.String()
}
