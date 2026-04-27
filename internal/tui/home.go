// Package tui contains Bubble Tea models for jr's interactive views.
package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/hugs7/jira-cli/internal/api"
	"github.com/hugs7/jira-cli/internal/cache"
	"github.com/hugs7/jira-cli/internal/config"
)

// boardsCacheKey / boardsCacheTTL are shared by the home model and
// any future code that wants to share the same on-disk cache.
const (
	boardsCacheKey = "boards"
	boardsCacheTTL = 1 * time.Hour
)

// HomeAction tells the caller what the user picked from the dashboard.
//   - Kind="issue", Key=PROJ-123    → open the issue viewer.
//   - Kind="board", BoardID=42      → open the Kanban board viewer.
//
// nil means the user quit cleanly.
type HomeAction struct {
	Kind    string
	Key     string
	BoardID int
}

// HomeState lets the caller persist tab / cursor / search across
// re-entries so that closing a sub-TUI drops you back where you were.
type HomeState struct {
	Tab          homeTab
	Cursor       int
	JQL          string
	BoardCursor  int
	BoardFilter  string
}

// homeTab is one top-level tab on the dashboard. Mirrors the pattern
// used by bb's home model: keep the tabs few and high-signal so users
// can flip through them with Tab/Shift+Tab.
type homeTab int

const (
	tabDashboard homeTab = iota
	tabBoards
)

var allTabs = []homeTab{tabDashboard, tabBoards}

func (t homeTab) name() string {
	switch t {
	case tabDashboard:
		return "Dashboard"
	case tabBoards:
		return "Boards"
	}
	return "?"
}

// Home runs the dashboard model and returns whatever action the user
// requested (or nil if they quit).
func Home(svc api.Service, prev *HomeState) (*HomeAction, *HomeState, error) {
	m := newHomeModel(svc, prev)
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion())
	final, err := p.Run()
	if err != nil {
		return nil, nil, err
	}
	hm, ok := final.(homeModel)
	if !ok {
		return nil, nil, nil
	}
	state := &HomeState{
		Tab:         hm.tab,
		Cursor:      hm.cursor,
		JQL:         hm.jqlInput.Value(),
		BoardCursor: hm.boardCursor,
		BoardFilter: hm.boardsFilter.Value(),
	}
	return hm.action, state, nil
}

// ---------- model ----------

type homeSection struct {
	title  string
	chip   string  // e.g. "12" count, "loading…", "no issues"
	issues []api.Issue
	loaded bool
	jql    string // JQL preset that powered this section (for "open in JQL" later)
}

type homeModel struct {
	svc api.Service

	tab homeTab

	// --- Dashboard tab ---
	sections []homeSection
	recent   []config.RecentIssue
	cursor   int  // section-flat row index across all visible rows
	jqlFocus bool // user is editing the JQL input
	jqlInput textinput.Model

	// --- Boards tab ---
	boards         []api.Board
	boardsFiltered []api.Board // re-derived after every filter change
	boardsLoaded   bool
	boardsLoading  bool
	boardsCacheAge time.Duration // 0 → fresh from server
	boardCursor    int
	boardsFilter   textinput.Model
	boardsFocus    bool // typing in the boards filter

	width, height int

	vp      viewport.Model
	spinner spinner.Model
	help    help.Model
	keys    homeKeys

	loading int
	status  string
	action  *HomeAction
}

func newHomeModel(svc api.Service, prev *HomeState) homeModel {
	// Apply the user's persisted theme up-front so every style we
	// initialise below already reflects the chosen palette.
	initTheme()

	ti := textinput.New()
	ti.Prompt = "JQL › "
	ti.Placeholder = "assignee = currentUser() AND resolution = Unresolved"
	ti.CharLimit = 0

	bf := textinput.New()
	bf.Prompt = "filter › "
	bf.Placeholder = "name / project key — leave empty for all"
	bf.CharLimit = 0

	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("13"))

	m := homeModel{
		svc:          svc,
		jqlInput:     ti,
		boardsFilter: bf,
		vp:           viewport.New(0, 0),
		spinner:      sp,
		help:         help.New(),
		keys:         defaultHomeKeys(),
		recent:       config.Get().Recent,
		sections: []homeSection{
			{title: "Assigned to me", jql: "assignee = currentUser() AND resolution = Unresolved ORDER BY updated DESC"},
			{title: "Mentions", jql: "text ~ currentUser() AND resolution = Unresolved ORDER BY updated DESC"},
			{title: "Watching", jql: "watcher = currentUser() AND resolution = Unresolved ORDER BY updated DESC"},
			{title: "Current sprint", jql: "assignee = currentUser() AND sprint in openSprints() ORDER BY rank"},
		},
		loading: 4,
	}
	if prev != nil {
		m.tab = prev.Tab
		m.cursor = prev.Cursor
		m.boardCursor = prev.BoardCursor
		if prev.JQL != "" {
			m.jqlInput.SetValue(prev.JQL)
		}
		if prev.BoardFilter != "" {
			m.boardsFilter.SetValue(prev.BoardFilter)
		}
	}
	return m
}

