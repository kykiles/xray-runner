package tui

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"xray-runner/internal/updater"
)

// The update screen (task #8): reached from the subscription list, it downloads
// a fresh xray core or the geo databases from the official XTLS repo and swaps
// them in, keeping a .bak of whatever it replaces.

type updateStage int

const (
	updMenu     updateStage = iota // pick core / geo / back
	updReleases                    // pick which core version to install
	updWorking                     // fetching or installing; async in flight
	updDone                        // show the result
)

type updateKind int

const (
	kindCore updateKind = iota
	kindGeo
)

type releasesMsg struct {
	releases []updater.Release
	err      error
}
type geoReleaseMsg struct {
	release updater.Release
	err     error
}
type installedMsg struct{ err error }

type updateModel struct {
	ctx      context.Context
	xrayPath string
	dir      string

	stage    updateStage
	kind     updateKind
	cursor   int
	releases []updater.Release // core releases carrying an asset for this platform
	status   string
	err      error
	width    int
}

var updateMenu = []string{"Обновить ядро xray", "Обновить гео-базы", "Назад"}

// RunUpdate shows the update screen. xrayPath is the core binary; the geo
// databases live next to it.
func RunUpdate(ctx context.Context, xrayPath string) error {
	m := updateModel{ctx: ctx, xrayPath: xrayPath, dir: filepath.Dir(xrayPath)}
	_, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
	return err
}

func (m updateModel) Init() tea.Cmd { return nil }

func (m updateModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		return m, nil
	case releasesMsg:
		return m.onReleases(msg)
	case geoReleaseMsg:
		return m.onGeoRelease(msg)
	case installedMsg:
		m.stage = updDone
		m.err = msg.err
		return m, nil
	case tea.KeyMsg:
		return m.updateKey(msg)
	}
	return m, nil
}

// onReleases handles the core-version list: keep only releases with a build for
// this platform and let the user pick one.
func (m updateModel) onReleases(msg releasesMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.stage = updDone
		m.err = msg.err
		return m, nil
	}
	var withCore []updater.Release
	for _, r := range msg.releases {
		if _, ok := updater.CoreAsset(r); ok {
			withCore = append(withCore, r)
		}
	}
	if len(withCore) == 0 {
		m.stage = updDone
		m.err = fmt.Errorf("в последних релизах нет сборки ядра под вашу ОС/архитектуру")
		return m, nil
	}
	m.releases = updater.TrimToLatestStable(withCore)
	m.cursor = 0
	m.stage = updReleases
	return m, nil
}

// onGeoRelease installs the geo databases straight from the latest geo release.
func (m updateModel) onGeoRelease(msg geoReleaseMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.stage = updDone
		m.err = msg.err
		return m, nil
	}
	ip, site, ok := updater.GeoAssets(msg.release)
	if !ok {
		m.stage = updDone
		m.err = fmt.Errorf("в последнем релизе нет geoip.dat/geosite.dat")
		return m, nil
	}
	m.stage = updWorking
	m.status = "Скачивание гео-баз…"
	return m, m.installGeo(ip, site)
}

func (m updateModel) updateKey(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	if key.Type == tea.KeyCtrlC {
		return m, tea.Quit
	}

	switch m.stage {
	case updMenu:
		return m.keyMenu(key)
	case updReleases:
		return m.keyReleases(key)
	case updDone:
		// Any key returns to the menu; esc/q leaves the screen.
		switch key.String() {
		case "esc", "q", "й":
			return m, tea.Quit
		}
		m.stage = updMenu
		m.status = ""
		m.err = nil
		return m, nil
	case updWorking:
		// Ignore input while a download is in flight.
		return m, nil
	}
	return m, nil
}

