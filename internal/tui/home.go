// Package tui contains Bubble Tea models for jr's interactive views.
package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/hugs7/jira-cli/internal/api"
	"github.com/hugs7/jira-cli/internal/config"
)

// HomeAction tells the caller what the user picked from the dashboard.
//   - Kind="issue", Key=PROJ-123 → open the issue viewer.
//   - Kind="boards"              → open the board picker (then board TUI).
//
// nil means the user quit cleanly.
type HomeAction struct {
	Kind string
	Key  string
}

// HomeState lets the caller persist cursor/jql across re-entries so
// that closing an issue viewer drops you back where you were.
type HomeState struct {
	Cursor int
	JQL    string
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
	state := &HomeState{Cursor: hm.cursor, JQL: hm.jqlInput.Value()}
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

	sections []homeSection
	recent   []config.RecentIssue

	width, height int
	cursor        int  // section-flat row index across all visible rows
	jqlFocus      bool // user is editing the JQL input

	jqlInput textinput.Model
	vp       viewport.Model
	spinner  spinner.Model
	help     help.Model
	keys     homeKeys

	loading int
	status  string
	action  *HomeAction
}

func newHomeModel(svc api.Service, prev *HomeState) homeModel {
	ti := textinput.New()
	ti.Prompt = "JQL › "
	ti.Placeholder = "assignee = currentUser() AND resolution = Unresolved"
	ti.CharLimit = 0
	if prev != nil && prev.JQL != "" {
		ti.SetValue(prev.JQL)
	}

	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("13"))

	cursor := 0
	if prev != nil {
		cursor = prev.Cursor
	}

	cfg := config.Get()
	return homeModel{
		svc:      svc,
		jqlInput: ti,
		vp:       viewport.New(0, 0),
		spinner:  sp,
		help:     help.New(),
		keys:     defaultHomeKeys(),
		cursor:   cursor,
		recent:   cfg.Recent,
		sections: []homeSection{
			{title: "Assigned to me", jql: "assignee = currentUser() AND resolution = Unresolved ORDER BY updated DESC"},
			{title: "Mentions", jql: "text ~ currentUser() AND resolution = Unresolved ORDER BY updated DESC"},
			{title: "Watching", jql: "watcher = currentUser() AND resolution = Unresolved ORDER BY updated DESC"},
			{title: "Current sprint", jql: "assignee = currentUser() AND sprint in openSprints() ORDER BY rank"},
		},
		loading: 4,
	}
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

	case tea.KeyMsg:
		// While the JQL input is focused, swallow most keys so the
		// user can type freely. Esc unfocuses; Enter runs the search.
		if m.jqlFocus {
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

		switch {
		case key.Matches(msg, m.keys.Quit):
			return m, tea.Quit
		case key.Matches(msg, m.keys.Up):
			m.moveCursor(-1)
			m.vp.SetContent(m.renderBody())
			m.snapViewportToCursor()
			return m, nil
		case key.Matches(msg, m.keys.Down):
			m.moveCursor(1)
			m.vp.SetContent(m.renderBody())
			m.snapViewportToCursor()
			return m, nil
		case key.Matches(msg, m.keys.PageUp):
			m.vp.ViewUp()
			return m, nil
		case key.Matches(msg, m.keys.PageDown):
			m.vp.ViewDown()
			return m, nil
		case key.Matches(msg, m.keys.Top):
			m.cursor = 0
			m.vp.SetContent(m.renderBody())
			m.vp.GotoTop()
			return m, nil
		case key.Matches(msg, m.keys.Bottom):
			if n := m.cursorRowCount(); n > 0 {
				m.cursor = n - 1
			}
			m.vp.SetContent(m.renderBody())
			m.vp.GotoBottom()
			return m, nil
		case key.Matches(msg, m.keys.HalfDown):
			m.vp.HalfViewDown()
			return m, nil
		case key.Matches(msg, m.keys.HalfUp):
			m.vp.HalfViewUp()
			return m, nil
		case key.Matches(msg, m.keys.JQL):
			m.jqlFocus = true
			return m, m.jqlInput.Focus()
		case key.Matches(msg, m.keys.Boards):
			m.action = &HomeAction{Kind: "boards"}
			return m, tea.Quit
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
			return m, nil
		case key.Matches(msg, m.keys.Enter):
			if iss, ok := m.issueAtCursor(); ok {
				_ = config.AddRecent(m.svc.Host(), iss.Key, iss.Summary)
				m.action = &HomeAction{Kind: "issue", Key: iss.Key}
				return m, tea.Quit
			}
			return m, nil
		}
	}
	return m, nil
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
	headerH := 4 // title + JQL bar + spacing
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
}

func (m homeModel) View() string {
	header := titleBar("JIRA · "+m.svc.Host(), titleChip.Render(m.svc.Me()))
	jql := m.jqlInput.View()
	if !m.jqlFocus {
		jql = paneMutedStyle.Render("type / to enter JQL · enter to run · esc to cancel")
		if v := m.jqlInput.Value(); v != "" {
			jql = paneMutedStyle.Render("filter: " + v + "   (/ to edit, ctrl-u to clear)")
		}
	}
	help := m.help.View(m.keys)
	status := ""
	if m.loading > 0 {
		status = paneMutedStyle.Render(m.spinner.View() + " loading…")
	} else if m.status != "" {
		status = paneMutedStyle.Render(m.status)
	}

	body := m.vp.View()
	footer := help
	if status != "" {
		footer = status + "   " + footer
	}
	return strings.Join([]string{header, jql, "", body, footer}, "\n")
}

// renderBody composes the section list with the current cursor mark.
func (m homeModel) renderBody() string {
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
	Boards                     key.Binding
	Refresh, JQL               key.Binding
	Help, Quit                 key.Binding
}

func defaultHomeKeys() homeKeys {
	return homeKeys{
		Up:       key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "up")),
		Down:     key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "down")),
		PageUp:   key.NewBinding(key.WithKeys("pgup", "b"), key.WithHelp("pgup", "page up")),
		PageDown: key.NewBinding(key.WithKeys("pgdown", " ", "f"), key.WithHelp("pgdn", "page down")),
		HalfUp:   key.NewBinding(key.WithKeys("ctrl+u"), key.WithHelp("^u", "½ page up")),
		HalfDown: key.NewBinding(key.WithKeys("ctrl+d"), key.WithHelp("^d", "½ page down")),
		Top:      key.NewBinding(key.WithKeys("g", "home"), key.WithHelp("g", "top")),
		Bottom:   key.NewBinding(key.WithKeys("G", "end"), key.WithHelp("G", "bottom")),
		Enter:    key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "open issue")),
		Open:     key.NewBinding(key.WithKeys("o"), key.WithHelp("o", "browser")),
		Boards:   key.NewBinding(key.WithKeys("b"), key.WithHelp("b", "boards")),
		Refresh:  key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "refresh")),
		JQL:      key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "JQL search")),
		Help:     key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
		Quit:     key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit")),
	}
}

func (k homeKeys) ShortHelp() []key.Binding {
	return []key.Binding{k.Up, k.Down, k.Enter, k.Open, k.Boards, k.JQL, k.Refresh, k.Quit}
}
func (k homeKeys) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.Up, k.Down, k.PageUp, k.PageDown, k.HalfUp, k.HalfDown, k.Top, k.Bottom},
		{k.Enter, k.Open, k.Boards, k.JQL, k.Refresh, k.Help, k.Quit},
	}
}
