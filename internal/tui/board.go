// Package tui — Kanban board model.
//
// Renders a Jira Software / Agile board as a horizontal strip of
// columns, each holding the issue cards whose status maps to that
// column. Navigation is h/l between columns and j/k within a column;
// enter opens the issue in the standard issue viewer.
package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/hugs7/jira-cli/internal/api"
	"github.com/hugs7/jira-cli/internal/config"
)

// BoardAction is what Board returns to its caller. Kind="issue" with
// Key set means the user chose to drill into an issue. nil means the
// user quit cleanly.
type BoardAction struct {
	Kind string
	Key  string
}

// Board runs the Kanban TUI for a single board ID. The active sprint
// is auto-selected when the board is a Scrum board with one open;
// otherwise the board-wide view is shown.
func Board(svc api.Service, boardID int) (*BoardAction, error) {
	m := newBoardModel(svc, boardID)
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion())
	final, err := p.Run()
	if err != nil {
		return nil, err
	}
	if bm, ok := final.(boardModel); ok {
		return bm.action, nil
	}
	return nil, nil
}

// ---------- model ----------

type boardModel struct {
	svc     api.Service
	boardID int

	cfg     *api.BoardConfig
	sprints []api.Sprint
	sprint  int           // 0 = board-wide, otherwise sprint ID
	issues  []api.Issue   // raw issue list (re-grouped on every render)
	grouped [][]api.Issue // grouped[colIdx]

	width, height int
	colCursor     int
	rowCursor     int
	colOffset     int // index of leftmost visible column for horizontal scroll

	loading int // count of in-flight loads
	err     error
	spinner spinner.Model
	help    help.Model
	keys    boardKeys
	action  *BoardAction

	vp        viewport.Model // body scroll for tall columns
	headerRow string         // sticky column-header strip rendered above vp
}

func newBoardModel(svc api.Service, boardID int) boardModel {
	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("13"))
	return boardModel{
		svc:     svc,
		boardID: boardID,
		spinner: sp,
		help:    help.New(),
		keys:    defaultBoardKeys(),
		loading: 2, // config + sprints
		vp:      viewport.New(0, 0),
	}
}

func (m boardModel) Init() tea.Cmd {
	return tea.Batch(m.spinner.Tick, m.fetchConfig(), m.fetchSprints())
}

// ---------- async loaders ----------

type boardConfigMsg struct {
	cfg *api.BoardConfig
	err error
}
type boardIssuesMsg struct {
	issues []api.Issue
	err    error
}
type boardSprintsMsg struct {
	sprints []api.Sprint
	err     error
}

func (m *boardModel) fetchConfig() tea.Cmd {
	id := m.boardID
	svc := m.svc
	return func() tea.Msg {
		c, err := svc.GetBoardConfig(id)
		return boardConfigMsg{cfg: c, err: err}
	}
}

func (m *boardModel) fetchSprints() tea.Cmd {
	id := m.boardID
	svc := m.svc
	return func() tea.Msg {
		sp, err := svc.ListBoardSprints(id, "active,future")
		return boardSprintsMsg{sprints: sp, err: err}
	}
}

func (m *boardModel) fetchIssues() tea.Cmd {
	id, sp := m.boardID, m.sprint
	svc := m.svc
	return func() tea.Msg {
		issues, err := svc.ListBoardIssues(id, sp, "", 200)
		return boardIssuesMsg{issues: issues, err: err}
	}
}

// ---------- update ----------