func (m homeModel) Init() tea.Cmd {
	return tea.Batch(
		m.spinner.Tick,
		m.fetchSection(0, m.svc.ListMyAssigned),
		m.fetchSection(1, m.svc.ListMentioned),
		m.fetchSection(2, m.svc.ListWatching),
		m.fetchSection(3, m.svc.ListCurrentSprint),
	)
}

// ---------- async loaders ----------

type sectionLoadedMsg struct {
	idx    int
	issues []api.Issue
	err    error
}

type searchDoneMsg struct {
	issues []api.Issue
	err    error
}

type boardsLoadedMsg struct {
	boards   []api.Board
	cacheAge time.Duration // 0 when fetched fresh
	err      error
}

func (m *homeModel) fetchSection(idx int, loader func(int) ([]api.Issue, error)) tea.Cmd {
	return func() tea.Msg {
		issues, err := loader(25)
		return sectionLoadedMsg{idx: idx, issues: issues, err: err}
	}
}

func (m *homeModel) runSearch(jql string) tea.Cmd {
	return func() tea.Msg {
		issues, err := m.svc.SearchIssues(api.SearchInput{JQL: jql, MaxResults: 50})
		return searchDoneMsg{issues: issues, err: err}
	}
}

// fetchBoards consults the on-disk cache first; if the entry is fresh
// it returns instantly, otherwise it paginates through every board on
// the host and writes the result to the cache. forceRefresh skips the
// read-side check (used by the Boards-tab `r` keybind).
func (m *homeModel) fetchBoards(forceRefresh bool) tea.Cmd {
	svc := m.svc
	host := svc.Host()
	return func() tea.Msg {
		if !forceRefresh {
			var cached []api.Board
			if cache.Get(host, boardsCacheKey, boardsCacheTTL, &cached) {
				age, _ := cache.Age(host, boardsCacheKey)
				return boardsLoadedMsg{boards: cached, cacheAge: age}
			}
		} else {
			_ = cache.Invalidate(host, boardsCacheKey)
		}
		bs, err := svc.ListBoards("", "", 0) // 0 → fetch all pages
		if err == nil {
			_ = cache.Put(host, boardsCacheKey, bs)
		}
		return boardsLoadedMsg{boards: bs, err: err}
	}
}

// ---------- update ----------

