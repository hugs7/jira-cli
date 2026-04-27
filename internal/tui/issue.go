package tui

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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
	modeAttachments
	modeConfirmDelete
	modeTransitions
	modePicker // generic picker overlay (assignee, priority, …)
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
	attachVP    viewport.Model
	commentsCur int
	transCur    int
	linksCur    int
	attachCur   int

	pendingDeleteID string

	// picker is the active modal overlay (assignee picker, priority,
	// labels, …). nil when no picker is open. modeReturn is the mode
	// to restore when the picker closes/cancels.
	picker     *pickerModel
	modeReturn issueMode

	// pendingLinkType is set after the user picks a link type and
	// before they pick the target issue (the two-step "add link"
	// flow). Cleared on cancel or completion.
	pendingLinkType *pendingLink

	width, height int
	loading       int
	status        string
	err           error

	spinner spinner.Model
	help    help.Model
	keys    issueKeys

	// settings overlay (toggled with `,`).
	settings     settingsModel
	settingsOpen bool
}

func newIssueModel(svc api.Service, key string) issueModel {
	initTheme()

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
		attachVP:   viewport.New(0, 0),
		spinner:    sp,
		help:       help.New(),
		keys:       defaultIssueKeys(),
		settings:   newSettings(),
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
		m.settings.SetSize(m.width, m.height-4)
		if m.picker != nil {
			m.picker.SetSize(m.width, m.height)
		}
		return m, nil

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		// Forward to the picker so its own loading spinner ticks too.
		var pCmd tea.Cmd
		if m.picker != nil {
			pCmd, _ = m.picker.Update(msg)
		}
		if m.loading > 0 || m.picker != nil {
			return m, tea.Batch(cmd, pCmd)
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
			// Clamp the cursor after a delete-then-reload cycle.
			if m.linksCur >= len(m.links) {
				m.linksCur = len(m.links) - 1
			}
			if m.linksCur < 0 {
				m.linksCur = 0
			}
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
		case "links":
			// Force-clear current links so refreshContent doesn't
			// flash the stale list while the new fetch is in flight.
			m.links = nil
			m.loading++
			return m, tea.Batch(m.spinner.Tick, m.fetchLinks())
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

	case watchersForPickerMsg:
		m.dec()
		if msg.err != nil {
			m.status = "✗ load watchers: " + msg.err.Error()
			return m, nil
		}
		return m, m.showWatchersPicker(msg.current)

	case pickerLoadedMsg, pickerTickMsg:
		if m.picker == nil {
			return m, nil
		}
		cmd, _ := m.picker.Update(msg)
		return m, cmd

	case pickerDoneMsg:
		m.picker = nil
		m.mode = m.modeReturn
		if msg.Cancelled {
			// Cancelling either step of the add-link flow aborts
			// the whole flow.
			m.pendingLinkType = nil
			m.status = "cancelled"
			return m, nil
		}
		return m.applyPicker(msg)

	case tea.KeyMsg:
		// Settings overlay owns all keys while open: esc closes,
		// q / ctrl+c still quit, everything else flows into the
		// settings list (navigation + enter/space toggles).
		if m.settingsOpen {
			switch msg.String() {
			case "q", "ctrl+c":
				return m, tea.Quit
			case "esc":
				m.settingsOpen = false
				return m, nil
			}
			var cmd tea.Cmd
			m.settings, cmd = m.settings.Update(msg)
			return m, cmd
		}
		// Picker eats all keys while open.
		if m.picker != nil && m.mode == modePicker {
			cmd, _ := m.picker.Update(msg)
			return m, cmd
		}
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
	case key.Matches(msg, m.keys.Settings):
		// Open the universal settings overlay (theme, …).
		m.settingsOpen = true
		m.settings.SetSize(m.width, m.height-4)
		return m, nil
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
	case key.Matches(msg, m.keys.TabAttachments):
		m.mode = modeAttachments
		// Loaded Issue already carries Attachments; just clamp the
		// cursor in case a previous fetch trimmed the list.
		if m.issue != nil && m.attachCur >= len(m.issue.Attachments) {
			m.attachCur = 0
		}
		m.refreshContent()
		return m, nil
	case key.Matches(msg, m.keys.TabTransitions):
		m.mode = modeTransitions
		return m, nil
	case key.Matches(msg, m.keys.UploadAttachment):
		return m, m.openUploadAttachmentPicker()
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
	case key.Matches(msg, m.keys.AssignPick):
		return m, m.openAssigneePicker()
	case key.Matches(msg, m.keys.PickPriority):
		return m, m.openPriorityPicker()
	case key.Matches(msg, m.keys.PickType):
		return m, m.openIssueTypePicker()
	case key.Matches(msg, m.keys.PickSprint):
		return m, m.openSprintPicker()
	case key.Matches(msg, m.keys.EditLabels):
		return m, m.openLabelsPicker()
	case key.Matches(msg, m.keys.EditComponents):
		return m, m.openComponentsPicker()
	case key.Matches(msg, m.keys.EditFixVersions):
		return m, m.openFixVersionsPicker()
	case key.Matches(msg, m.keys.EditDueDate):
		return m, m.openDueDatePicker()
	case key.Matches(msg, m.keys.EditStoryPoints):
		return m, m.openStoryPointsPicker()
	case key.Matches(msg, m.keys.AddLink):
		return m, m.openLinkTypePicker()
	case key.Matches(msg, m.keys.ToggleWatch):
		return m, m.toggleWatchCmd()
	case key.Matches(msg, m.keys.EditWatchers):
		return m, m.openWatchersPicker()
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
		switch {
		case key.Matches(msg, m.keys.Up):
			if m.linksCur > 0 {
				m.linksCur--
				m.refreshContent()
			}
			return m, nil
		case key.Matches(msg, m.keys.Down):
			if m.linksCur < len(m.links)-1 {
				m.linksCur++
				m.refreshContent()
			}
			return m, nil
		case key.Matches(msg, m.keys.RemoveLink):
			if l, ok := m.linkAtCursor(); ok && l.ID != "" {
				id := l.ID
				other := l.OtherKey
				m.loading++
				return m, tea.Batch(m.spinner.Tick, m.runAction(
					"removed link to "+other, "links", func() error {
						return m.svc.DeleteIssueLink(id)
					}))
			}
			return m, nil
		}
		var cmd tea.Cmd
		m.linksVP, cmd = m.linksVP.Update(msg)
		return m, cmd

	case modeAttachments:
		count := 0
		if m.issue != nil {
			count = len(m.issue.Attachments)
		}
		switch {
		case key.Matches(msg, m.keys.Up):
			if m.attachCur > 0 {
				m.attachCur--
				m.refreshContent()
			}
			return m, nil
		case key.Matches(msg, m.keys.Down):
			if m.attachCur < count-1 {
				m.attachCur++
				m.refreshContent()
			}
			return m, nil
		case key.Matches(msg, m.keys.Enter):
			if a, ok := m.attachmentAtCursor(); ok && a.BrowseURL != "" {
				_ = openInBrowser(a.BrowseURL)
			}
			return m, nil
		case key.Matches(msg, m.keys.DeleteAttachment):
			if a, ok := m.attachmentAtCursor(); ok {
				id := a.ID
				name := a.Filename
				m.loading++
				return m, tea.Batch(m.spinner.Tick, m.runAction(
					"deleted "+name, "issue", func() error {
						return m.svc.DeleteAttachment(id)
					}))
			}
			return m, nil
		}
		var cmd tea.Cmd
		m.attachVP, cmd = m.attachVP.Update(msg)
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
	m.attachVP.Width, m.attachVP.Height = w, bodyH
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
	m.attachVP.SetContent(m.renderAttachments())
}

func (m issueModel) View() string {
	if m.err != nil {
		return statusErr.Render("error: "+m.err.Error()) + "\n\npress q to quit"
	}

	// Settings overlay replaces the body so the user has the whole
	// frame to navigate toggles. Esc returns to the issue.
	if m.settingsOpen {
		settingsHeader := titleBar("SETTINGS",
			titleChipDim.Render("persisted to ~/.config/jr/config.yml"))
		footer := m.help.View(m.keys)
		return settingsHeader + "\n" + m.settings.View() + "\n" + footer
	}

	header := m.renderHeader()
	tabs := m.renderTabs()

	var body string
	switch m.mode {
	case modePicker:
		// Centre the picker box over the body area. lipgloss.Place
		// handles the vertical/horizontal maths so the modal stays
		// nicely middle-screen as the window resizes.
		body = lipgloss.Place(m.width, m.desc.Height,
			lipgloss.Center, lipgloss.Center,
			m.picker.View(),
			lipgloss.WithWhitespaceChars(" "))
	case modeComments:
		body = m.commentsVP.View()
	case modeLinks:
		body = m.linksVP.View()
	case modeAttachments:
		body = m.attachVP.View()
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
	if m.issue.WatchCount > 0 || m.issue.Watching {
		eye := "👁"
		if m.issue.Watching {
			chips = append(chips, lipgloss.NewStyle().
				Foreground(lipgloss.Color("159")).
				Render(fmt.Sprintf("%s %d", eye, m.issue.WatchCount)))
		} else {
			chips = append(chips, titleChipDim.Render(fmt.Sprintf("%s %d", eye, m.issue.WatchCount)))
		}
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
	attachCount := 0
	if m.issue != nil {
		attachCount = len(m.issue.Attachments)
	}
	parts := []string{
		tab("[d] description", true, m.mode == modeDescription),
		tab(fmt.Sprintf("[c] comments (%d)", len(m.comments)), true, m.mode == modeComments),
		tab(fmt.Sprintf("[l] links"), true, m.mode == modeLinks),
		tab(fmt.Sprintf("[f] files (%d)", attachCount), true, m.mode == modeAttachments),
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
		return paneMutedStyle.Render("(no linked issues — press K to add one)")
	}
	var b strings.Builder
	for i, l := range m.links {
		marker := "  "
		if i == m.linksCur {
			marker = lipgloss.NewStyle().Foreground(lipgloss.Color("11")).Bold(true).Render("▶ ")
		}
		key := lipgloss.NewStyle().Foreground(lipgloss.Color("12")).Render(l.OtherKey)
		typ := paneMutedStyle.Render(l.Type)
		b.WriteString(fmt.Sprintf("%s%-20s %s  %s\n", marker, typ, key, l.OtherSum))
	}
	return b.String()
}

// linkAtCursor returns the IssueLink under the modeLinks cursor, if
// the cursor index is in range.
func (m *issueModel) linkAtCursor() (api.IssueLink, bool) {
	if m.linksCur < 0 || m.linksCur >= len(m.links) {
		return api.IssueLink{}, false
	}
	return m.links[m.linksCur], true
}

// renderAttachments lists the files attached to the issue, with the
// cursor row highlighted. Empty state advertises the upload key so
// the feature is discoverable without the help overlay.
func (m issueModel) renderAttachments() string {
	if m.issue == nil {
		return paneMutedStyle.Render("loading…")
	}
	if len(m.issue.Attachments) == 0 {
		return paneMutedStyle.Render("(no attachments — press F to upload one)")
	}
	var b strings.Builder
	for i, a := range m.issue.Attachments {
		marker := "  "
		if i == m.attachCur {
			marker = lipgloss.NewStyle().Foreground(lipgloss.Color("11")).Bold(true).Render("▶ ")
		}
		name := lipgloss.NewStyle().Foreground(lipgloss.Color("12")).Render(a.Filename)
		size := paneMutedStyle.Render(humanBytes(a.Size))
		meta := paneMutedStyle.Render(fmt.Sprintf("by %s · %s", a.Author, a.CreatedAt.Local().Format("2006-01-02 15:04")))
		b.WriteString(fmt.Sprintf("%s%s  %s  %s\n", marker, name, size, meta))
	}
	return b.String()
}

// attachmentAtCursor returns the Attachment under the modeAttachments
// cursor if in range.
func (m *issueModel) attachmentAtCursor() (api.Attachment, bool) {
	if m.issue == nil || m.attachCur < 0 || m.attachCur >= len(m.issue.Attachments) {
		return api.Attachment{}, false
	}
	return m.issue.Attachments[m.attachCur], true
}

// humanBytes renders a byte count in the smallest unit that yields a
// number under 1024 (e.g. "12.4 MB").
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for x := n / unit; x >= unit; x /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
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

// ---------- picker plumbing ----------

// openAssigneePicker swaps the screen for an async user picker
// scoped to this issue's assignable users. The first row is always
// "(unassigned)" so users can clear the field without leaving the
// picker; the second row is "Me" as a fast-path when the cursor is
// already there.
func (m *issueModel) openAssigneePicker() tea.Cmd {
	loader := func(query string, token int) tea.Cmd {
		key := m.key
		svc := m.svc
		return func() tea.Msg {
			users, err := svc.SearchAssignableUsers(key, query, 25)
			items := []PickerItem{
				{Label: "(unassigned)", Sub: "clear assignee", Value: ""},
			}
			if me := svc.Me(); me != "" {
				items = append(items, PickerItem{
					Label: "Me", Sub: me, Value: me,
				})
			}
			for _, u := range users {
				val := u.AccountID
				if val == "" {
					val = u.Name
				}
				if val == "" {
					continue
				}
				items = append(items, PickerItem{
					Label: u.DisplayName,
					Sub:   strings.TrimSpace(u.Name + "  " + u.Email),
					Value: val,
				})
			}
			return pickerLoadedMsg{Token: token, Items: items, Err: err}
		}
	}
	p := NewAsyncPicker("assignee", "Assign "+m.key+" to…", "type a name…", loader)
	p.SetSize(m.width, m.height)
	m.modeReturn = m.mode
	m.mode = modePicker
	m.picker = p
	return p.Init()
}

// openPriorityPicker fetches the priority catalogue once and shows
// it as a static-filter picker. The first row is "(none)" so the
// user can clear the field without leaving the picker.
func (m *issueModel) openPriorityPicker() tea.Cmd {
	loader := func(query string, token int) tea.Cmd {
		svc := m.svc
		return func() tea.Msg {
			items := []PickerItem{
				{Label: "(none)", Sub: "clear priority", Value: ""},
			}
			ps, err := svc.ListPriorities()
			for _, p := range ps {
				items = append(items, PickerItem{
					Label: p.Name, Sub: p.Description, Value: p.Name,
				})
			}
			return pickerLoadedMsg{Token: token, Items: items, Err: err}
		}
	}
	p := NewAsyncPicker("priority", "Set priority for "+m.key, "filter…", loader)
	p.SetSize(m.width, m.height)
	m.modeReturn = m.mode
	m.mode = modePicker
	m.picker = p
	return p.Init()
}

// openIssueTypePicker shows the project's issue types so the user
// can re-classify (Story → Bug, Task → Sub-task, …). Driven by
// project key from the loaded issue; falls back to the global
// catalogue if the issue isn't loaded yet.
func (m *issueModel) openIssueTypePicker() tea.Cmd {
	projectKey := ""
	if m.issue != nil {
		projectKey = m.issue.Project
	}
	loader := func(query string, token int) tea.Cmd {
		svc := m.svc
		pk := projectKey
		return func() tea.Msg {
			ts, err := svc.ListIssueTypes(pk)
			items := make([]PickerItem, 0, len(ts))
			for _, t := range ts {
				items = append(items, PickerItem{
					Label: t.Name, Sub: t.Description, Value: t.Name,
				})
			}
			return pickerLoadedMsg{Token: token, Items: items, Err: err}
		}
	}
	p := NewAsyncPicker("issuetype", "Change type of "+m.key, "filter…", loader)
	p.SetSize(m.width, m.height)
	m.modeReturn = m.mode
	m.mode = modePicker
	m.picker = p
	return p.Init()
}

// openSprintPicker shows active+future sprints across the issue's
// project, plus a "(backlog)" entry to remove the issue from any
// sprint. Sprint listing requires walking all Scrum boards for the
// project so it's cheaper to do once and let static filter take over.
func (m *issueModel) openSprintPicker() tea.Cmd {
	projectKey := ""
	if m.issue != nil {
		projectKey = m.issue.Project
	}
	loader := func(query string, token int) tea.Cmd {
		svc := m.svc
		pk := projectKey
		return func() tea.Msg {
			items := []PickerItem{
				{Label: "(backlog)", Sub: "remove from sprint", Value: 0},
			}
			sps, err := svc.ListProjectSprints(pk, "active,future")
			for _, sp := range sps {
				items = append(items, PickerItem{
					Label: sp.Name, Sub: sp.State, Value: sp.ID,
				})
			}
			return pickerLoadedMsg{Token: token, Items: items, Err: err}
		}
	}
	p := NewAsyncPicker("sprint", "Move "+m.key+" to sprint…", "filter…", loader)
	p.SetSize(m.width, m.height)
	m.modeReturn = m.mode
	m.mode = modePicker
	m.picker = p
	return p.Init()
}

// openLabelsPicker opens a multi-select chip editor over the
// instance's label catalogue, pre-selecting the issue's current
// labels. Free-text is enabled so the user can create a brand-new
// label by typing it (Jira labels aren't gated by a catalogue).
func (m *issueModel) openLabelsPicker() tea.Cmd {
	loader := func(query string, token int) tea.Cmd {
		svc := m.svc
		return func() tea.Msg {
			labels, err := svc.ListLabels(query, 50)
			items := make([]PickerItem, 0, len(labels))
			for _, l := range labels {
				items = append(items, PickerItem{Label: l, Value: l})
			}
			return pickerLoadedMsg{Token: token, Items: items, Err: err}
		}
	}
	p := NewAsyncPicker("labels", "Labels for "+m.key, "type to filter / create…", loader)
	p.SetSize(m.width, m.height)
	pre := []any{}
	if m.issue != nil {
		for _, l := range m.issue.Labels {
			pre = append(pre, l)
		}
	}
	p.EnableMultiSelect(pre)
	p.EnableFreeText()
	m.modeReturn = m.mode
	m.mode = modePicker
	m.picker = p
	return p.Init()
}

// openComponentsPicker opens a multi-select chip editor over the
// project's component catalogue. Components must exist in the project
// (free-text creation isn't available — that needs project-admin
// permission and a different endpoint).
func (m *issueModel) openComponentsPicker() tea.Cmd {
	projectKey := ""
	if m.issue != nil {
		projectKey = m.issue.Project
	}
	loader := func(query string, token int) tea.Cmd {
		svc := m.svc
		pk := projectKey
		return func() tea.Msg {
			comps, err := svc.ListProjectComponents(pk)
			items := make([]PickerItem, 0, len(comps))
			for _, c := range comps {
				items = append(items, PickerItem{
					Label: c.Name, Sub: c.Description, Value: c.Name,
				})
			}
			return pickerLoadedMsg{Token: token, Items: items, Err: err}
		}
	}
	p := NewAsyncPicker("components", "Components for "+m.key, "filter…", loader)
	p.SetSize(m.width, m.height)
	pre := []any{}
	if m.issue != nil {
		for _, c := range m.issue.Components {
			pre = append(pre, c)
		}
	}
	p.EnableMultiSelect(pre)
	m.modeReturn = m.mode
	m.mode = modePicker
	m.picker = p
	return p.Init()
}

// openFixVersionsPicker opens a multi-select chip editor over the
// project's fix-version catalogue, pre-selecting the issue's current
// fix versions.
func (m *issueModel) openFixVersionsPicker() tea.Cmd {
	projectKey := ""
	if m.issue != nil {
		projectKey = m.issue.Project
	}
	loader := func(query string, token int) tea.Cmd {
		svc := m.svc
		pk := projectKey
		return func() tea.Msg {
			vs, err := svc.ListProjectVersions(pk)
			items := make([]PickerItem, 0, len(vs))
			for _, v := range vs {
				items = append(items, PickerItem{
					Label: v.Name, Sub: v.Description, Value: v.Name,
				})
			}
			return pickerLoadedMsg{Token: token, Items: items, Err: err}
		}
	}
	p := NewAsyncPicker("fixversions", "Fix versions for "+m.key, "filter…", loader)
	p.SetSize(m.width, m.height)
	pre := []any{}
	if m.issue != nil {
		for _, v := range m.issue.FixVersions {
			pre = append(pre, v)
		}
	}
	p.EnableMultiSelect(pre)
	m.modeReturn = m.mode
	m.mode = modePicker
	m.picker = p
	return p.Init()
}

// openDueDatePicker opens a free-text picker (no items) so the user
// can type a YYYY-MM-DD date. The picker's "+ Add 'X'" row commits
// X as the new due date. Empty input commits nothing — to clear, the
// user types "clear" (handled in applyPicker) or "0".
func (m *issueModel) openDueDatePicker() tea.Cmd {
	current := ""
	if m.issue != nil {
		current = m.issue.DueDate
	}
	loader := func(query string, token int) tea.Cmd {
		return func() tea.Msg {
			items := []PickerItem{
				{Label: "(clear)", Sub: "remove due date", Value: ""},
			}
			if current != "" {
				items = append(items, PickerItem{
					Label: current, Sub: "current value", Value: current,
				})
			}
			return pickerLoadedMsg{Token: token, Items: items}
		}
	}
	p := NewAsyncPicker("duedate", "Due date for "+m.key+" (YYYY-MM-DD)", "YYYY-MM-DD…", loader)
	p.SetSize(m.width, m.height)
	p.EnableFreeText()
	m.modeReturn = m.mode
	m.mode = modePicker
	m.picker = p
	return p.Init()
}

// openStoryPointsPicker opens a free-text picker for entering a
// numeric story-point estimate. Common Fibonacci values are
// pre-loaded as quick-pick rows.
func (m *issueModel) openStoryPointsPicker() tea.Cmd {
	loader := func(query string, token int) tea.Cmd {
		return func() tea.Msg {
			// Conventional Fibonacci-ish scale used by most teams.
			items := []PickerItem{
				{Label: "0", Sub: "no estimate", Value: "0"},
				{Label: "0.5", Value: "0.5"},
				{Label: "1", Value: "1"},
				{Label: "2", Value: "2"},
				{Label: "3", Value: "3"},
				{Label: "5", Value: "5"},
				{Label: "8", Value: "8"},
				{Label: "13", Value: "13"},
				{Label: "21", Value: "21"},
			}
			return pickerLoadedMsg{Token: token, Items: items}
		}
	}
	p := NewAsyncPicker("storypoints", "Story points for "+m.key, "type a number…", loader)
	p.SetSize(m.width, m.height)
	p.EnableFreeText()
	m.modeReturn = m.mode
	m.mode = modePicker
	m.picker = p
	return p.Init()
}

// pendingLink carries link-type + direction between the two-step
// "add link" picker flow (type picker → target picker).
type pendingLink struct {
	typeName  string // canonical link-type name
	direction string // "outward" or "inward"
	verb      string // human verb shown in status line
}

// openLinkTypePicker is step 1 of the "add link" flow: pick a link
// type. Each catalogue type produces two rows — outward and inward —
// so the user picks the verb that reads naturally ("blocks" vs "is
// blocked by"). Selection chains into the target-issue picker.
func (m *issueModel) openLinkTypePicker() tea.Cmd {
	loader := func(query string, token int) tea.Cmd {
		svc := m.svc
		return func() tea.Msg {
			ts, err := svc.ListIssueLinkTypes()
			items := make([]PickerItem, 0, len(ts)*2)
			for _, t := range ts {
				if t.Outward != "" {
					items = append(items, PickerItem{
						Label: t.Outward,
						Sub:   t.Name,
						Value: pendingLink{typeName: t.Name, direction: "outward", verb: t.Outward},
					})
				}
				if t.Inward != "" && !strings.EqualFold(t.Inward, t.Outward) {
					items = append(items, PickerItem{
						Label: t.Inward,
						Sub:   t.Name,
						Value: pendingLink{typeName: t.Name, direction: "inward", verb: t.Inward},
					})
				}
			}
			return pickerLoadedMsg{Token: token, Items: items, Err: err}
		}
	}
	p := NewAsyncPicker("linktype", "Link "+m.key+" — pick a relationship", "filter…", loader)
	p.SetSize(m.width, m.height)
	m.modeReturn = m.mode
	m.mode = modePicker
	m.picker = p
	return p.Init()
}

// openLinkTargetPicker is step 2: search for the issue to link to.
// Driven by JQL (text → fuzzy substring against summary/key) so the
// user can paste a key or just type a few characters.
func (m *issueModel) openLinkTargetPicker() tea.Cmd {
	myKey := m.key
	loader := func(query string, token int) tea.Cmd {
		svc := m.svc
		return func() tea.Msg {
			query = strings.TrimSpace(query)
			var jql string
			switch {
			case query == "":
				// Default: most-recent issues in the same project,
				// excluding self so users can't link to themselves.
				jql = fmt.Sprintf("key != %s ORDER BY updated DESC", myKey)
			case looksLikeIssueKey(query):
				jql = "key = " + strings.ToUpper(query)
			default:
				escaped := strings.ReplaceAll(query, `"`, `\"`)
				jql = fmt.Sprintf(
					`(summary ~ "%s" OR text ~ "%s") AND key != %s ORDER BY updated DESC`,
					escaped, escaped, myKey,
				)
			}
			issues, err := svc.SearchIssues(api.SearchInput{JQL: jql, MaxResults: 25})
			items := make([]PickerItem, 0, len(issues))
			for _, iss := range issues {
				items = append(items, PickerItem{
					Label: fmt.Sprintf("%s · %s", iss.Key, iss.Summary),
					Sub:   strings.TrimSpace(iss.Status + "  " + iss.Assignee),
					Value: iss.Key,
				})
			}
			return pickerLoadedMsg{Token: token, Items: items, Err: err}
		}
	}
	prompt := "Link target — type key (PROJ-123) or summary…"
	if m.pendingLinkType != nil {
		prompt = m.key + " " + m.pendingLinkType.verb + " …"
	}
	p := NewAsyncPicker("linktarget", prompt, "PROJ-123 or text…", loader)
	p.SetSize(m.width, m.height)
	m.modeReturn = m.mode
	m.mode = modePicker
	m.picker = p
	return p.Init()
}

// watchersForPickerMsg carries the result of the pre-fetch
// openWatchersPicker fires before constructing its multi-select
// picker. The current watcher list isn't on the loaded Issue (only
// Watching + WatchCount are), so we have to round-trip the API
// before we can preselect rows.
type watchersForPickerMsg struct {
	current []api.User
	err     error
}

// openWatchersPicker pre-fetches the issue's current watchers,
// then (in Update → showWatchersPicker) constructs a multi-select
// user picker with those watchers preselected. Returning the
// fetch as a Cmd keeps the UI responsive while the API call is
// in flight.
func (m *issueModel) openWatchersPicker() tea.Cmd {
	if m.issue == nil {
		return nil
	}
	key := m.key
	svc := m.svc
	m.loading++
	return tea.Batch(m.spinner.Tick, func() tea.Msg {
		users, err := svc.ListWatchers(key)
		return watchersForPickerMsg{current: users, err: err}
	})
}

// showWatchersPicker constructs the watchers multi-select picker
// with the supplied current set preselected. Items are looked up
// async via SearchUsers as the user types; the current watchers
// are seeded into every result page so they remain de-selectable
// even when the search wouldn't otherwise return them.
func (m *issueModel) showWatchersPicker(current []api.User) tea.Cmd {
	userValue := func(u api.User) string {
		if u.AccountID != "" {
			return u.AccountID
		}
		return u.Name
	}
	pre := []any{}
	seedItems := make([]PickerItem, 0, len(current))
	for _, u := range current {
		v := userValue(u)
		if v == "" {
			continue
		}
		pre = append(pre, v)
		seedItems = append(seedItems, PickerItem{
			Label: u.DisplayName,
			Sub:   strings.TrimSpace(u.Name + "  " + u.Email),
			Value: v,
		})
	}
	svc := m.svc
	loader := func(query string, token int) tea.Cmd {
		return func() tea.Msg {
			users, err := svc.SearchUsers(query, 25)
			items := append([]PickerItem(nil), seedItems...)
			seen := map[string]bool{}
			for _, it := range items {
				seen[fmt.Sprint(it.Value)] = true
			}
			for _, u := range users {
				v := userValue(u)
				if v == "" || seen[v] {
					continue
				}
				seen[v] = true
				items = append(items, PickerItem{
					Label: u.DisplayName,
					Sub:   strings.TrimSpace(u.Name + "  " + u.Email),
					Value: v,
				})
			}
			return pickerLoadedMsg{Token: token, Items: items, Err: err}
		}
	}
	p := NewAsyncPicker("watchers", "Watchers for "+m.key, "type to find users…", loader)
	p.SetSize(m.width, m.height)
	p.EnableMultiSelect(pre)
	m.modeReturn = m.mode
	m.mode = modePicker
	m.picker = p
	return p.Init()
}

// openUploadAttachmentPicker opens a free-text picker the user can
// type a file path into. Submitting "" cancels (free-text picker
// returns empty Value on Enter with no input).
func (m *issueModel) openUploadAttachmentPicker() tea.Cmd {
	loader := func(query string, token int) tea.Cmd {
		return func() tea.Msg {
			return pickerLoadedMsg{Token: token}
		}
	}
	p := NewAsyncPicker("attachfile", "Upload to "+m.key, "/path/to/file…", loader)
	p.SetSize(m.width, m.height)
	p.EnableFreeText()
	m.modeReturn = m.mode
	m.mode = modePicker
	m.picker = p
	return p.Init()
}

// toggleWatchCmd flips the current user's watch state on this
// issue. Reloads the issue afterwards so the eye chip updates.
func (m *issueModel) toggleWatchCmd() tea.Cmd {
	if m.issue == nil {
		return nil
	}
	m.loading++
	if m.issue.Watching {
		return tea.Batch(m.spinner.Tick, m.runAction("unwatched", "issue", func() error {
			return m.svc.RemoveWatcher(m.key, "")
		}))
	}
	return tea.Batch(m.spinner.Tick, m.runAction("watching", "issue", func() error {
		return m.svc.AddWatcher(m.key, "")
	}))
}

// looksLikeIssueKey returns true for strings that match the canonical
// PROJ-123 form. We use it to short-circuit JQL "summary ~ ..." into
// the much faster "key = PROJ-123" lookup.
func looksLikeIssueKey(s string) bool {
	s = strings.TrimSpace(strings.ToUpper(s))
	dash := strings.Index(s, "-")
	if dash <= 0 || dash == len(s)-1 {
		return false
	}
	for _, r := range s[:dash] {
		if r < 'A' || r > 'Z' {
			return false
		}
	}
	for _, r := range s[dash+1:] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// applyPicker dispatches a finished picker's value to the right API
// call based on the picker's purpose. Adding a new picker = adding a
// new case here + a corresponding open*Picker helper.
func (m *issueModel) applyPicker(msg pickerDoneMsg) (tea.Model, tea.Cmd) {
	switch msg.Purpose {
	case "assignee":
		val, _ := msg.Value.(string)
		label := "unassigned"
		if val != "" {
			label = "assigned to " + val
		}
		m.loading++
		return m, tea.Batch(m.spinner.Tick, m.runAction(label, "issue", func() error {
			return m.svc.AssignIssue(m.key, val)
		}))
	case "priority":
		val, _ := msg.Value.(string)
		label := "priority cleared"
		if val != "" {
			label = "priority → " + val
		}
		m.loading++
		return m, tea.Batch(m.spinner.Tick, m.runAction(label, "issue", func() error {
			return m.svc.UpdatePriority(m.key, val)
		}))
	case "issuetype":
		val, _ := msg.Value.(string)
		if val == "" {
			m.status = "✗ issue type cannot be empty"
			return m, nil
		}
		m.loading++
		return m, tea.Batch(m.spinner.Tick, m.runAction("type → "+val, "issue", func() error {
			return m.svc.UpdateIssueType(m.key, val)
		}))
	case "sprint":
		id, _ := msg.Value.(int)
		label := "moved to backlog"
		if id > 0 {
			label = fmt.Sprintf("moved to sprint %d", id)
		}
		m.loading++
		return m, tea.Batch(m.spinner.Tick, m.runAction(label, "issue", func() error {
			return m.svc.MoveIssueToSprint(m.key, id)
		}))
	case "labels":
		labels := stringSliceFromAny(msg.Values)
		m.loading++
		return m, tea.Batch(m.spinner.Tick, m.runAction(
			fmt.Sprintf("labels updated (%d)", len(labels)), "issue", func() error {
				return m.svc.UpdateLabels(m.key, labels)
			}))
	case "components":
		comps := stringSliceFromAny(msg.Values)
		m.loading++
		return m, tea.Batch(m.spinner.Tick, m.runAction(
			fmt.Sprintf("components updated (%d)", len(comps)), "issue", func() error {
				return m.svc.UpdateComponents(m.key, comps)
			}))
	case "fixversions":
		versions := stringSliceFromAny(msg.Values)
		m.loading++
		return m, tea.Batch(m.spinner.Tick, m.runAction(
			fmt.Sprintf("fix versions updated (%d)", len(versions)), "issue", func() error {
				return m.svc.UpdateFixVersions(m.key, versions)
			}))
	case "duedate":
		date, _ := msg.Value.(string)
		date = strings.TrimSpace(date)
		// Validate the format up front so we surface a friendly
		// error instead of letting Jira's "expected ISO 8601" through.
		if date != "" && !isValidDueDate(date) {
			m.status = "✗ due date must be YYYY-MM-DD"
			return m, nil
		}
		label := "due date cleared"
		if date != "" {
			label = "due " + date
		}
		m.loading++
		return m, tea.Batch(m.spinner.Tick, m.runAction(label, "issue", func() error {
			return m.svc.UpdateDueDate(m.key, date)
		}))
	case "storypoints":
		raw, _ := msg.Value.(string)
		raw = strings.TrimSpace(raw)
		points, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			m.status = "✗ story points must be a number"
			return m, nil
		}
		m.loading++
		return m, tea.Batch(m.spinner.Tick, m.runAction(
			fmt.Sprintf("story points → %g", points), "issue", func() error {
				return m.svc.UpdateStoryPoints(m.key, points)
			}))
	case "attachfile":
		path, _ := msg.Value.(string)
		path = strings.TrimSpace(path)
		// Expand a leading ~ so users can paste paths copied from
		// their shell history without surprise.
		if strings.HasPrefix(path, "~/") {
			if home, err := os.UserHomeDir(); err == nil {
				path = home + path[1:]
			}
		}
		if path == "" {
			m.status = "✗ no file path entered"
			return m, nil
		}
		key := m.key
		svc := m.svc
		// Switch to the attachments tab so the user sees the new
		// file appear once the post-upload reload lands.
		m.mode = modeAttachments
		m.modeReturn = modeAttachments
		m.loading++
		return m, tea.Batch(m.spinner.Tick, m.runAction(
			"uploaded "+filepath.Base(path), "issue", func() error {
				_, err := svc.AddAttachment(key, path)
				return err
			}))
	case "watchers":
		desired := stringSliceFromAny(msg.Values)
		key := m.key
		svc := m.svc
		m.loading++
		return m, tea.Batch(m.spinner.Tick, m.runAction(
			fmt.Sprintf("watchers updated (%d)", len(desired)), "issue", func() error {
				// Re-fetch the current set inside the action so the
				// diff is computed against fresh server-side state
				// (rather than the snapshot taken when the picker
				// opened, which could be stale by now).
				current, err := svc.ListWatchers(key)
				if err != nil {
					return err
				}
				userValue := func(u api.User) string {
					if u.AccountID != "" {
						return u.AccountID
					}
					return u.Name
				}
				currentSet := map[string]bool{}
				for _, u := range current {
					if v := userValue(u); v != "" {
						currentSet[v] = true
					}
				}
				desiredSet := map[string]bool{}
				for _, v := range desired {
					desiredSet[v] = true
				}
				for v := range desiredSet {
					if !currentSet[v] {
						if err := svc.AddWatcher(key, v); err != nil {
							return err
						}
					}
				}
				for v := range currentSet {
					if !desiredSet[v] {
						if err := svc.RemoveWatcher(key, v); err != nil {
							return err
						}
					}
				}
				return nil
			}))
	case "linktype":
		pl, ok := msg.Value.(pendingLink)
		if !ok {
			return m, nil
		}
		m.pendingLinkType = &pl
		// Chain straight into the target picker without giving the
		// user a chance to do anything else in between.
		return m, m.openLinkTargetPicker()
	case "linktarget":
		target, _ := msg.Value.(string)
		target = strings.TrimSpace(strings.ToUpper(target))
		if target == "" || m.pendingLinkType == nil {
			m.pendingLinkType = nil
			return m, nil
		}
		pl := *m.pendingLinkType
		m.pendingLinkType = nil
		// Switch the user to the Links tab so they see the new link
		// land once the post-add fetchLinks completes.
		m.mode = modeLinks
		m.modeReturn = modeLinks
		m.loading++
		return m, tea.Batch(m.spinner.Tick, m.runAction(
			fmt.Sprintf("linked %s → %s (%s)", m.key, target, pl.verb),
			"links", func() error {
				return m.svc.AddIssueLink(m.key, target, pl.typeName, pl.direction)
			}))
	}
	return m, nil
}

// isValidDueDate enforces the YYYY-MM-DD form required by Jira.
func isValidDueDate(s string) bool {
	_, err := time.Parse("2006-01-02", s)
	return err == nil
}

// stringSliceFromAny converts a picker's []any value list to a
// []string, dropping any non-string entries.
func stringSliceFromAny(vs []any) []string {
	out := make([]string, 0, len(vs))
	for _, v := range vs {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// ---------- key map ----------

type issueKeys struct {
	Up, Down, Enter, Back, Quit, Help, Settings key.Binding

	TabDesc, TabComments, TabLinks, TabAttachments, TabTransitions key.Binding

	OpenBrowser, EditDescription, EditSummary, NewComment key.Binding
	AssignMe, AssignPick, Unassign                        key.Binding
	PickPriority, PickType, PickSprint                    key.Binding
	EditLabels, EditComponents                            key.Binding
	EditFixVersions, EditDueDate, EditStoryPoints         key.Binding
	AddLink, RemoveLink                                   key.Binding
	ToggleWatch, EditWatchers                             key.Binding
	UploadAttachment, DeleteAttachment                    key.Binding
	EditComment, DeleteComment, ConfirmYes, ConfirmNo     key.Binding
}

func defaultIssueKeys() issueKeys {
	return issueKeys{
		Up:    key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "up")),
		Down:  key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "down")),
		Enter: key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "apply")),
		Back:  key.NewBinding(key.WithKeys("esc", "h"), key.WithHelp("esc/h", "back")),
		Quit:     key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit")),
		Help:     key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
		Settings: key.NewBinding(key.WithKeys(","), key.WithHelp(",", "settings")),

		TabDesc:        key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "description")),
		TabComments:    key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "comments")),
		TabLinks:       key.NewBinding(key.WithKeys("l"), key.WithHelp("l", "links")),
		TabAttachments: key.NewBinding(key.WithKeys("f"), key.WithHelp("f", "files")),
		TabTransitions: key.NewBinding(key.WithKeys("t"), key.WithHelp("t", "transitions")),

		OpenBrowser:     key.NewBinding(key.WithKeys("o"), key.WithHelp("o", "browser")),
		EditDescription: key.NewBinding(key.WithKeys("e"), key.WithHelp("e", "edit desc")),
		EditSummary:     key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "edit summary")),
		AssignMe:        key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "assign me")),
		AssignPick:      key.NewBinding(key.WithKeys("A"), key.WithHelp("A", "assign…")),
		Unassign:        key.NewBinding(key.WithKeys("U"), key.WithHelp("U", "unassign")),
		PickPriority:    key.NewBinding(key.WithKeys("p"), key.WithHelp("p", "priority…")),
		PickType:        key.NewBinding(key.WithKeys("T"), key.WithHelp("T", "type…")),
		PickSprint:      key.NewBinding(key.WithKeys("S"), key.WithHelp("S", "sprint…")),
		EditLabels:      key.NewBinding(key.WithKeys("L"), key.WithHelp("L", "labels…")),
		EditComponents:  key.NewBinding(key.WithKeys("C"), key.WithHelp("C", "components…")),
		EditFixVersions: key.NewBinding(key.WithKeys("V"), key.WithHelp("V", "fix versions…")),
		EditDueDate:     key.NewBinding(key.WithKeys("B"), key.WithHelp("B", "due-by date…")),
		EditStoryPoints: key.NewBinding(key.WithKeys("#"), key.WithHelp("#", "story points…")),
		AddLink:         key.NewBinding(key.WithKeys("K"), key.WithHelp("K", "add link…")),
		RemoveLink:      key.NewBinding(key.WithKeys("X"), key.WithHelp("X", "remove link")),
		ToggleWatch:     key.NewBinding(key.WithKeys("w"), key.WithHelp("w", "watch toggle")),
		EditWatchers:    key.NewBinding(key.WithKeys("W"), key.WithHelp("W", "watchers…")),
		UploadAttachment: key.NewBinding(key.WithKeys("F"), key.WithHelp("F", "upload file…")),
		DeleteAttachment: key.NewBinding(key.WithKeys("D"), key.WithHelp("D", "delete file")),
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
		{k.TabDesc, k.TabComments, k.TabLinks, k.TabAttachments, k.TabTransitions},
		{k.AssignMe, k.AssignPick, k.Unassign, k.EditSummary, k.EditDescription, k.OpenBrowser},
		{k.PickPriority, k.PickType, k.PickSprint, k.EditLabels, k.EditComponents},
		{k.EditFixVersions, k.EditDueDate, k.EditStoryPoints},
		{k.AddLink, k.RemoveLink, k.ToggleWatch, k.EditWatchers},
		{k.UploadAttachment, k.DeleteAttachment, k.OpenBrowser},
		{k.NewComment, k.EditComment, k.DeleteComment},
		{k.Up, k.Down, k.Enter, k.Help, k.Settings, k.Back, k.Quit},
	}
}
