package tui

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/hugs7/jira-cli/internal/api"
	"github.com/hugs7/jira-cli/internal/config"
)

// Issue runs the interactive issue viewer for the given key. Loops
// asynchronously: header + description load first, comments and
// transitions fetched in parallel.
func Issue(svc api.Service, key string) error {
	m := newIssueModel(svc, key)
	_, err := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion()).Run()
	return err
}

// ---------- model ----------

type issueMode int

const (
	modeDescription issueMode = iota
	modeComments
	modeLinks
	modeConfirmDelete
	modeTransitions
)

type issueModel struct {
	svc api.Service
	key string

	mode issueMode

	issue       *api.Issue
	comments    []api.Comment
	transitions []api.Transition
	links       []api.IssueLink

	desc        viewport.Model
	commentsVP  viewport.Model
	linksVP     viewport.Model
	transVP     viewport.Model
	commentsCur int
	transCur    int

	pendingDeleteID string

	width, height int
	loading       int
	status        string
	err           error

	spinner spinner.Model
	help    help.Model
	keys    issueKeys
}

func newIssueModel(svc api.Service, key string) issueModel {
	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("13"))
	return issueModel{
		svc:        svc,
		key:        key,
		mode:       modeDescription,
		desc:       viewport.New(0, 0),
		commentsVP: viewport.New(0, 0),
		linksVP:    viewport.New(0, 0),
		transVP:    viewport.New(0, 0),
		spinner:    sp,
		help:       help.New(),
		keys:       defaultIssueKeys(),
		loading:    3,
	}
}

func (m issueModel) Init() tea.Cmd {
	return tea.Batch(m.spinner.Tick, m.fetchIssue(), m.fetchComments(), m.fetchTransitions())
}

// ---------- async loaders ----------

type (
	issueLoadedMsg       struct{ iss *api.Issue; err error }
	commentsLoadedMsg    struct{ cs []api.Comment; err error }
	transitionsLoadedMsg struct{ ts []api.Transition; err error }
	linksLoadedMsg       struct{ ls []api.IssueLink; err error }
	actionDoneMsg        struct{ text string; err error; reload string }
	editorDoneMsg        struct {
		purpose   string // "edit-description" | "add-comment" | "edit-comment"
		commentID string
		body      string
		err       error
	}
)

func (m *issueModel) fetchIssue() tea.Cmd {
	return func() tea.Msg {
		iss, err := m.svc.GetIssue(m.key)
		return issueLoadedMsg{iss: iss, err: err}
	}
}
func (m *issueModel) fetchComments() tea.Cmd {
	return func() tea.Msg {
		cs, err := m.svc.ListComments(m.key)
		return commentsLoadedMsg{cs: cs, err: err}
	}
}
func (m *issueModel) fetchTransitions() tea.Cmd {
	return func() tea.Msg {
		ts, err := m.svc.ListTransitions(m.key)
		return transitionsLoadedMsg{ts: ts, err: err}
	}
}
func (m *issueModel) fetchLinks() tea.Cmd {
	return func() tea.Msg {
		ls, err := m.svc.ListLinks(m.key)
		return linksLoadedMsg{ls: ls, err: err}
	}
}

// ---------- update ----------

func (m issueModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layout()
		m.refreshContent()
		return m, nil

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		if m.loading > 0 {
			return m, cmd
		}
		return m, nil

	case issueLoadedMsg:
		m.dec()
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.issue = msg.iss
		m.refreshContent()
		return m, nil

	case commentsLoadedMsg:
		m.dec()
		if msg.err == nil {
			m.comments = msg.cs
		}
		m.refreshContent()
		return m, nil

	case transitionsLoadedMsg:
		m.dec()
		if msg.err == nil {
			m.transitions = msg.ts
		}
		m.refreshContent()
		return m, nil

	case linksLoadedMsg:
		m.dec()
		if msg.err == nil {
			m.links = msg.ls
		}
		m.refreshContent()
		return m, nil

	case actionDoneMsg:
		m.dec()
		if msg.err != nil {
			m.status = "✗ " + msg.text + ": " + msg.err.Error()
			return m, nil
		}
		m.status = "✓ " + msg.text
		switch msg.reload {
		case "issue":
			m.loading++
			return m, tea.Batch(m.spinner.Tick, m.fetchIssue())
		case "comments":
			m.loading++
			return m, tea.Batch(m.spinner.Tick, m.fetchComments())
		case "transitions":
			m.loading += 2
			return m, tea.Batch(m.spinner.Tick, m.fetchIssue(), m.fetchTransitions())
		}
		return m, nil

	case editorDoneMsg:
		body := strings.TrimSpace(msg.body)
		if msg.err != nil {
			m.status = "✗ editor: " + msg.err.Error()
			return m, nil
		}
		if body == "" {
			m.status = "aborted (empty)"
			return m, nil
		}
		switch msg.purpose {
		case "edit-description":
			m.loading++
			return m, tea.Batch(m.spinner.Tick, m.runAction("description updated", "issue", func() error {
				return m.svc.UpdateDescription(m.key, body)
			}))
		case "edit-summary":
			m.loading++
			return m, tea.Batch(m.spinner.Tick, m.runAction("summary updated", "issue", func() error {
				return m.svc.UpdateSummary(m.key, body)
			}))
		case "add-comment":
			m.loading++
			return m, tea.Batch(m.spinner.Tick, m.runAction("comment added", "comments", func() error {
				_, err := m.svc.AddComment(m.key, body)
				return err
			}))
		case "edit-comment":
			cid := msg.commentID
			m.loading++
			return m, tea.Batch(m.spinner.Tick, m.runAction(fmt.Sprintf("edited #%s", cid), "comments", func() error {
				_, err := m.svc.EditComment(m.key, cid, body)
				return err
			}))
		}
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m *issueModel) dec() {
	if m.loading > 0 {
		m.loading--
	}
}