func (m homeModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layout()
		m.vp.SetContent(m.renderBody())
		m.snapViewportToCursor()
		return m, nil

	case tea.MouseMsg:
		// Forward mouse events (notably the wheel) to the viewport
		// so the user can scroll the body with the trackpad / wheel.
		var cmd tea.Cmd
		m.vp, cmd = m.vp.Update(msg)
		return m, cmd

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		if m.loading > 0 {
			return m, cmd
		}
		return m, nil

	case sectionLoadedMsg:
		if msg.idx < len(m.sections) {
			s := &m.sections[msg.idx]
			s.issues = msg.issues
			s.loaded = true
			if msg.err != nil {
				s.chip = "err: " + truncErr(msg.err.Error())
			} else {
				s.chip = fmt.Sprintf("%d", len(msg.issues))
			}
		}
		if m.loading > 0 {
			m.loading--
		}
		m.vp.SetContent(m.renderBody())
		m.snapViewportToCursor()
		return m, nil

	case searchDoneMsg:
		m.loading = 0
		// Replace the first section with search results so users can
		// instantly browse them with the existing cursor logic.
		if msg.err != nil {
			m.status = "✗ search: " + msg.err.Error()
			return m, nil
		}
		m.sections = []homeSection{{
			title:  "Search results",
			chip:   fmt.Sprintf("%d", len(msg.issues)),
			issues: msg.issues,
			loaded: true,
		}}
		m.cursor = 0
		m.vp.SetContent(m.renderBody())
		m.vp.GotoTop()
		return m, nil

	case boardsLoadedMsg:
		m.boardsLoading = false
		m.boardsLoaded = true
		if msg.err != nil {
			m.status = "✗ boards: " + msg.err.Error()
			return m, nil
		}
		m.boards = msg.boards
		m.boardsCacheAge = msg.cacheAge
		m.applyBoardsFilter()
		m.vp.SetContent(m.renderBody())
		return m, nil

	case tea.KeyMsg:
		// Filter inputs grab the keystroke first so the user can
		// type freely. Esc unfocuses; Enter runs the search /
		// applies the filter.
		if m.jqlFocus {
			return m.updateJQLFocused(msg)
		}
		if m.boardsFocus {
			return m.updateBoardsFilterFocused(msg)
		}

		// Universal keys handled the same on every tab.
		switch {
		case key.Matches(msg, m.keys.Quit):
			return m, tea.Quit
		case key.Matches(msg, m.keys.Tab):
			return m.switchTab(homeTab((int(m.tab) + 1) % len(allTabs)))
		case key.Matches(msg, m.keys.ShiftTab):
			return m.switchTab(homeTab((int(m.tab) + len(allTabs) - 1) % len(allTabs)))
		case key.Matches(msg, m.keys.Dashboard):
			return m.switchTab(tabDashboard)
		case key.Matches(msg, m.keys.Boards):
			return m.switchTab(tabBoards)
		case key.Matches(msg, m.keys.Theme):
			name := cycleTheme()
			m.status = "theme: " + name
			return m, nil
		}

		// Tab-specific keys.
		switch m.tab {
		case tabDashboard:
			return m.updateDashboardKey(msg)
		case tabBoards:
			return m.updateBoardsKey(msg)
		}
	}
	return m, nil
}

// switchTab flips to the requested tab, lazy-loading boards on first
// open. Re-renders the body so the new tab is visible immediately.
func (m homeModel) switchTab(t homeTab) (tea.Model, tea.Cmd) {
	m.tab = t
	m.vp.SetContent(m.renderBody())
	m.vp.GotoTop()
	if t == tabBoards && !m.boardsLoaded && !m.boardsLoading {
		m.boardsLoading = true
		return m, tea.Batch(m.spinner.Tick, m.fetchBoards(false))
	}
	return m, nil
}

func (m homeModel) updateJQLFocused(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.jqlFocus = false
		m.jqlInput.Blur()
		return m, nil
	case "enter":
		jql := strings.TrimSpace(m.jqlInput.Value())
		if jql == "" {
			return m, nil
		}
		m.loading = 1
		m.jqlFocus = false
		m.jqlInput.Blur()
		return m, tea.Batch(m.spinner.Tick, m.runSearch(jql))
	}
	var cmd tea.Cmd
	m.jqlInput, cmd = m.jqlInput.Update(msg)
	return m, cmd
}

func (m homeModel) updateBoardsFilterFocused(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "enter":
		m.boardsFocus = false
		m.boardsFilter.Blur()
		m.applyBoardsFilter()
		m.boardCursor = 0
		m.vp.SetContent(m.renderBody())
		m.vp.GotoTop()
		return m, nil
	}
	var cmd tea.Cmd
	m.boardsFilter, cmd = m.boardsFilter.Update(msg)
	// Live-filter as the user types so the list narrows immediately.
	m.applyBoardsFilter()
	if m.boardCursor >= len(m.boardsFiltered) {
		m.boardCursor = 0
	}
	m.vp.SetContent(m.renderBody())
	return m, cmd
}