func (m updateModel) keyMenu(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "esc", "q", "й":
		return m, tea.Quit
	case "up", "k", "л":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j", "о":
		if m.cursor < len(updateMenu)-1 {
			m.cursor++
		}
	case "enter":
		switch m.cursor {
		case 0:
			m.kind = kindCore
			m.stage = updWorking
			m.status = "Получение списка релизов…"
			return m, m.fetchReleases()
		case 1:
			m.kind = kindGeo
			m.stage = updWorking
			m.status = "Получение свежих гео-баз…"
			return m, m.fetchGeoRelease()
		default:
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m updateModel) keyReleases(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "esc", "left":
		m.stage = updMenu
		return m, nil
	case "q", "й":
		return m, tea.Quit
	case "up", "k", "л":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j", "о":
		if m.cursor < len(m.releases)-1 {
			m.cursor++
		}
	case "enter":
		r := m.releases[m.cursor]
		a, _ := updater.CoreAsset(r)
		m.stage = updWorking
		m.status = fmt.Sprintf("Скачивание и установка %s…", a.Name)
		return m, m.installCore(a)
	}
	return m, nil
}

func (m updateModel) fetchReleases() tea.Cmd {
	ctx := m.ctx
	return func() tea.Msg {
		rels, err := updater.FetchReleases(ctx, 20)
		return releasesMsg{releases: rels, err: err}
	}
}

func (m updateModel) fetchGeoRelease() tea.Cmd {
	ctx := m.ctx
	return func() tea.Msg {
		r, err := updater.FetchLatestGeoRelease(ctx)
		return geoReleaseMsg{release: r, err: err}
	}
}

func (m updateModel) installCore(a updater.Asset) tea.Cmd {
	ctx, path := m.ctx, m.xrayPath
	return func() tea.Msg {
		return installedMsg{err: updater.InstallCore(ctx, a, path)}
	}
}

func (m updateModel) installGeo(geoip, geosite updater.Asset) tea.Cmd {
	ctx, dir := m.ctx, m.dir
	return func() tea.Msg {
		return installedMsg{err: updater.InstallGeo(ctx, geoip, geosite, dir)}
	}
}

// releaseNote builds the styled "(последняя, стабильная)" style suffix shown next
// to a version tag. stableIdx is the index of the newest stable release (-1 if
// none). The suffix is empty when a release carries no note.
func releaseNote(i, stableIdx int, r updater.Release) string {
	var notes []string
	if i == 0 {
		notes = append(notes, "последняя")
	}
	if i == stableIdx {
		notes = append(notes, "стабильная")
	}
	if r.Prerelease {
		notes = append(notes, "pre-release")
	}
	if len(notes) == 0 {
		return ""
	}
	text := "  (" + strings.Join(notes, ", ") + ")"
	switch {
	case r.Prerelease:
		return warnStyle.Render(text)
	case i == stableIdx:
		return okStyle.Render(text)
	default:
		return dimStyle.Render(text)
	}
}

func (m updateModel) View() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("── Обновление") + "\n\n")

	switch m.stage {
	case updMenu:
		for i, item := range updateMenu {
			cursor := "  "
			line := item
			if i == m.cursor {
				cursor = cursorStyle.Render("▸ ")
				line = selectedStyle.Render(item)
			}
			b.WriteString("  " + cursor + line + "\n")
		}
		b.WriteString(legend("  ↑/↓ выбор · enter выбрать · esc назад"))

	case updReleases:
		b.WriteString(dimStyle.Render("  Выберите версию ядра:") + "\n\n")
		stableIdx := -1
		for i, r := range m.releases {
			if !r.Prerelease {
				stableIdx = i
				break
			}
		}
		for i, r := range m.releases {
			cursor := "  "
			tag := r.Tag
			if i == m.cursor {
				cursor = cursorStyle.Render("▸ ")
				tag = selectedStyle.Render(r.Tag)
			}
			b.WriteString("  " + cursor + clip(tag+releaseNote(i, stableIdx, r), m.width-4) + "\n")
		}
		b.WriteString(legend("  ↑/↓ выбор · enter установить · esc назад"))

	case updWorking:
		b.WriteString("  " + dimStyle.Render("⏳ "+m.status) + "\n")
		b.WriteString(legend("  подождите…"))

	case updDone:
		if m.err != nil {
			b.WriteString("  " + errStyle.Render("✖ "+m.err.Error()) + "\n")
		} else {
			b.WriteString("  " + okStyle.Render("✔ Готово") + "\n")
		}
		b.WriteString(legend("  любая клавиша · назад в меню · esc выход"))
	}

	return b.String()
}
