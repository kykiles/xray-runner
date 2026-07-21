package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"

	"xray-runner/internal/updater"
)

// The update screen (task #8): reached from the subscription list, it downloads
// a fresh xray core or the geo databases from the official XTLS repo and swaps
// them in atomically, verifying the SHA-256 of the core before installing.

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
	ctx       context.Context
	xrayPath  string
	dir       string
	installed string // current core version number, e.g. "26.6.27" ("" if unknown)
	geoDate   string // geoip.dat modification date, e.g. "2026-07-16" ("" if unknown)

	stage      updateStage
	kind       updateKind
	cursor     int
	pendingTag string            // core release tag being installed, for the header refresh
	releases   []updater.Release // core releases carrying an asset for this platform
	status     string
	err        error
	width      int
	height     int
	spinner    spinner.Model
}

var updateMenu = []string{"Обновить ядро xray", "Обновить гео-базы"}

// RunUpdate shows the update screen. xrayPath is the core binary; the geo
// databases live next to it. installed is the currently-running core version
// (empty if it could not be determined), shown so the user knows what they are
// updating from.
func RunUpdate(ctx context.Context, xrayPath, installed string) error {
	dir := filepath.Dir(xrayPath)
	sp := spinner.New(spinner.WithSpinner(spinner.Dot), spinner.WithStyle(cursorStyle))
	m := updateModel{ctx: ctx, xrayPath: xrayPath, dir: dir, installed: installed, geoDate: geoDate(dir), spinner: sp}
	_, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
	return err
}

// geoDate is the modification date of geoip.dat next to the core — the closest
// thing to a geo-database version we have. Empty when the file is missing.
func geoDate(dir string) string {
	fi, err := os.Stat(filepath.Join(dir, "geoip.dat"))
	if err != nil {
		return ""
	}
	return fi.ModTime().Format("2006-01-02")
}

func (m updateModel) Init() tea.Cmd { return m.spinner.Tick }

func (m updateModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	case releasesMsg:
		return m.onReleases(msg)
	case geoReleaseMsg:
		return m.onGeoRelease(msg)
	case installedMsg:
		m.stage = updDone
		m.err = msg.err
		if msg.err == nil {
			// Refresh the header so it reflects what we just installed instead of
			// the values captured when the screen opened — otherwise the old core
			// version / geo date lingers until the user leaves and re-enters.
			switch m.kind {
			case kindCore:
				m.installed = strings.TrimPrefix(strings.ToLower(m.pendingTag), "v")
			case kindGeo:
				m.geoDate = geoDate(m.dir)
			}
		}
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
		// Any key returns to the menu; ←/esc/q leaves the screen.
		switch key.String() {
		case "esc", "left", "q", "й":
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
	case "esc", "left", "q", "й":
		return m, tea.Quit
	case "up", "k", "л":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j", "о":
		if m.cursor < len(updateMenu)-1 {
			m.cursor++
		}
	case "enter", "right":
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
	case "enter", "right":
		r := m.releases[m.cursor]
		a, _ := updater.CoreAsset(r)
		m.pendingTag = r.Tag
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
	b.WriteString(titleStyle.Render("Обновление") + "\n")
	cur := m.installed
	if cur == "" {
		cur = "неизвестно"
	}
	geo := m.geoDate
	if geo == "" {
		geo = "неизвестно"
	}
	b.WriteString("  " + dimStyle.Render("Текущее ядро: ") + cur + "\n")
	b.WriteString("  " + dimStyle.Render("Гео-базы: ") + geo + "\n\n")

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
		b.WriteString(legend(m.width, "  ↑/↓ выбор · → выбрать · ← назад"))

	case updReleases:
		b.WriteString(dimStyle.Render("  Выберите версию ядра:") + "\n\n")
		stableIdx := -1
		for i, r := range m.releases {
			if !r.Prerelease {
				stableIdx = i
				break
			}
		}

		keys := "  ↑/↓ выбор · → установить · ← назад"
		// L-3: fit the list into the terminal like the other list screens do,
		// reserving the fixed chrome: title(1) + header(2) + prompt(2) + legend +
		// the two scroll indicator lines.
		budget := len(m.releases)
		if m.height > 0 {
			if budget = m.height - (1 + 2 + 2 + legendHeight(m.width, keys) + 2); budget < 1 {
				budget = 1
			}
		}
		start, end, above, below := window(len(m.releases), m.cursor, budget)

		b.WriteString(moreUp(above))
		for i := start; i < end; i++ {
			r := m.releases[i]
			cursor := "  "
			tag := r.Tag
			if i == m.cursor {
				cursor = cursorStyle.Render("▸ ")
				tag = selectedStyle.Render(r.Tag)
			}
			note := releaseNote(i, stableIdx, r)
			if updater.SameVersion(r.Tag, m.installed) {
				note += okStyle.Render("  (установлено)")
			}
			b.WriteString("  " + cursor + clip(tag+note, m.width-4) + "\n")
		}
		b.WriteString(moreDown(below))
		b.WriteString(legend(m.width, keys))

	case updWorking:
		b.WriteString("  " + m.spinner.View() + " " + dimStyle.Render(m.status) + "\n")
		b.WriteString(legend(m.width, "  подождите…"))

	case updDone:
		if m.err != nil {
			b.WriteString("  " + errStyle.Render("✖ "+m.err.Error()) + "\n")
		} else {
			b.WriteString("  " + okStyle.Render("✔ Готово") + "\n")
		}
		b.WriteString(legend(m.width, "  любая клавиша назад в меню · ← выход"))
	}

	return b.String()
}