func (m homeModel) updateDashboardKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.Up):
		m.moveCursor(-1)
		m.vp.SetContent(m.renderBody())
		m.snapViewportToCursor()
	case key.Matches(msg, m.keys.Down):
		m.moveCursor(1)
		m.vp.SetContent(m.renderBody())
		m.snapViewportToCursor()
	case key.Matches(msg, m.keys.PageUp):
		m.vp.ViewUp()
	case key.Matches(msg, m.keys.PageDown):
		m.vp.ViewDown()
	case key.Matches(msg, m.keys.Top):
		m.cursor = 0
		m.vp.SetContent(m.renderBody())
		m.vp.GotoTop()
	case key.Matches(msg, m.keys.Bottom):
		if n := m.cursorRowCount(); n > 0 {
			m.cursor = n - 1
		}
		m.vp.SetContent(m.renderBody())
		m.vp.GotoBottom()
	case key.Matches(msg, m.keys.HalfDown):
		m.vp.HalfViewDown()
	case key.Matches(msg, m.keys.HalfUp):
		m.vp.HalfViewUp()
	case key.Matches(msg, m.keys.JQL):
		m.jqlFocus = true
		return m, m.jqlInput.Focus()
	case key.Matches(msg, m.keys.Refresh):
		m.loading = len(m.sections)
		cmds := []tea.Cmd{m.spinner.Tick}
		loaders := []func(int) ([]api.Issue, error){
			m.svc.ListMyAssigned, m.svc.ListMentioned,
			m.svc.ListWatching, m.svc.ListCurrentSprint,
		}
		for i := range m.sections {
			if i < len(loaders) {
				m.sections[i].loaded = false
				m.sections[i].chip = "loading…"
				cmds = append(cmds, m.fetchSection(i, loaders[i]))
			}
		}
		return m, tea.Batch(cmds...)
	case key.Matches(msg, m.keys.Open):
		if iss, ok := m.issueAtCursor(); ok {
			_ = openInBrowser(iss.WebURL)
		}
	case key.Matches(msg, m.keys.Enter):
		if iss, ok := m.issueAtCursor(); ok {
			_ = config.AddRecent(m.svc.Host(), iss.Key, iss.Summary)
			m.action = &HomeAction{Kind: "issue", Key: iss.Key}
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m homeModel) updateBoardsKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.Up):
		if m.boardCursor > 0 {
			m.boardCursor--
		}
		m.vp.SetContent(m.renderBody())
	case key.Matches(msg, m.keys.Down):
		if m.boardCursor < len(m.boardsFiltered)-1 {
			m.boardCursor++
		}
		m.vp.SetContent(m.renderBody())
	case key.Matches(msg, m.keys.Top):
		m.boardCursor = 0
		m.vp.SetContent(m.renderBody())
		m.vp.GotoTop()
	case key.Matches(msg, m.keys.Bottom):
		if n := len(m.boardsFiltered); n > 0 {
			m.boardCursor = n - 1
		}
		m.vp.SetContent(m.renderBody())
		m.vp.GotoBottom()
	case key.Matches(msg, m.keys.PageDown):
		m.vp.ViewDown()
	case key.Matches(msg, m.keys.PageUp):
		m.vp.ViewUp()
	case key.Matches(msg, m.keys.HalfDown):
		m.vp.HalfViewDown()
	case key.Matches(msg, m.keys.HalfUp):
		m.vp.HalfViewUp()
	case key.Matches(msg, m.keys.JQL):
		// '/' opens the boards filter when on the Boards tab.
		m.boardsFocus = true
		return m, m.boardsFilter.Focus()
	case key.Matches(msg, m.keys.Refresh):
		m.boardsLoading = true
		m.boardsLoaded = false
		return m, tea.Batch(m.spinner.Tick, m.fetchBoards(true))
	case key.Matches(msg, m.keys.Enter):
		if b, ok := m.boardAtCursor(); ok {
			m.action = &HomeAction{Kind: "board", BoardID: b.ID}
			return m, tea.Quit
		}
	}
	return m, nil
}

// applyBoardsFilter recomputes boardsFiltered using the textinput
// value. Filter terms are case-insensitive substrings checked
// against project key, board type and name.
func (m *homeModel) applyBoardsFilter() {
	q := strings.ToLower(strings.TrimSpace(m.boardsFilter.Value()))
	if q == "" {
		m.boardsFiltered = m.boards
		return
	}
	out := make([]api.Board, 0, len(m.boards))
	for _, b := range m.boards {
		hay := strings.ToLower(b.ProjectKey + " " + b.Type + " " + b.Name)
		if strings.Contains(hay, q) {
			out = append(out, b)
		}
	}
	m.boardsFiltered = out
}

func (m homeModel) boardAtCursor() (api.Board, bool) {
	if m.boardCursor < 0 || m.boardCursor >= len(m.boardsFiltered) {
		return api.Board{}, false
	}
	return m.boardsFiltered[m.boardCursor], true
}