func (m *issueModel) runAction(label, reload string, fn func() error) tea.Cmd {
	return func() tea.Msg {
		err := fn()
		return actionDoneMsg{text: label, err: err, reload: reload}
	}
}

// ---------- key handling ----------

func (m issueModel) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Mode-independent keys.
	switch {
	case key.Matches(msg, m.keys.Quit):
		return m, tea.Quit
	case key.Matches(msg, m.keys.Back):
		switch m.mode {
		case modeDescription:
			return m, tea.Quit
		case modeConfirmDelete:
			m.mode = modeComments
			m.status = "delete cancelled"
			return m, nil
		default:
			m.mode = modeDescription
			return m, nil
		}
	case key.Matches(msg, m.keys.Help):
		m.help.ShowAll = !m.help.ShowAll
		m.layout()
		return m, nil
	case key.Matches(msg, m.keys.TabDesc):
		m.mode = modeDescription
		return m, nil
	case key.Matches(msg, m.keys.TabComments):
		m.mode = modeComments
		return m, nil
	case key.Matches(msg, m.keys.TabLinks):
		m.mode = modeLinks
		if m.links == nil {
			m.loading++
			return m, tea.Batch(m.spinner.Tick, m.fetchLinks())
		}
		return m, nil
	case key.Matches(msg, m.keys.TabTransitions):
		m.mode = modeTransitions
		return m, nil
	case key.Matches(msg, m.keys.OpenBrowser):
		if m.issue != nil && m.issue.WebURL != "" {
			_ = openInBrowser(m.issue.WebURL)
		}
		return m, nil
	case key.Matches(msg, m.keys.NewComment):
		return m, m.editorCmd("add-comment", "", "")
	case key.Matches(msg, m.keys.EditDescription):
		desc := ""
		if m.issue != nil {
			desc = m.issue.Description
		}
		return m, m.editorCmd("edit-description", "", desc)
	case key.Matches(msg, m.keys.EditSummary):
		summary := ""
		if m.issue != nil {
			summary = m.issue.Summary
		}
		return m, m.editorCmd("edit-summary", "", summary)
	case key.Matches(msg, m.keys.AssignMe):
		me := m.svc.Me()
		if me == "" {
			m.status = "✗ no `me` configured for this host"
			return m, nil
		}
		m.loading++
		return m, tea.Batch(m.spinner.Tick, m.runAction("assigned to "+me, "issue", func() error {
			return m.svc.AssignIssue(m.key, me)
		}))
	case key.Matches(msg, m.keys.Unassign):
		m.loading++
		return m, tea.Batch(m.spinner.Tick, m.runAction("unassigned", "issue", func() error {
			return m.svc.AssignIssue(m.key, "")
		}))
	}

	// Per-mode keys.
	switch m.mode {
	case modeDescription:
		var cmd tea.Cmd
		m.desc, cmd = m.desc.Update(msg)
		return m, cmd

	case modeComments:
		switch {
		case key.Matches(msg, m.keys.Up):
			if m.commentsCur > 0 {
				m.commentsCur--
				m.refreshContent()
			}
			return m, nil
		case key.Matches(msg, m.keys.Down):
			if m.commentsCur < len(m.comments)-1 {
				m.commentsCur++
				m.refreshContent()
			}
			return m, nil
		case key.Matches(msg, m.keys.EditComment):
			if cm, ok := m.commentAtCursor(); ok {
				return m, m.editorCmd("edit-comment", cm.ID, cm.Body)
			}
			return m, nil
		case key.Matches(msg, m.keys.DeleteComment):
			if cm, ok := m.commentAtCursor(); ok {
				m.pendingDeleteID = cm.ID
				m.mode = modeConfirmDelete
			}
			return m, nil
		}
		var cmd tea.Cmd
		m.commentsVP, cmd = m.commentsVP.Update(msg)
		return m, cmd

	case modeLinks:
		var cmd tea.Cmd
		m.linksVP, cmd = m.linksVP.Update(msg)
		return m, cmd

	case modeTransitions:
		switch {
		case key.Matches(msg, m.keys.Up):
			if m.transCur > 0 {
				m.transCur--
				m.refreshContent()
			}
			return m, nil
		case key.Matches(msg, m.keys.Down):
			if m.transCur < len(m.transitions)-1 {
				m.transCur++
				m.refreshContent()
			}
			return m, nil
		case key.Matches(msg, m.keys.Enter):
			if m.transCur < len(m.transitions) {
				t := m.transitions[m.transCur]
				m.loading++
				m.mode = modeDescription
				return m, tea.Batch(m.spinner.Tick, m.runAction("transitioned to "+t.To, "transitions", func() error {
					return m.svc.DoTransition(m.key, t.ID)
				}))
			}
			return m, nil
		}
		return m, nil

	case modeConfirmDelete:
		switch {
		case key.Matches(msg, m.keys.ConfirmYes):
			cid := m.pendingDeleteID
			m.pendingDeleteID = ""
			m.mode = modeComments
			m.loading++
			return m, tea.Batch(m.spinner.Tick, m.runAction("deleted comment "+cid, "comments", func() error {
				return m.svc.DeleteComment(m.key, cid)
			}))
		case key.Matches(msg, m.keys.ConfirmNo):
			m.pendingDeleteID = ""
			m.mode = modeComments
			m.status = "delete cancelled"
			return m, nil
		}
	}

	return m, nil
}