func (m boardModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layout()
		m.composeBody()
		return m, nil

	case tea.MouseMsg:
		// Wheel scrolls the body without disturbing the cursor.
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

	case boardConfigMsg:
		if msg.err != nil {
			m.err = msg.err
			m.loading = 0
			return m, nil
		}
		m.cfg = msg.cfg
		if m.loading > 0 {
			m.loading--
		}
		m.composeBody()
		// Now that we know the columns, kick off issues load too.
		m.loading++
		return m, m.fetchIssues()

	case boardSprintsMsg:
		m.sprints = msg.sprints
		// Auto-pick the active sprint if exactly one is open.
		for _, s := range msg.sprints {
			if strings.EqualFold(s.State, "active") {
				m.sprint = s.ID
				break
			}
		}
		if m.loading > 0 {
			m.loading--
		}
		return m, nil

	case boardIssuesMsg:
		m.issues = msg.issues
		m.regroup()
		if m.loading > 0 {
			m.loading--
		}
		m.clampCursor()
		m.composeBody()
		m.snapVP()
		return m, nil

	case tea.KeyMsg:
		switch {
		case key.Matches(msg, m.keys.Quit):
			return m, tea.Quit
		case key.Matches(msg, m.keys.Left):
			m.moveCol(-1)
			m.composeBody()
			m.snapVP()
		case key.Matches(msg, m.keys.Right):
			m.moveCol(1)
			m.composeBody()
			m.snapVP()
		case key.Matches(msg, m.keys.Up):
			m.moveRow(-1)
			m.composeBody()
			m.snapVP()
		case key.Matches(msg, m.keys.Down):
			m.moveRow(1)
			m.composeBody()
			m.snapVP()
		case key.Matches(msg, m.keys.Top):
			m.rowCursor = 0
			m.composeBody()
			m.vp.GotoTop()
		case key.Matches(msg, m.keys.Bottom):
			if n := len(m.currentColIssues()); n > 0 {
				m.rowCursor = n - 1
			}
			m.composeBody()
			m.snapVP()
		case key.Matches(msg, m.keys.Refresh):
			m.loading = 1
			return m, tea.Batch(m.spinner.Tick, m.fetchIssues())
		case key.Matches(msg, m.keys.Sprint):
			m.cycleSprint()
			m.loading = 1
			return m, tea.Batch(m.spinner.Tick, m.fetchIssues())
		case key.Matches(msg, m.keys.Open):
			if iss, ok := m.cardAtCursor(); ok {
				_ = openInBrowser(iss.WebURL)
			}
		case key.Matches(msg, m.keys.Enter):
			if iss, ok := m.cardAtCursor(); ok {
				_ = config.AddRecent(m.svc.Host(), iss.Key, iss.Summary)
				m.action = &BoardAction{Kind: "issue", Key: iss.Key}
				return m, tea.Quit
			}
		}
		return m, nil
	}
	return m, nil
}

// regroup buckets the current issue set into columns by status name.
// Issues whose status doesn't map to any column land in a synthetic
// "Other" column appended to the end so they're still discoverable.
func (m *boardModel) regroup() {
	if m.cfg == nil {
		m.grouped = nil
		return
	}
	cols := len(m.cfg.Columns)
	m.grouped = make([][]api.Issue, cols)
	var other []api.Issue
	for _, iss := range m.issues {
		st := strings.ToLower(iss.Status)
		placed := false
		for i, c := range m.cfg.Columns {
			for _, k := range c.StatusKeys {
				if k == st {
					m.grouped[i] = append(m.grouped[i], iss)
					placed = true
					break
				}
			}
			if placed {
				break
			}
		}
		if !placed {
			other = append(other, iss)
		}
	}
	if len(other) > 0 {
		m.grouped = append(m.grouped, other)
	}
}

func (m *boardModel) currentColIssues() []api.Issue {
	if m.colCursor < 0 || m.colCursor >= len(m.grouped) {
		return nil
	}
	return m.grouped[m.colCursor]
}

func (m *boardModel) cardAtCursor() (api.Issue, bool) {
	col := m.currentColIssues()
	if m.rowCursor < 0 || m.rowCursor >= len(col) {
		return api.Issue{}, false
	}
	return col[m.rowCursor], true
}

func (m *boardModel) moveCol(d int) {
	if len(m.grouped) == 0 {
		return
	}
	m.colCursor += d
	if m.colCursor < 0 {
		m.colCursor = 0
	}
	if m.colCursor >= len(m.grouped) {
		m.colCursor = len(m.grouped) - 1
	}
	m.rowCursor = 0
	m.snapColOffset()
}

func (m *boardModel) moveRow(d int) {
	col := m.currentColIssues()
	if len(col) == 0 {
		return
	}
	m.rowCursor += d
	if m.rowCursor < 0 {
		m.rowCursor = 0
	}
	if m.rowCursor >= len(col) {
		m.rowCursor = len(col) - 1
	}
}