// cursorLine returns the y-coordinate (in lines from the top of the
// rendered body) of the row currently under m.cursor. Returns -1
// when the cursor doesn't land on any row. The layout walked here
// must mirror renderBody exactly.
func (m homeModel) cursorLine() int {
	idx := 0
	line := 0
	for _, s := range m.sections {
		line++ // section title line
		if len(s.issues) == 0 {
			if s.loaded {
				line++ // "(no issues)" placeholder
			}
			line++ // blank trailing line
			continue
		}
		for range s.issues {
			if idx == m.cursor {
				return line
			}
			line++
			idx++
		}
		line++ // blank trailing line
	}
	if len(m.recent) > 0 {
		line++ // "Recently viewed" header
		for range m.recent {
			if idx == m.cursor {
				return line
			}
			line++
			idx++
		}
	}
	return -1
}

// snapViewportToCursor adjusts vp.YOffset so the row under the
// cursor is visible inside the body viewport.
func (m *homeModel) snapViewportToCursor() {
	line := m.cursorLine()
	if line < 0 || m.vp.Height == 0 {
		return
	}
	off := m.vp.YOffset
	if line < off {
		off = line
	} else if line >= off+m.vp.Height {
		off = line - m.vp.Height + 1
	}
	if off < 0 {
		off = 0
	}
	m.vp.SetYOffset(off)
}

func (m *homeModel) moveCursor(delta int) {
	total := m.cursorRowCount()
	if total == 0 {
		m.cursor = 0
		return
	}
	m.cursor += delta
	if m.cursor < 0 {
		m.cursor = 0
	}
	if m.cursor >= total {
		m.cursor = total - 1
	}
}

// cursorRowCount returns how many issue rows the cursor can land on
// across all sections. Used to clamp navigation.
func (m homeModel) cursorRowCount() int {
	n := 0
	for _, s := range m.sections {
		n += len(s.issues)
	}
	return n + len(m.recent)
}

// issueAtCursor resolves the cursor's flat index back to a concrete
// issue (synthesising one for a recent entry — Key is enough to open
// it).
func (m homeModel) issueAtCursor() (api.Issue, bool) {
	idx := m.cursor
	for _, s := range m.sections {
		if idx < len(s.issues) {
			return s.issues[idx], true
		}
		idx -= len(s.issues)
	}
	if idx < len(m.recent) {
		r := m.recent[idx]
		return api.Issue{Key: r.Key, Summary: r.Summary}, true
	}
	return api.Issue{}, false
}

// ---------- view ----------

func (m *homeModel) layout() {
	headerH := 5 // title + tab strip + sub-bar + spacing
	footerH := 1
	w := m.width
	if w < 30 {
		w = 30
	}
	bodyH := m.height - headerH - footerH
	if bodyH < 5 {
		bodyH = 5
	}
	m.vp.Width = w
	m.vp.Height = bodyH
	m.jqlInput.Width = w - 8
	m.boardsFilter.Width = w - 12
}

// tab strip styles — the active tab gets the indigo badge, others a
// dim muted look. Mirrors bb's titleBadge / paneMutedStyle pairing.
var (
	tabActiveStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("231")).Background(lipgloss.Color("57")).Padding(0, 1)
	tabInactiveStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Padding(0, 1)
)

func (m homeModel) renderTabStrip() string {
	parts := make([]string, 0, len(allTabs))
	for _, t := range allTabs {
		label := fmt.Sprintf("[%s] %s", tabHotkey(t), t.name())
		if t == m.tab {
			parts = append(parts, tabActiveStyle.Render(label))
		} else {
			parts = append(parts, tabInactiveStyle.Render(label))
		}
	}
	hint := paneMutedStyle.Render("  · tab/⇧tab to switch")
	return strings.Join(parts, " ") + hint
}

// tabHotkey returns the single-letter shortcut for a tab.
func tabHotkey(t homeTab) string {
	switch t {
	case tabDashboard:
		return "d"
	case tabBoards:
		return "b"
	}
	return "?"
}

