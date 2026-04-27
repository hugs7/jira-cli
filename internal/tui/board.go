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
	status  string // transient one-line feedback for the help row

	// Initial-load gates: the first issues fetch waits for *both*
	// the board config (we need column→status mappings to render)
	// and the sprint list (so the fetch is sprint-scoped from the
	// start instead of pulling the whole board, then refetching).
	gotConfig, gotSprints, initialIssuesFired bool
	spinner spinner.Model
	help    help.Model
	keys    boardKeys
	action  *BoardAction

	// vim-style two-key prefix (`gg` → top). Set by the first `g`,
	// cleared by anything else / a 750ms timeout.
	pendingG bool

	vp        viewport.Model // body scroll for tall columns
	headerRow string         // sticky column-header strip rendered above vp

	// picker is the active modal overlay (currently used only by the
	// inline "create issue" flow). nil when no picker is open.
	picker *pickerModel

	// selected is the set of multi-selected issue keys (set by v/V).
	// Bulk actions like H/L drag operate on this set when non-empty,
	// otherwise on just the cursor card.
	selected map[string]bool

	// Quick filters applied client-side at regroup time. Filters
	// don't reduce the underlying m.issues list — they only narrow
	// what's bucketed into the visible columns, so toggling them
	// off restores the full board without an API round-trip.
	filterMine bool
	filterText string

	// previewOpen toggles the right-hand preview pane that shows
	// the focused card's metadata without leaving the board. Uses
	// only the data already on m.issues (no extra round-trip).
	previewOpen bool
}