func (m *issueModel) commentAtCursor() (api.Comment, bool) {
	if m.commentsCur < 0 || m.commentsCur >= len(m.comments) {
		return api.Comment{}, false
	}
	return m.comments[m.commentsCur], true
}

// ---------- editor ----------

func (m *issueModel) editorCmd(purpose, commentID, initial string) tea.Cmd {
	hint := purpose
	header := ""
	switch purpose {
	case "edit-description":
		header = fmt.Sprintf("# Editing description for %s. Lines starting with # are stripped.\n\n", m.key)
	case "add-comment":
		header = fmt.Sprintf("# New comment on %s. Lines starting with # are stripped.\n\n", m.key)
	case "edit-comment":
		header = fmt.Sprintf("# Editing comment %s on %s. Lines starting with # are stripped.\n\n", commentID, m.key)
	}
	body := header + initial
	f, err := os.CreateTemp("", "jr-edit-*-"+sanitizeFilename(hint)+".md")
	if err != nil {
		return func() tea.Msg { return editorDoneMsg{purpose: purpose, commentID: commentID, err: err} }
	}
	tmp := f.Name()
	if _, err := f.WriteString(body); err != nil {
		f.Close()
		os.Remove(tmp)
		return func() tea.Msg { return editorDoneMsg{purpose: purpose, commentID: commentID, err: err} }
	}
	f.Close()

	editor := config.Get().EditorCmd()
	parts := strings.Fields(editor)
	if len(parts) == 0 {
		os.Remove(tmp)
		return func() tea.Msg {
			return editorDoneMsg{purpose: purpose, commentID: commentID, err: fmt.Errorf("no editor configured")}
		}
	}
	args := append(parts[1:], tmp)
	cmd := exec.Command(parts[0], args...)
	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		defer os.Remove(tmp)
		if err != nil {
			return editorDoneMsg{purpose: purpose, commentID: commentID, err: err}
		}
		data, rerr := os.ReadFile(tmp)
		if rerr != nil {
			return editorDoneMsg{purpose: purpose, commentID: commentID, err: rerr}
		}
		// Strip comment lines so users can keep the hint header.
		var b strings.Builder
		for _, line := range strings.Split(string(data), "\n") {
			t := strings.TrimSpace(line)
			if strings.HasPrefix(t, "#") || strings.HasPrefix(t, "<!--") {
				continue
			}
			b.WriteString(line)
			b.WriteByte('\n')
		}
		return editorDoneMsg{purpose: purpose, commentID: commentID, body: strings.TrimSpace(b.String())}
	})
}