func (m homeModel) View() string {
	header := titleBar("JIRA · "+m.svc.Host(), titleChip.Render(m.svc.Me()))
	tabs := m.renderTabStrip()
	subBar := m.renderSubBar()

	help := m.help.View(m.keys)
	status := ""
	if m.loading > 0 || m.boardsLoading {
		status = paneMutedStyle.Render(m.spinner.View() + " loading…")
	} else if m.status != "" {
		status = paneMutedStyle.Render(m.status)
	}

	body := m.vp.View()
	footer := help
	if status != "" {
		footer = status + "   " + footer
	}
	return strings.Join([]string{header, tabs, subBar, "", body, footer}, "\n")
}

// renderSubBar renders the per-tab control row that sits between the
// tab strip and the body — the JQL prompt for Dashboard, the board
// filter for Boards.
func (m homeModel) renderSubBar() string {
	switch m.tab {
	case tabDashboard:
		if m.jqlFocus {
			return m.jqlInput.View()
		}
		if v := m.jqlInput.Value(); v != "" {
			return paneMutedStyle.Render("filter: " + v + "   (/ to edit, ctrl-u to clear)")
		}
		return paneMutedStyle.Render("type / to enter JQL · enter to run · esc to cancel")
	case tabBoards:
		if m.boardsFocus {
			return m.boardsFilter.View()
		}
		count := paneMutedStyle.Render(fmt.Sprintf("%d / %d boards", len(m.boardsFiltered), len(m.boards)))
		freshness := ""
		if m.boardsCacheAge > 0 {
			freshness = paneMutedStyle.Render(" · cached " + humanDuration(m.boardsCacheAge) + " ago")
		} else if m.boardsLoaded {
			freshness = paneMutedStyle.Render(" · fresh")
		}
		hint := paneMutedStyle.Render("   /  filter · enter open · r refresh")
		if v := m.boardsFilter.Value(); v != "" {
			return paneMutedStyle.Render("filter: "+v+"   ") + count + freshness + hint
		}
		return count + freshness + hint
	}
	return ""
}

// renderBody dispatches to the right per-tab renderer.
func (m homeModel) renderBody() string {
	switch m.tab {
	case tabDashboard:
		return m.renderDashboard()
	case tabBoards:
		return m.renderBoards()
	}
	return ""
}

// renderDashboard composes the section list with the current cursor.
func (m homeModel) renderDashboard() string {
	var b strings.Builder
	idx := 0
	for _, s := range m.sections {
		chip := s.chip
		if !s.loaded {
			chip = "loading…"
		}
		b.WriteString(paneTitleStyle.Render(s.title))
		b.WriteString(" ")
		b.WriteString(paneChipStyle.Render(chip))
		b.WriteByte('\n')
		if len(s.issues) == 0 {
			if s.loaded {
				b.WriteString("  " + paneMutedStyle.Render("(no issues)") + "\n")
			}
			b.WriteByte('\n')
			continue
		}
		for _, iss := range s.issues {
			b.WriteString(renderIssueRow(iss, idx == m.cursor))
			b.WriteByte('\n')
			idx++
		}
		b.WriteByte('\n')
	}
	if len(m.recent) > 0 {
		b.WriteString(paneTitleStyle.Render("Recently viewed"))
		b.WriteString(" ")
		b.WriteString(paneChipStyle.Render(fmt.Sprintf("%d", len(m.recent))))
		b.WriteByte('\n')
		for _, r := range m.recent {
			selected := idx == m.cursor
			b.WriteString(renderIssueRow(api.Issue{Key: r.Key, Summary: r.Summary}, selected))
			b.WriteByte('\n')
			idx++
		}
	}
	return b.String()
}

// renderBoards lists every board (post-filter) with the current
// cursor mark. Empty / loading states get their own card.
func (m homeModel) renderBoards() string {
	if !m.boardsLoaded && !m.boardsLoading {
		return paneMutedStyle.Render("press enter or wait — boards will load automatically")
	}
	if m.boardsLoading && len(m.boards) == 0 {
		return paneMutedStyle.Render("loading boards…")
	}
	if len(m.boards) == 0 {
		return paneMutedStyle.Render("no boards available — is the Jira Agile / Software addon installed?")
	}
	if len(m.boardsFiltered) == 0 {
		return paneMutedStyle.Render(fmt.Sprintf("no boards match filter %q", m.boardsFilter.Value()))
	}

	var b strings.Builder
	idStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	typeStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
	projStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("141"))
	selStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("15"))
	pointerSel := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("11")).Render("▶ ")
	for i, brd := range m.boardsFiltered {
		pointer := "  "
		nameStyle := lipgloss.NewStyle()
		if i == m.boardCursor {
			pointer = pointerSel
			nameStyle = selStyle
		}
		proj := brd.ProjectKey
		if proj == "" {
			proj = "—"
		}
		fmt.Fprintf(&b, "%s%s  %s  %s  %s\n",
			pointer,
			idStyle.Render(padRight(fmt.Sprintf("#%d", brd.ID), 7)),
			typeStyle.Render(padRight(brd.Type, 7)),
			projStyle.Render(padRight(proj, 10)),
			nameStyle.Render(brd.Name),
		)
	}
	return b.String()
}