func newBoardModel(svc api.Service, boardID int) boardModel {
	initTheme()

	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("13"))
	return boardModel{
		svc:      svc,
		boardID:  boardID,
		spinner:  sp,
		help:     help.New(),
		keys:     defaultBoardKeys(),
		loading:  2, // config + sprints
		vp:       viewport.New(0, 0),
		selected: map[string]bool{},
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

// cardMovedMsg is the result of a drag (H/L). label describes the
// attempted move ("FOO-1 → In Progress") and is shown in the
// status row regardless of outcome.
type cardMovedMsg struct {
	label string
	err   error
}

// issueCreatedMsg is the result of an inline-create (c). On success
// key is the new issue's key so the status row can show it; on
// error err is non-nil.
type issueCreatedMsg struct {
	key string
	err error
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

// moveCardCmd transitions one or more cards into a status that maps
// to the column `dir` away from the current one (-1 = left, +1 =
// right). When the multi-select set is non-empty it operates on
// every selected card; otherwise it falls back to the focused card.
// Each card's transition list is fetched independently because Jira
// returns workflow-relative transitions per issue.
func (m *boardModel) moveCardCmd(dir int) tea.Cmd {
	if m.cfg == nil {
		return nil
	}
	target := m.colCursor + dir
	if target < 0 || target >= len(m.cfg.Columns) {
		m.status = "edge column — can't move further"
		return nil
	}
	col := m.cfg.Columns[target]
	if len(col.StatusKeys) == 0 {
		m.status = fmt.Sprintf("column %q has no statuses mapped", col.Name)
		return nil
	}
	keys := m.bulkOrCursorKeys()
	if len(keys) == 0 {
		return nil
	}
	svc := m.svc
	colName := col.Name
	wanted := map[string]bool{}
	for _, k := range col.StatusKeys {
		wanted[strings.ToLower(k)] = true
	}
	var label string
	if len(keys) == 1 {
		label = fmt.Sprintf("%s → %s", keys[0], colName)
	} else {
		label = fmt.Sprintf("%d cards → %s", len(keys), colName)
	}

	// Optimistic local update: rewrite each moved issue's status
	// to the target column's first status key, regroup, and follow
	// the focused card to its new column position. This makes H/L
	// feel instant — the API round-trip happens in the background
	// and only refetches if it fails.
	targetStatus := col.StatusKeys[0]
	keySet := map[string]bool{}
	for _, k := range keys {
		keySet[k] = true
	}
	focusedKey := ""
	if iss, ok := m.cardAtCursor(); ok {
		focusedKey = iss.Key
	}
	for i := range m.issues {
		if keySet[m.issues[i].Key] {
			m.issues[i].Status = targetStatus
		}
	}
	m.regroup()
	if focusedKey != "" && keySet[focusedKey] {
		for ri, iss := range m.grouped[target] {
			if iss.Key == focusedKey {
				m.colCursor = target
				m.rowCursor = ri
				break
			}
		}
	}
	m.snapColOffset()
	m.composeBody()
	m.snapVP()
	m.loading++
	return func() tea.Msg {
		var firstErr error
		for _, key := range keys {
			ts, err := svc.ListTransitions(key)
			if err != nil {
				if firstErr == nil {
					firstErr = fmt.Errorf("%s: %w", key, err)
				}
				continue
			}
			var pickID string
			for _, t := range ts {
				if wanted[strings.ToLower(t.To)] {
					pickID = t.ID
					break
				}
			}
			if pickID == "" {
				if firstErr == nil {
					firstErr = fmt.Errorf("%s: no transition into %q", key, colName)
				}
				continue
			}
			if err := svc.DoTransition(key, pickID); err != nil {
				if firstErr == nil {
					firstErr = fmt.Errorf("%s: %w", key, err)
				}
			}
		}
		return cardMovedMsg{label: label, err: firstErr}
	}
}

// passesFilters returns true when an issue matches every active
// quick filter. Filters AND together: enabling both "mine" and a
// text query keeps only issues that satisfy both.
func (m *boardModel) passesFilters(iss api.Issue) bool {
	if m.filterMine {
		me := strings.ToLower(m.svc.Me())
		// Match either the stable login (AssigneeKey) or the
		// display name fallback so users whose configured `me`
		// happens to be the display name still get sensible
		// results.
		if me == "" || (strings.ToLower(iss.AssigneeKey) != me &&
			strings.ToLower(iss.Assignee) != me) {
			return false
		}
	}
	if m.filterText != "" {
		q := strings.ToLower(m.filterText)
		// Match against summary + key + assignee + labels — the
		// fields the user is most likely typing about.
		hay := strings.ToLower(iss.Summary + " " + iss.Key + " " + iss.Assignee + " " + strings.Join(iss.Labels, " "))
		if !strings.Contains(hay, q) {
			return false
		}
	}
	return true
}

// filterChips renders a compact summary of the currently-active
// filters for the title bar; "" when no filters are active.
func (m *boardModel) filterChips() string {
	parts := []string{}
	if m.filterMine {
		parts = append(parts, "mine")
	}
	if m.filterText != "" {
		parts = append(parts, fmt.Sprintf("/%s/", m.filterText))
	}
	if len(parts) == 0 {
		return ""
	}
	return "filter: " + strings.Join(parts, " + ")
}

// maybeFireInitialIssues kicks off the first issue fetch once both
// the board config and sprint list have arrived. Doing it any
// earlier risks an extra round-trip (config-only → board scope, then
// sprint scope after sprints arrive); doing it any later means the
// user stares at an empty board for an extra HTTP round-trip's
// worth of latency.
func (m *boardModel) maybeFireInitialIssues() tea.Cmd {
	if m.initialIssuesFired || !m.gotConfig || !m.gotSprints {
		return nil
	}
	m.initialIssuesFired = true
	m.loading++
	return tea.Batch(m.spinner.Tick, m.fetchIssues())
}

// selectionLabel renders the selection-count chip used in the
// status footer; "" when nothing is selected so the footer hides.
func (m *boardModel) selectionLabel() string {
	if len(m.selected) == 0 {
		return ""
	}
	return fmt.Sprintf("%d selected", len(m.selected))
}

// bulkOrCursorKeys returns the multi-select set in stable display
// order when non-empty; otherwise the single focused card's key.
// Falling back to the cursor lets every shortcut behave the same
// whether or not the user has explicitly selected cards.
func (m *boardModel) bulkOrCursorKeys() []string {
	if len(m.selected) > 0 {
		out := []string{}
		for _, col := range m.grouped {
			for _, iss := range col {
				if m.selected[iss.Key] {
					out = append(out, iss.Key)
				}
			}
		}
		return out
	}
	if iss, ok := m.cardAtCursor(); ok {
		return []string{iss.Key}
	}
	return nil
}

// openFilterPicker opens a free-text picker that sets the board's
// substring filter against issue summaries / keys / labels. An
// empty submission clears the filter; the picker pre-fills with
// the current filter value so the user can edit rather than retype.
func (m *boardModel) openFilterPicker() tea.Cmd {
	loader := func(query string, token int) tea.Cmd {
		return func() tea.Msg {
			return pickerLoadedMsg{Token: token}
		}
	}
	prompt := "summary / key / label…"
	if m.filterText != "" {
		prompt = "current: " + m.filterText + " — type new or empty to clear"
	}
	p := NewAsyncPicker("filter", "Filter cards by text", prompt, loader)
	p.SetSize(m.width, m.height)
	p.EnableFreeText()
	m.picker = p
	return p.Init()
}

// openCreatePicker opens a free-text picker for the user to type a
// summary for a new issue. Submitting a non-empty value triggers
// createIssueCmd; an empty value cancels.
func (m *boardModel) openCreatePicker() tea.Cmd {
	if m.cfg == nil || m.cfg.Board.ProjectKey == "" {
		m.status = "✗ board has no project — can't create"
		return nil
	}
	loader := func(query string, token int) tea.Cmd {
		return func() tea.Msg {
			return pickerLoadedMsg{Token: token}
		}
	}
	title := "Create issue in " + m.cfg.Board.ProjectKey
	p := NewAsyncPicker("newissue", title, "summary…", loader)
	p.SetSize(m.width, m.height)
	p.EnableFreeText()
	m.picker = p
	return p.Init()
}

// createIssueCmd posts the new issue via CreateIssue. If the board
// is filtered to an active sprint, the new issue is then moved into
// that sprint so it actually appears on the current view.
func (m *boardModel) createIssueCmd(summary string) tea.Cmd {
	if m.cfg == nil {
		return nil
	}
	svc := m.svc
	project := m.cfg.Board.ProjectKey
	sprint := m.sprint
	m.loading++
	return func() tea.Msg {
		iss, err := svc.CreateIssue(api.CreateIssueInput{
			Project: project,
			Summary: summary,
		})
		if err != nil {
			return issueCreatedMsg{err: err}
		}
		if sprint > 0 {
			// Best-effort: if the sprint move fails (custom-field
			// not present, permissions, …) we still surface the
			// successful create rather than treating it as failure.
			_ = svc.MoveIssueToSprint(iss.Key, sprint)
		}
		return issueCreatedMsg{key: iss.Key}
	}
}

// ---------- update ----------

func (m boardModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layout()
		m.composeBody()
		if m.picker != nil {
			m.picker.SetSize(m.width, m.height)
		}
		return m, nil

	case tea.MouseMsg:
		// Wheel scrolls the body without disturbing the cursor.
		var cmd tea.Cmd
		m.vp, cmd = m.vp.Update(msg)
		return m, cmd

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		var pCmd tea.Cmd
		if m.picker != nil {
			pCmd, _ = m.picker.Update(msg)
		}
		if m.loading > 0 || m.picker != nil {
			return m, tea.Batch(cmd, pCmd)
		}
		return m, nil

	case pickerLoadedMsg, pickerTickMsg:
		if m.picker == nil {
			return m, nil
		}
		cmd, _ := m.picker.Update(msg)
		return m, cmd

	case pickerDoneMsg:
		m.picker = nil
		if msg.Cancelled {
			m.status = "cancelled"
			m.layout()
			return m, nil
		}
		switch msg.Purpose {
		case "newissue":
			summary, _ := msg.Value.(string)
			summary = strings.TrimSpace(summary)
			if summary == "" {
				m.status = "✗ summary cannot be empty"
				m.layout()
				return m, nil
			}
			cmd := m.createIssueCmd(summary)
			if cmd == nil {
				return m, nil
			}
			return m, tea.Batch(m.spinner.Tick, cmd)
		case "filter":
			text, _ := msg.Value.(string)
			m.filterText = strings.TrimSpace(text)
			m.regroup()
			m.clampCursor()
			m.layout()
			m.composeBody()
			m.snapVP()
			return m, nil
		}
		return m, nil

	case issueCreatedMsg:
		if m.loading > 0 {
			m.loading--
		}
		if msg.err != nil {
			m.status = "✗ create: " + msg.err.Error()
			m.layout()
			return m, nil
		}
		m.status = "✓ created " + msg.key
		m.layout()
		m.loading++
		return m, tea.Batch(m.spinner.Tick, m.fetchIssues())

	case boardConfigMsg:
		if msg.err != nil {
			m.err = msg.err
			m.loading = 0
			return m, nil
		}
		m.cfg = msg.cfg
		m.gotConfig = true
		if m.loading > 0 {
			m.loading--
		}
		// Title chips depend on cfg; relayout so chrome height is
		// re-measured before the next render.
		m.layout()
		m.composeBody()
		return m, m.maybeFireInitialIssues()

	case boardSprintsMsg:
		m.sprints = msg.sprints
		m.gotSprints = true
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
		m.layout()
		return m, m.maybeFireInitialIssues()

	case cardMovedMsg:
		if m.loading > 0 {
			m.loading--
		}
		if msg.err != nil {
			m.status = "✗ " + msg.label + ": " + msg.err.Error()
			m.layout()
			// Refetch to roll back the optimistic local update —
			// without this, the card would stay in its target
			// column visually even though Jira rejected the move.
			m.loading++
			return m, tea.Batch(m.spinner.Tick, m.fetchIssues())
		}
		m.status = "✓ " + msg.label
		// Clear the multi-select set: those cards have moved and
		// keeping them selected on the next view is rarely useful.
		m.selected = map[string]bool{}
		m.layout()
		m.composeBody()
		// No refetch on success — the optimistic update already
		// matches what Jira now has, and skipping the round-trip
		// is what makes successive H/L presses feel snappy.
		return m, nil

	case boardIssuesMsg:
		m.issues = msg.issues
		m.regroup()
		if m.loading > 0 {
			m.loading--
		}
		m.clampCursor()
		m.layout()
		m.composeBody()
		m.snapVP()
		return m, nil

	case tea.KeyMsg:
		// Picker eats all keys (including q/esc → cancel) while open.
		if m.picker != nil {
			cmd, _ := m.picker.Update(msg)
			return m, cmd
		}
		// vim two-key prefix: a second `g` after the first means
		// "top". Any other key clears the prefix.
		if m.pendingG && msg.String() == "g" {
			m.pendingG = false
			m.rowCursor = 0
			m.composeBody()
			m.vp.GotoTop()
			return m, nil
		}
		if msg.String() != "g" {
			m.pendingG = false
		}
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
			m.pendingG = true // allow `gg` for symmetry with vim
			m.composeBody()
			m.vp.GotoTop()
			return m, nil
		case key.Matches(msg, m.keys.Bottom):
			if n := len(m.currentColIssues()); n > 0 {
				m.rowCursor = n - 1
			}
			m.composeBody()
			m.snapVP()
			// Belt-and-braces: ensure we land at the very last line
			// of viewport content too (covers off-by-one when other
			// columns are taller than the current one).
			m.vp.GotoBottom()
			m.snapVP() // re-snap so cursor is still in view
		case key.Matches(msg, m.keys.HalfDown):
			m.moveRow(m.cardsPerHalfPage())
			m.composeBody()
			m.snapVP()
		case key.Matches(msg, m.keys.HalfUp):
			m.moveRow(-m.cardsPerHalfPage())
			m.composeBody()
			m.snapVP()
		case key.Matches(msg, m.keys.PageDown):
			m.moveRow(m.cardsPerPage())
			m.composeBody()
			m.snapVP()
		case key.Matches(msg, m.keys.PageUp):
			m.moveRow(-m.cardsPerPage())
			m.composeBody()
			m.snapVP()
		case key.Matches(msg, m.keys.FirstCol):
			m.colCursor = 0
			m.rowCursor = 0
			m.colOffset = 0
			m.composeBody()
			m.vp.GotoTop()
		case key.Matches(msg, m.keys.LastCol):
			if n := len(m.grouped); n > 0 {
				m.colCursor = n - 1
				m.rowCursor = 0
			}
			m.snapColOffset()
			m.composeBody()
			m.vp.GotoTop()
		case key.Matches(msg, m.keys.Help):
			m.help.ShowAll = !m.help.ShowAll
			// Help row height changes between short/full → relayout
			// so the viewport is sized correctly for the new chrome.
			m.layout()
			m.composeBody()
			m.snapVP()
		case key.Matches(msg, m.keys.MoveLeft):
			if cmd := m.moveCardCmd(-1); cmd != nil {
				return m, tea.Batch(m.spinner.Tick, cmd)
			}
			return m, nil
		case key.Matches(msg, m.keys.MoveRight):
			if cmd := m.moveCardCmd(1); cmd != nil {
				return m, tea.Batch(m.spinner.Tick, cmd)
			}
			return m, nil
		case key.Matches(msg, m.keys.Create):
			if cmd := m.openCreatePicker(); cmd != nil {
				return m, tea.Batch(m.spinner.Tick, cmd)
			}
			return m, nil
		case key.Matches(msg, m.keys.SelectToggle):
			if iss, ok := m.cardAtCursor(); ok {
				if m.selected[iss.Key] {
					delete(m.selected, iss.Key)
				} else {
					m.selected[iss.Key] = true
				}
				m.status = m.selectionLabel()
				m.layout()
				m.composeBody()
			}
			return m, nil
		case key.Matches(msg, m.keys.SelectColumn):
			// Toggle whole-column: if any card in the focused
			// column is already selected, clear them; otherwise
			// select the lot.
			cards := m.currentColIssues()
			anySelected := false
			for _, iss := range cards {
				if m.selected[iss.Key] {
					anySelected = true
					break
				}
			}
			for _, iss := range cards {
				if anySelected {
					delete(m.selected, iss.Key)
				} else {
					m.selected[iss.Key] = true
				}
			}
			m.status = m.selectionLabel()
			m.layout()
			m.composeBody()
			return m, nil
		case key.Matches(msg, m.keys.FilterMine):
			m.filterMine = !m.filterMine
			m.regroup()
			m.clampCursor()
			m.layout()
			m.composeBody()
			m.snapVP()
			return m, nil
		case key.Matches(msg, m.keys.FilterText):
			return m, m.openFilterPicker()
		case key.Matches(msg, m.keys.FilterClear):
			m.filterMine = false
			m.filterText = ""
			m.regroup()
			m.clampCursor()
			m.layout()
			m.composeBody()
			m.snapVP()
			return m, nil
		case key.Matches(msg, m.keys.Preview):
			m.previewOpen = !m.previewOpen
			// Layout depends on previewWidth(); recompute the
			// viewport size and re-render cards into the new
			// boardWidth before painting.
			m.layout()
			m.composeBody()
			m.snapVP()
			return m, nil
		case key.Matches(msg, m.keys.Refresh):
			m.loading = 1
			return m, tea.Batch(m.spinner.Tick, m.fetchIssues())
		case key.Matches(msg, m.keys.Theme):
			name := cycleTheme()
			m.status = "theme: " + name
			return m, nil
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
		if !m.passesFilters(iss) {
			continue
		}
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

// cardsPerPage returns the number of cards that fit in the body
// viewport — the unit for ⌃f / ⌃b style full-page navigation.
// Always at least 1 so the keys can't deadlock on a tiny terminal.
func (m *boardModel) cardsPerPage() int {
	if m.vp.Height <= 0 {
		return 1
	}
	n := m.vp.Height / cardRows
	if n < 1 {
		n = 1
	}
	return n
}

// cardsPerHalfPage is half a page (rounded up), the conventional
// vim ⌃d / ⌃u stride.
func (m *boardModel) cardsPerHalfPage() int {
	n := m.cardsPerPage() / 2
	if n < 1 {
		n = 1
	}
	return n
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
	// Preserve the row position across columns so h/l feels like a
	// vim-style cursor rather than a "jump to top" reset. When the
	// new column is shorter than the previous one, clamp to its
	// last card; empty columns drop the cursor to 0.
	if n := len(m.currentColIssues()); n == 0 {
		m.rowCursor = 0
	} else if m.rowCursor >= n {
		m.rowCursor = n - 1
	}
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

// previewWidth returns the right-pane width when the preview is
// open, 0 otherwise. The pane is suppressed on terminals too narrow
// to fit a single card next to it (under ~80 cols total) — without
// this guard the board collapses to a useless one-column strip.
func (m *boardModel) previewWidth() int {
	if !m.previewOpen || m.width < 80 {
		return 0
	}
	w := m.width / 3
	if w < 32 {
		w = 32
	}
	if w > 60 {
		w = 60
	}
	return w
}

// boardWidth returns the columns of horizontal space the board itself
// gets to render in (total width minus any preview pane). Used by
// every geometry function that previously read m.width directly.
func (m *boardModel) boardWidth() int {
	w := m.width - m.previewWidth()
	if w < 30 {
		w = 30
	}
	return w
}

func (m *boardModel) visibleColumns() int {
	bw := m.boardWidth()
	if bw <= 0 {
		return 1
	}
	w := cardMinWidth + 2 // border
	v := bw / w
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
	w := (m.boardWidth() - 2*visible) / visible
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
	cardBase = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).
			Padding(0, 1).Margin(0, 0, 1, 0)
	cardSelBase = lipgloss.NewStyle().Border(lipgloss.ThickBorder()).
			Padding(0, 1).Margin(0, 0, 1, 0)
	emptyColPlaceholder = lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Italic(true)
)

// issueTypeColor maps a Jira issue-type string to the colour used on
// the card border and key chip. Lookup is case-insensitive against
// the type name; common Jira / agile defaults are covered, with a
// muted grey fallback.
func issueTypeColor(typeName string) lipgloss.Color {
	switch strings.ToLower(typeName) {
	case "story":
		return lipgloss.Color("46") // bright green
	case "bug", "defect":
		return lipgloss.Color("196") // red
	case "epic":
		return lipgloss.Color("141") // purple
	case "task":
		return lipgloss.Color("39") // blue
	case "sub-task", "subtask":
		return lipgloss.Color("51") // cyan
	case "improvement":
		return lipgloss.Color("220") // yellow
	case "spike":
		return lipgloss.Color("208") // orange
	case "incident", "outage":
		return lipgloss.Color("197") // pink-red
	}
	return lipgloss.Color("245") // muted grey
}

func (m boardModel) View() string {
	if m.err != nil {
		return statusErr.Render("✗ "+m.err.Error()) + "\n\n" + paneMutedStyle.Render("press q to go back")
	}
	if m.cfg == nil {
		return paneMutedStyle.Render(m.spinner.View() + " loading board…")
	}

	// --- title bar ---
	header := m.renderTitleBar()

	// --- body / picker overlay ---
	body := m.vp.View()
	headerRow := m.headerRow
	if m.picker != nil {
		// Centre the picker over the body area; suppress the
		// sticky column header so the modal isn't visually
		// crowded.
		body = lipgloss.Place(m.width, m.vp.Height,
			lipgloss.Center, lipgloss.Center,
			m.picker.View(),
			lipgloss.WithWhitespaceChars(" "))
		headerRow = strings.Repeat(" ", m.width)
	}

	// --- side-by-side board + preview ---
	if pw := m.previewWidth(); pw > 0 && m.picker == nil {
		bw := m.boardWidth()
		boardCol := lipgloss.JoinVertical(lipgloss.Left, headerRow, body)
		boardCol = lipgloss.NewStyle().Width(bw).Render(boardCol)
		preview := lipgloss.NewStyle().Width(pw).
			Border(lipgloss.NormalBorder(), false, false, false, true).
			BorderForeground(lipgloss.Color("8")).
			PaddingLeft(1).
			Render(m.renderPreview(pw - 3))
		split := lipgloss.JoinHorizontal(lipgloss.Top, boardCol, preview)
		return strings.Join([]string{header, "", split, m.renderFooter()}, "\n")
	}

	// --- footer ---
	return strings.Join([]string{header, "", headerRow, body, m.renderFooter()}, "\n")
}

// renderPreview builds the right-pane content for the focused card.
// Uses only fields already on m.issues so it's instant on cursor
// move (no extra round-trip); summary is wrapped to the available
// width and the bottom suggests Enter for the full viewer.
func (m *boardModel) renderPreview(w int) string {
	iss, ok := m.cardAtCursor()
	if !ok {
		return paneMutedStyle.Render("(no card focused)")
	}
	if w < 16 {
		w = 16
	}
	typeCol := issueTypeColor(iss.IssueType)
	keyLine := lipgloss.NewStyle().Bold(true).Foreground(typeCol).Render(iss.Key)
	if iss.IssueType != "" {
		keyLine += "  " + lipgloss.NewStyle().Foreground(typeCol).Render("· "+iss.IssueType)
	}
	summary := wrap(iss.Summary, w, 8)
	if summary == "" {
		summary = paneMutedStyle.Render("(no summary)")
	}
	chips := []string{styleStatus(iss.StatusCat).Render(iss.Status)}
	if iss.Priority != "" {
		chips = append(chips, titleChipWarn.Render(iss.Priority))
	}
	if iss.StoryPoints > 0 {
		chips = append(chips, titleChip.Render(fmt.Sprintf("⛶ %g", iss.StoryPoints)))
	}
	chipLine := strings.Join(chips, " ")

	rows := []string{keyLine, "", summary, "", chipLine}
	if iss.Assignee != "" {
		rows = append(rows, paneMutedStyle.Render("Assignee:"), "  "+iss.Assignee)
	} else {
		rows = append(rows, paneMutedStyle.Render("Assignee:"), "  "+paneMutedStyle.Render("unassigned"))
	}
	if len(iss.Labels) > 0 {
		rows = append(rows, "", paneMutedStyle.Render("Labels:"), "  "+strings.Join(iss.Labels, ", "))
	}
	if iss.ParentKey != "" {
		rows = append(rows, "", paneMutedStyle.Render("Parent:"), "  "+iss.ParentKey)
	}
	rows = append(rows, "", paneMutedStyle.Render("enter → full view"))
	return strings.Join(rows, "\n")
}

// renderFooter composes the status + help row. Used both by View
// and by layout (so chrome-height measurement accounts for the
// status row's potential wrap on narrow terminals).
func (m *boardModel) renderFooter() string {
	footer := m.help.View(m.keys) + m.boardScrollHint()
	if m.status == "" {
		return footer
	}
	var st string
	switch {
	case strings.HasPrefix(m.status, "✗"):
		st = statusErr.Render(m.status)
	case strings.HasPrefix(m.status, "✓"):
		st = statusOK.Render(m.status)
	default:
		st = statusInfo.Render(m.status)
	}
	return st + "  " + titleSep + "  " + footer
}

// layout sizes the body viewport. The chrome height is *measured*
// rather than hard-coded because:
//   - the title bar may wrap onto a second line when the project +
//     sprint name is long for the current terminal width;
//   - the help row grows from 1 line (short) to 2 lines (full) when
//     the user toggles ?.
// Hard-coding "4" used to mean the bottom card on a long board got
// clipped by however many lines the chrome had grown by — pressing G
// looked like it didn't scroll quite far enough.
func (m *boardModel) layout() {
	w := m.boardWidth()
	if w < 30 {
		w = 30
	}
	// We use *visual* height (accounting for terminal-width line
	// wrapping) rather than lipgloss.Height which only counts \n.
	// Without this, on a narrow terminal the title bar wraps onto a
	// second visual row but layout still allocates the viewport as
	// if it didn't — pushing the bottom card off-screen and making
	// G look like it doesn't scroll quite far enough.
	titleH := visualHeight(m.renderTitleBar(), m.width)
	helpH := visualHeight(m.renderFooter(), m.width)
	// Layout: title (titleH) + blank (1) + sticky col header (1) +
	// vp (h) + footer (helpH).  Joined by 4 \n separators which add
	// no extra physical lines beyond what each block contributes.
	chromeH := titleH + 1 + 1 + helpH
	h := m.height - chromeH
	if h < 5 {
		h = 5
	}
	m.vp.Width = w
	m.vp.Height = h
}

// boardScrollHint returns the "cols 1-3 of 7" footer suffix when
// horizontal scrolling is active, "" otherwise. Extracted so the
// layout chrome calc matches what View() actually renders.
func (m *boardModel) boardScrollHint() string {
	if m.cfg == nil {
		return ""
	}
	visible := m.visibleColumns()
	if len(m.grouped) <= visible {
		return ""
	}
	from := m.colOffset
	to := from + visible
	if to > len(m.grouped) {
		to = len(m.grouped)
	}
	return paneMutedStyle.Render(
		fmt.Sprintf("  cols %d-%d of %d (h/l to scroll)", from+1, to, len(m.grouped)))
}

// visualHeight returns the number of physical terminal rows the
// rendered string s will occupy at the given terminal width. Each
// logical line is divided by termWidth (round-up) to capture the
// fact that long lines wrap visually. If termWidth <= 0 we fall
// back to lipgloss.Height (logical lines only).
func visualHeight(s string, termWidth int) int {
	if termWidth <= 0 {
		return lipgloss.Height(s)
	}
	h := 0
	for _, line := range strings.Split(s, "\n") {
		w := lipgloss.Width(line)
		if w == 0 {
			h++
			continue
		}
		h += (w + termWidth - 1) / termWidth
	}
	return h
}

// renderTitleBar produces the same title bar View() will display.
// Extracted so layout() can measure its height without duplicating
// the chip-assembly logic.
func (m *boardModel) renderTitleBar() string {
	if m.cfg == nil {
		return titleBar("BOARD", titleChipDim.Render("loading…"))
	}
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
		// Show "visible / total" when filters are reducing the
		// set so the user sees how aggressive the filter is.
		visible := 0
		for _, col := range m.grouped {
			visible += len(col)
		}
		if (m.filterMine || m.filterText != "") && visible != len(m.issues) {
			chips = append(chips, titleChip.Render(fmt.Sprintf("%d / %d issues", visible, len(m.issues))))
		} else {
			chips = append(chips, titleChip.Render(fmt.Sprintf("%d issues", len(m.issues))))
		}
	}
	if chip := m.filterChips(); chip != "" {
		chips = append(chips, titleChipWarn.Render(chip))
	}
	if m.loading > 0 {
		chips = append(chips, paneMutedStyle.Render(m.spinner.View()+" loading"))
	}
	return titleBar("BOARD · "+m.cfg.Board.Name, chips...)
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
// header is sticky above the viewport. Empty columns get a width-
// padded placeholder block so JoinHorizontal still allocates the
// right horizontal slot — without this, an empty column collapses
// to zero width and shifts every column to its right out of
// alignment with the sticky headers.
func (m *boardModel) renderColumnCards(idx, width int) string {
	if len(m.grouped[idx]) == 0 {
		return emptyColPlaceholder.Width(width).Render("  (empty)")
	}
	// Render each card as a discrete block, then JoinVertical with
	// Left alignment + an explicit width so lipgloss pads every line
	// (including the cards' bottom-margin line) to exactly `width`.
	// String-concatenating styled blocks loses that geometry, so the
	// trailing margin line of card N has width 0 and JoinHorizontal
	// shifts every subsequent column right by one card width.
	blocks := make([]string, 0, len(m.grouped[idx]))
	for ri, iss := range m.grouped[idx] {
		selected := idx == m.colCursor && ri == m.rowCursor
		marked := m.selected[iss.Key]
		blocks = append(blocks, renderBoardCard(iss, width, selected, marked))
	}
	col := lipgloss.JoinVertical(lipgloss.Left, blocks...)
	return lipgloss.NewStyle().Width(width).Render(col)
}

func renderBoardCard(iss api.Issue, width int, selected, marked bool) string {
	typeCol := issueTypeColor(iss.IssueType)
	keyStyle := lipgloss.NewStyle().Bold(true).Foreground(typeCol)
	keyLine := keyStyle.Render(iss.Key)
	if marked {
		// Bright check mark prefix so multi-selected cards are
		// instantly recognisable irrespective of cursor position.
		check := lipgloss.NewStyle().Foreground(lipgloss.Color("11")).Bold(true).Render("✓ ")
		keyLine = check + keyLine
	}
	if iss.IssueType != "" {
		keyLine += " " + lipgloss.NewStyle().Foreground(typeCol).Render("· "+iss.IssueType)
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

	style := cardBase.BorderForeground(typeCol)
	switch {
	case selected:
		// Brighter, thick border in the issue type's accent colour
		// so selection still pops without losing the type cue.
		style = cardSelBase.BorderForeground(lipgloss.Color("11"))
	case marked:
		// Multi-selected but not focused — yellow outline so the
		// bulk set is visible at a glance.
		style = cardBase.BorderForeground(lipgloss.Color("11"))
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
	Up, Down, Left, Right        key.Binding
	HalfDown, HalfUp             key.Binding // Ctrl-d / Ctrl-u
	PageDown, PageUp             key.Binding // Ctrl-f / Ctrl-b
	Top, Bottom                  key.Binding
	FirstCol, LastCol            key.Binding // 0 / $
	MoveLeft, MoveRight          key.Binding // H / L — drag card
	Create                       key.Binding // c — inline new issue
	SelectToggle, SelectColumn   key.Binding // v / V — bulk select
	FilterMine, FilterText, FilterClear key.Binding // m / / / M — quick filters
	Preview                      key.Binding // i — toggle side preview
	Enter, Open                  key.Binding
	Sprint, Refresh              key.Binding
	Theme                        key.Binding
	Help, Quit                   key.Binding
}

func defaultBoardKeys() boardKeys {
	return boardKeys{
		Up:       key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "card up")),
		Down:     key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "card down")),
		Left:     key.NewBinding(key.WithKeys("left", "h"), key.WithHelp("←/h", "column left")),
		Right:    key.NewBinding(key.WithKeys("right", "l"), key.WithHelp("→/l", "column right")),
		HalfDown: key.NewBinding(key.WithKeys("ctrl+d"), key.WithHelp("⌃d", "½ page down")),
		HalfUp:   key.NewBinding(key.WithKeys("ctrl+u"), key.WithHelp("⌃u", "½ page up")),
		PageDown: key.NewBinding(key.WithKeys("ctrl+f", "pgdown"), key.WithHelp("⌃f", "page down")),
		PageUp:   key.NewBinding(key.WithKeys("ctrl+b", "pgup"), key.WithHelp("⌃b", "page up")),
		Top:      key.NewBinding(key.WithKeys("g", "home"), key.WithHelp("g/gg", "top")),
		Bottom:   key.NewBinding(key.WithKeys("G", "end"), key.WithHelp("G", "bottom")),
		FirstCol:  key.NewBinding(key.WithKeys("0", "^"), key.WithHelp("0", "first column")),
		LastCol:   key.NewBinding(key.WithKeys("$"), key.WithHelp("$", "last column")),
		MoveLeft:  key.NewBinding(key.WithKeys("H"), key.WithHelp("H", "drag card left")),
		MoveRight: key.NewBinding(key.WithKeys("L"), key.WithHelp("L", "drag card right")),
		Create:    key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "create issue…")),
		SelectToggle: key.NewBinding(key.WithKeys("v"), key.WithHelp("v", "select card")),
		SelectColumn: key.NewBinding(key.WithKeys("V"), key.WithHelp("V", "select column")),
		FilterMine:   key.NewBinding(key.WithKeys("m"), key.WithHelp("m", "mine only")),
		FilterText:   key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "filter…")),
		FilterClear:  key.NewBinding(key.WithKeys("M"), key.WithHelp("M", "clear filters")),
		Preview:      key.NewBinding(key.WithKeys("i"), key.WithHelp("i", "preview pane")),
		Enter:    key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "open issue")),
		Open:     key.NewBinding(key.WithKeys("o"), key.WithHelp("o", "browser")),
		Sprint:   key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "cycle sprint")),
		Refresh:  key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "refresh")),
		Theme:    key.NewBinding(key.WithKeys("T"), key.WithHelp("T", "cycle theme")),
		Help:     key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
		Quit:     key.NewBinding(key.WithKeys("q", "ctrl+c", "esc"), key.WithHelp("q", "back")),
	}
}

func (k boardKeys) ShortHelp() []key.Binding {
	return []key.Binding{k.Left, k.Right, k.Up, k.Down, k.Enter, k.Sprint, k.Refresh, k.Quit}
}
func (k boardKeys) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.Left, k.Right, k.Up, k.Down, k.Top, k.Bottom, k.FirstCol, k.LastCol},
		{k.HalfDown, k.HalfUp, k.PageDown, k.PageUp, k.Enter, k.Open},
		{k.MoveLeft, k.MoveRight, k.Create, k.SelectToggle, k.SelectColumn},
		{k.FilterMine, k.FilterText, k.FilterClear, k.Preview},
		{k.Sprint, k.Refresh, k.Theme, k.Help, k.Quit},
	}
}