func (m *boardModel) clampCursor() {
	if m.colCursor >= len(m.grouped) {
		m.colCursor = len(m.grouped) - 1
	}
	if m.colCursor < 0 {
		m.colCursor = 0
	}
	if col := m.currentColIssues(); m.rowCursor >= len(col) {
		m.rowCursor = len(col) - 1
		if m.rowCursor < 0 {
			m.rowCursor = 0
		}
	}
}

// snapColOffset shifts colOffset so colCursor stays within the
// visibleColumns() window.
func (m *boardModel) snapColOffset() {
	visible := m.visibleColumns()
	if visible <= 0 {
		return
	}
	if m.colCursor < m.colOffset {
		m.colOffset = m.colCursor
	} else if m.colCursor >= m.colOffset+visible {
		m.colOffset = m.colCursor - visible + 1
	}
	if m.colOffset < 0 {
		m.colOffset = 0
	}
}

// cycleSprint rotates through (no sprint) → active → future → ...
func (m *boardModel) cycleSprint() {
	all := append([]api.Sprint{{ID: 0, Name: "All (board)", State: "—"}}, m.sprints...)
	cur := 0
	for i, s := range all {
		if s.ID == m.sprint {
			cur = i
			break
		}
	}
	cur = (cur + 1) % len(all)
	m.sprint = all[cur].ID
	m.colCursor = 0
	m.rowCursor = 0
	m.colOffset = 0
}

// ---------- view ----------

const (
	cardMinWidth = 22
	cardMaxWidth = 36
)

func (m *boardModel) visibleColumns() int {
	if m.width <= 0 {
		return 1
	}
	w := cardMinWidth + 2 // border
	v := m.width / w
	if v < 1 {
		v = 1
	}
	if v > len(m.grouped) {
		v = len(m.grouped)
	}
	return v
}

func (m *boardModel) cardWidth() int {
	visible := m.visibleColumns()
	if visible <= 0 {
		return cardMinWidth
	}
	w := (m.width - 2*visible) / visible
	if w < cardMinWidth {
		w = cardMinWidth
	}
	if w > cardMaxWidth {
		w = cardMaxWidth
	}
	return w
}

var (
	// Padding(0, 2) so header text starts at the same column as the
	// card body (1 char card border + 1 char card padding = 2).
	colHeaderStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("231")).
			Background(lipgloss.Color("57")).Padding(0, 2)
	colHeaderDim = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("245")).
			Padding(0, 2)
	cardBorder = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("238")).Padding(0, 1).Margin(0, 0, 1, 0)
	cardSel = lipgloss.NewStyle().Border(lipgloss.ThickBorder()).
			BorderForeground(lipgloss.Color("11")).Padding(0, 1).Margin(0, 0, 1, 0)
)

func (m boardModel) View() string {
	if m.err != nil {
		return statusErr.Render("✗ "+m.err.Error()) + "\n\n" + paneMutedStyle.Render("press q to go back")
	}
	if m.cfg == nil {
		return paneMutedStyle.Render(m.spinner.View() + " loading board…")
	}

	// --- title bar ---
	chips := []string{}
	if m.cfg.Board.ProjectKey != "" {
		chips = append(chips, titleChip.Render(m.cfg.Board.ProjectKey))
	}
	if m.sprint > 0 {
		for _, s := range m.sprints {
			if s.ID == m.sprint {
				chips = append(chips, titleChipWarn.Render("sprint: "+s.Name))
				break
			}
		}
	} else {
		chips = append(chips, titleChipDim.Render("all (no sprint filter)"))
	}
	if len(m.issues) > 0 {
		chips = append(chips, titleChip.Render(fmt.Sprintf("%d issues", len(m.issues))))
	}
	if m.loading > 0 {
		chips = append(chips, paneMutedStyle.Render(m.spinner.View()+" loading"))
	}
	header := titleBar("BOARD · "+m.cfg.Board.Name, chips...)

	// --- footer ---
	visible := m.visibleColumns()
	from := m.colOffset
	to := from + visible
	if to > len(m.grouped) {
		to = len(m.grouped)
	}
	scrollHint := ""
	if len(m.grouped) > visible {
		scrollHint = paneMutedStyle.Render(
			fmt.Sprintf("  cols %d-%d of %d (h/l to scroll)", from+1, to, len(m.grouped)))
	}
	help := m.help.View(m.keys)
	return strings.Join([]string{header, "", m.headerRow, m.vp.View(), help + scrollHint}, "\n")
}