func renderIssueRow(iss api.Issue, selected bool) string {
	keyW := 12
	statusW := 14
	key := padRight(iss.Key, keyW)
	status := padRight(iss.Status, statusW)
	summary := iss.Summary

	pointer := "  "
	style := lipgloss.NewStyle()
	if selected {
		pointer = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("11")).Render("▶ ")
		style = style.Bold(true).Foreground(lipgloss.Color("15"))
	}
	keyStyled := lipgloss.NewStyle().Foreground(lipgloss.Color("12")).Render(key)
	statusStyled := styleStatus(iss.StatusCat).Render(status)
	return pointer + keyStyled + " " + statusStyled + " " + style.Render(summary)
}

func padRight(s string, w int) string {
	if len(s) >= w {
		return s[:w]
	}
	return s + strings.Repeat(" ", w-len(s))
}

// humanDuration prints a Duration in coarse units suitable for a UI
// hint ("3m", "2h", "5d") — Time-since-cache age rather than precise
// elapsed time.
func humanDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

func truncErr(s string) string {
	if len(s) > 60 {
		return s[:57] + "…"
	}
	return s
}

// ---------- key map ----------

type homeKeys struct {
	Up, Down, PageUp, PageDown key.Binding
	HalfUp, HalfDown           key.Binding
	Top, Bottom                key.Binding
	Enter, Open                key.Binding
	Tab, ShiftTab              key.Binding
	Dashboard, Boards          key.Binding
	Refresh, JQL               key.Binding
	Theme                      key.Binding
	Help, Quit                 key.Binding
}

func defaultHomeKeys() homeKeys {
	return homeKeys{
		Up:        key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "up")),
		Down:      key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "down")),
		PageUp:    key.NewBinding(key.WithKeys("pgup"), key.WithHelp("pgup", "page up")),
		PageDown:  key.NewBinding(key.WithKeys("pgdown", " "), key.WithHelp("pgdn", "page down")),
		HalfUp:    key.NewBinding(key.WithKeys("ctrl+u"), key.WithHelp("^u", "½ page up")),
		HalfDown:  key.NewBinding(key.WithKeys("ctrl+d"), key.WithHelp("^d", "½ page down")),
		Top:       key.NewBinding(key.WithKeys("g", "home"), key.WithHelp("g", "top")),
		Bottom:    key.NewBinding(key.WithKeys("G", "end"), key.WithHelp("G", "bottom")),
		Enter:     key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "open")),
		Open:      key.NewBinding(key.WithKeys("o"), key.WithHelp("o", "browser")),
		Tab:       key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "next tab")),
		ShiftTab:  key.NewBinding(key.WithKeys("shift+tab"), key.WithHelp("⇧tab", "prev tab")),
		Dashboard: key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "Dashboard")),
		Boards:    key.NewBinding(key.WithKeys("b"), key.WithHelp("b", "Boards")),
		Refresh:   key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "refresh")),
		JQL:       key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "search")),
		Theme:     key.NewBinding(key.WithKeys("T"), key.WithHelp("T", "cycle theme")),
		Help:      key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
		Quit:      key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit")),
	}
}

func (k homeKeys) ShortHelp() []key.Binding {
	return []key.Binding{k.Up, k.Down, k.Enter, k.Tab, k.Dashboard, k.Boards, k.JQL, k.Refresh, k.Quit}
}
func (k homeKeys) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.Up, k.Down, k.PageUp, k.PageDown, k.HalfUp, k.HalfDown, k.Top, k.Bottom},
		{k.Tab, k.ShiftTab, k.Dashboard, k.Boards, k.JQL, k.Refresh, k.Theme},
		{k.Enter, k.Open, k.Help, k.Quit},
	}
}