func sanitizeFilename(s string) string {
	r := strings.NewReplacer("/", "-", "\\", "-", " ", "_")
	return r.Replace(s)
}

// ---------- view ----------

func (m *issueModel) layout() {
	helpH := lipgloss.Height(m.help.View(m.keys))
	headerH := 4
	bodyH := m.height - headerH - helpH - 1
	if bodyH < 5 {
		bodyH = 5
	}
	w := m.width
	if w < 30 {
		w = 30
	}
	m.desc.Width, m.desc.Height = w, bodyH
	m.commentsVP.Width, m.commentsVP.Height = w, bodyH
	m.linksVP.Width, m.linksVP.Height = w, bodyH
	m.transVP.Width, m.transVP.Height = w, bodyH
}

// refreshContent re-renders whichever viewport the current mode uses.
func (m *issueModel) refreshContent() {
	if m.issue != nil {
		desc := m.issue.Description
		if strings.TrimSpace(desc) == "" {
			desc = paneMutedStyle.Render("(no description)")
		}
		m.desc.SetContent(desc)
	}
	m.commentsVP.SetContent(m.renderComments())
	m.linksVP.SetContent(m.renderLinks())
	m.transVP.SetContent(m.renderTransitions())
}

func (m issueModel) View() string {
	if m.err != nil {
		return statusErr.Render("error: "+m.err.Error()) + "\n\npress q to quit"
	}

	header := m.renderHeader()
	tabs := m.renderTabs()

	var body string
	switch m.mode {
	case modeComments:
		body = m.commentsVP.View()
	case modeLinks:
		body = m.linksVP.View()
	case modeTransitions:
		body = m.transVP.View()
	case modeConfirmDelete:
		warn := lipgloss.NewStyle().
			Foreground(lipgloss.Color("9")).
			Bold(true).
			BorderStyle(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("9")).
			Padding(0, 2)
		body = "\n  " + warn.Render(fmt.Sprintf("Delete comment %s?  [y/n]", m.pendingDeleteID))
	default:
		body = m.desc.View()
	}

	footer := m.help.View(m.keys)
	status := ""
	if m.loading > 0 {
		status = statusInfo.Render(m.spinner.View() + " loading…")
	} else if m.status != "" {
		switch {
		case strings.HasPrefix(m.status, "✗"):
			status = statusErr.Render(m.status)
		case strings.HasPrefix(m.status, "✓"):
			status = statusOK.Render(m.status)
		default:
			status = statusInfo.Render(m.status)
		}
	}
	if status != "" {
		footer = status + "  " + titleSep + "  " + footer
	}
	return strings.Join([]string{header, tabs, body, footer}, "\n")
}

func (m issueModel) renderHeader() string {
	if m.issue == nil {
		return titleBar(m.key, titleChipDim.Render("loading…"))
	}
	chips := []string{
		titleChip.Render(m.issue.IssueType),
		styleStatus(m.issue.StatusCat).Render(m.issue.Status),
	}
	if m.issue.Priority != "" {
		chips = append(chips, titleChipWarn.Render(m.issue.Priority))
	}
	if m.issue.Assignee != "" {
		chips = append(chips, titleChipDim.Render("@"+m.issue.Assignee))
	}
	header := titleBar(m.key+"  "+m.issue.Summary, chips...)
	return header
}

func (m issueModel) renderTabs() string {
	tab := func(label string, mine, active bool) string {
		s := lipgloss.NewStyle().Padding(0, 2)
		if active {
			s = s.Bold(true).Foreground(lipgloss.Color("231")).Background(lipgloss.Color("57"))
		} else if mine {
			s = s.Foreground(lipgloss.Color("141"))
		} else {
			s = s.Foreground(lipgloss.Color("8"))
		}
		return s.Render(label)
	}
	parts := []string{
		tab("[d] description", true, m.mode == modeDescription),
		tab(fmt.Sprintf("[c] comments (%d)", len(m.comments)), true, m.mode == modeComments),
		tab(fmt.Sprintf("[l] links"), true, m.mode == modeLinks),
		tab(fmt.Sprintf("[t] transitions (%d)", len(m.transitions)), true, m.mode == modeTransitions),
	}
	return strings.Join(parts, " ")
}