// layout sizes the body viewport. The visible chrome is:
//   - title bar          (1 line)
//   - blank spacer       (1 line)
//   - sticky header row  (1 line)
//   - footer / help      (1 line)
func (m *boardModel) layout() {
	const chromeH = 4
	h := m.height - chromeH
	if h < 5 {
		h = 5
	}
	w := m.width
	if w < 30 {
		w = 30
	}
	m.vp.Width = w
	m.vp.Height = h
}

// composeBody recomputes the sticky header strip and the cards-only
// body, and pushes the body into the viewport. Called from every
// Update branch that mutates the visible state.
func (m *boardModel) composeBody() {
	if m.cfg == nil {
		return
	}
	visible := m.visibleColumns()
	cw := m.cardWidth()
	from := m.colOffset
	to := from + visible
	if to > len(m.grouped) {
		to = len(m.grouped)
	}

	headers := make([]string, 0, to-from)
	cards := make([]string, 0, to-from)
	for ci := from; ci < to; ci++ {
		headers = append(headers, m.renderColumnHeader(ci, cw))
		cards = append(cards, m.renderColumnCards(ci, cw))
	}
	m.headerRow = lipgloss.JoinHorizontal(lipgloss.Top, headers...)
	body := lipgloss.JoinHorizontal(lipgloss.Top, cards...)
	m.vp.SetContent(body)
}

// snapVP scrolls the viewport so the row holding the selected card
// is visible. Card heights are constant (cardRows) so the math is
// straightforward — no need to walk the rendered string. The header
// row is rendered above the viewport (sticky) and so doesn't enter
// the offset calculation.
func (m *boardModel) snapVP() {
	if m.vp.Height == 0 {
		return
	}
	top := m.rowCursor * cardRows
	bottom := top + cardRows
	off := m.vp.YOffset
	if top < off {
		off = top
	} else if bottom > off+m.vp.Height {
		off = bottom - m.vp.Height
	}
	if off < 0 {
		off = 0
	}
	m.vp.SetYOffset(off)
}

// summaryLines is the fixed line count we wrap card summaries to so
// every card has identical height — the only way columns line up
// horizontally when they share a single body string.
const summaryLines = 2

// cardRows is the rendered height of one card. 2 (border) + 1 (key)
// + summaryLines + 1 (meta) + 1 (margin). Used by snap-to-cursor.
const cardRows = 2 + 1 + summaryLines + 1 + 1

// renderColumnHeader returns just the column header line (sticky;
// rendered above the viewport so it doesn't scroll away). Padding is
// (0,2) so the header text starts at the same column as the card
// body content (1 char border + 1 char card padding).
func (m *boardModel) renderColumnHeader(idx, width int) string {
	colName := "Other"
	if idx < len(m.cfg.Columns) {
		colName = m.cfg.Columns[idx].Name
	}
	count := len(m.grouped[idx])

	headStyle := colHeaderDim
	if idx == m.colCursor {
		headStyle = colHeaderStyle
	}
	// Width(width) is the rendered cell width (incl. padding); the
	// content area inside is width - 2*padding = width - 4.
	headText := truncateRunes(fmt.Sprintf("%s · %d", colName, count), width-4)
	return headStyle.Width(width).Render(headText)
}

// renderColumnCards returns the cards for a column, no header. The
// header is sticky above the viewport.
func (m *boardModel) renderColumnCards(idx, width int) string {
	var b strings.Builder
	for ri, iss := range m.grouped[idx] {
		selected := idx == m.colCursor && ri == m.rowCursor
		b.WriteString(renderBoardCard(iss, width, selected))
	}
	return b.String()
}

func renderBoardCard(iss api.Issue, width int, selected bool) string {
	keyLine := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12")).Render(iss.Key)
	if iss.IssueType != "" {
		keyLine += " " + paneMutedStyle.Render("· "+iss.IssueType)
	}
	maxSum := width - 4
	if maxSum < 8 {
		maxSum = 8
	}
	summary := wrapPad(iss.Summary, maxSum, summaryLines)

	var meta []string
	if iss.Assignee != "" {
		meta = append(meta, "👤 "+iss.Assignee)
	}
	if iss.StoryPoints > 0 {
		meta = append(meta, fmt.Sprintf("⛶ %g", iss.StoryPoints))
	}
	// Always emit the meta line — empty when there's no metadata —
	// so every card is exactly the same height.
	metaLine := paneMutedStyle.Render(strings.Join(meta, "  "))

	body := keyLine + "\n" + summary + "\n" + metaLine

	style := cardBorder
	if selected {
		style = cardSel
	}
	return style.Width(width - 2).Render(body)
}