func (m issueModel) renderComments() string {
	if len(m.comments) == 0 {
		return paneMutedStyle.Render("(no comments — press n to add one)")
	}
	var b strings.Builder
	for i, c := range m.comments {
		marker := "  "
		if i == m.commentsCur {
			marker = lipgloss.NewStyle().Foreground(lipgloss.Color("11")).Bold(true).Render("▶ ")
		}
		when := ""
		if !c.CreatedAt.IsZero() {
			when = "  " + paneMutedStyle.Render(humanTime(c.CreatedAt))
		}
		head := marker + lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("13")).Render(c.Author) + when
		b.WriteString(head)
		b.WriteByte('\n')
		body := strings.TrimSpace(c.Body)
		if body == "" {
			body = paneMutedStyle.Render("(empty)")
		}
		for _, ln := range strings.Split(body, "\n") {
			b.WriteString("    " + ln + "\n")
		}
		b.WriteByte('\n')
	}
	return b.String()
}

func (m issueModel) renderLinks() string {
	if m.links == nil {
		return paneMutedStyle.Render("loading links…")
	}
	if len(m.links) == 0 {
		return paneMutedStyle.Render("(no linked issues)")
	}
	var b strings.Builder
	for _, l := range m.links {
		key := lipgloss.NewStyle().Foreground(lipgloss.Color("12")).Render(l.OtherKey)
		typ := paneMutedStyle.Render(l.Type)
		b.WriteString(fmt.Sprintf("  %-20s %s  %s\n", typ, key, l.OtherSum))
	}
	return b.String()
}

func (m issueModel) renderTransitions() string {
	if len(m.transitions) == 0 {
		return paneMutedStyle.Render("(no transitions available)")
	}
	var b strings.Builder
	b.WriteString(paneMutedStyle.Render("Pick a workflow step (enter to apply):"))
	b.WriteString("\n\n")
	for i, t := range m.transitions {
		marker := "  "
		if i == m.transCur {
			marker = lipgloss.NewStyle().Foreground(lipgloss.Color("11")).Bold(true).Render("▶ ")
		}
		name := lipgloss.NewStyle().Bold(true).Render(t.Name)
		to := lipgloss.NewStyle().Foreground(lipgloss.Color("10")).Render(t.To)
		b.WriteString(fmt.Sprintf("%s%s  →  %s\n", marker, name, to))
	}
	return b.String()
}

func humanTime(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// ---------- key map ----------

type issueKeys struct {
	Up, Down, Enter, Back, Quit, Help key.Binding

	TabDesc, TabComments, TabLinks, TabTransitions key.Binding

	OpenBrowser, EditDescription, EditSummary, NewComment key.Binding
	AssignMe, Unassign                                    key.Binding
	EditComment, DeleteComment, ConfirmYes, ConfirmNo     key.Binding
}

func defaultIssueKeys() issueKeys {
	return issueKeys{
		Up:    key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "up")),
		Down:  key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "down")),
		Enter: key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "apply")),
		Back:  key.NewBinding(key.WithKeys("esc", "h"), key.WithHelp("esc/h", "back")),
		Quit:  key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit")),
		Help:  key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),

		TabDesc:        key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "description")),
		TabComments:    key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "comments")),
		TabLinks:       key.NewBinding(key.WithKeys("l"), key.WithHelp("l", "links")),
		TabTransitions: key.NewBinding(key.WithKeys("t"), key.WithHelp("t", "transitions")),

		OpenBrowser:     key.NewBinding(key.WithKeys("o"), key.WithHelp("o", "browser")),
		EditDescription: key.NewBinding(key.WithKeys("e"), key.WithHelp("e", "edit desc")),
		EditSummary:     key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "edit summary")),
		AssignMe:        key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "assign me")),
		Unassign:        key.NewBinding(key.WithKeys("U"), key.WithHelp("U", "unassign")),
		NewComment:      key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "new comment")),
		EditComment:     key.NewBinding(key.WithKeys("E"), key.WithHelp("E", "edit comment")),
		DeleteComment:   key.NewBinding(key.WithKeys("D"), key.WithHelp("D", "delete comment")),
		ConfirmYes:      key.NewBinding(key.WithKeys("y", "Y"), key.WithHelp("y", "yes")),
		ConfirmNo:       key.NewBinding(key.WithKeys("n", "N"), key.WithHelp("n", "no")),
	}
}

func (k issueKeys) ShortHelp() []key.Binding {
	return []key.Binding{
		k.TabDesc, k.TabComments, k.TabTransitions, k.AssignMe, k.EditSummary, k.EditDescription, k.NewComment, k.OpenBrowser, k.Back, k.Quit,
	}
}
func (k issueKeys) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.TabDesc, k.TabComments, k.TabLinks, k.TabTransitions},
		{k.AssignMe, k.Unassign, k.EditSummary, k.EditDescription, k.OpenBrowser},
		{k.NewComment, k.EditComment, k.DeleteComment},
		{k.Up, k.Down, k.Enter, k.Help, k.Back, k.Quit},
	}
}