// truncateRunes shortens s to at most w display runes, appending an
// ellipsis when it had to cut. w<=0 returns the empty string.
func truncateRunes(s string, w int) string {
	if w <= 0 {
		return ""
	}
	rs := []rune(s)
	if len(rs) <= w {
		return s
	}
	if w == 1 {
		return "…"
	}
	return string(rs[:w-1]) + "…"
}

// wrapPad word-wraps s to lines of at most w runes, padded or
// truncated to exactly `lines` rows. The constant card height is
// what keeps the columns aligned in JoinHorizontal.
func wrapPad(s string, w, lines int) string {
	out := wrap(s, w, lines)
	parts := strings.Split(out, "\n")
	for len(parts) < lines {
		parts = append(parts, "")
	}
	if len(parts) > lines {
		parts = parts[:lines]
	}
	return strings.Join(parts, "\n")
}

// wrap word-wraps `s` into lines of at most `w` runes, capped at
// `maxLines`. Overflow is signalled with an ellipsis.
func wrap(s string, w, maxLines int) string {
	if w <= 0 {
		return s
	}
	words := strings.Fields(s)
	var lines []string
	cur := ""
	for _, word := range words {
		if cur == "" {
			cur = word
			continue
		}
		if len(cur)+1+len(word) <= w {
			cur += " " + word
			continue
		}
		lines = append(lines, cur)
		if len(lines) >= maxLines {
			break
		}
		cur = word
	}
	if cur != "" && len(lines) < maxLines {
		lines = append(lines, cur)
	}
	if len(lines) == 0 {
		return ""
	}
	last := lines[len(lines)-1]
	if len(last) > w {
		last = last[:w-1] + "…"
		lines[len(lines)-1] = last
	}
	// If we stopped early, mark the truncation.
	if len(words) > 0 && totalWords(lines) < len(words) {
		last = lines[len(lines)-1]
		if len(last) >= w {
			last = last[:w-1] + "…"
		} else {
			last = last + "…"
		}
		lines[len(lines)-1] = last
	}
	return strings.Join(lines, "\n")
}

func totalWords(lines []string) int {
	n := 0
	for _, l := range lines {
		n += len(strings.Fields(l))
	}
	return n
}

// ---------- key map ----------

type boardKeys struct {
	Up, Down, Left, Right key.Binding
	Top, Bottom           key.Binding
	Enter, Open           key.Binding
	Sprint, Refresh       key.Binding
	Help, Quit            key.Binding
}

func defaultBoardKeys() boardKeys {
	return boardKeys{
		Up:      key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "card up")),
		Down:    key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "card down")),
		Left:    key.NewBinding(key.WithKeys("left", "h"), key.WithHelp("←/h", "column left")),
		Right:   key.NewBinding(key.WithKeys("right", "l"), key.WithHelp("→/l", "column right")),
		Top:     key.NewBinding(key.WithKeys("g", "home"), key.WithHelp("g", "top")),
		Bottom:  key.NewBinding(key.WithKeys("G", "end"), key.WithHelp("G", "bottom")),
		Enter:   key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "open issue")),
		Open:    key.NewBinding(key.WithKeys("o"), key.WithHelp("o", "browser")),
		Sprint:  key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "cycle sprint")),
		Refresh: key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "refresh")),
		Help:    key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
		Quit:    key.NewBinding(key.WithKeys("q", "ctrl+c", "esc"), key.WithHelp("q", "back")),
	}
}

func (k boardKeys) ShortHelp() []key.Binding {
	return []key.Binding{k.Left, k.Right, k.Up, k.Down, k.Enter, k.Sprint, k.Refresh, k.Quit}
}
func (k boardKeys) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.Left, k.Right, k.Up, k.Down, k.Top, k.Bottom},
		{k.Enter, k.Open, k.Sprint, k.Refresh, k.Help, k.Quit},
	}
}
