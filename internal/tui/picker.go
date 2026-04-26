// Package tui — generic searchable picker overlay.
//
// pickerModel is a self-contained sub-model designed to be embedded
// inside any tea.Model that wants to ask the user "pick one of
// these". A picker renders as a centred modal panel: text input on
// top, scrollable list below. The host model:
//
//  1. constructs a picker with NewPicker(...)
//  2. forwards key/window/tick messages while pickerActive is true
//  3. on pickerDoneMsg, reads .Value (or .Cancelled) and routes the
//     result to the right action.
//
// The picker supports two modes:
//
//   - static: caller provides the full slice of items up-front, the
//     picker filters in-process by substring.
//   - async: caller provides a Loader callback the picker invokes
//     (debounced) every time the query changes; results stream back
//     as pickerLoadedMsg.
//
// Keep this file dependency-free of any specific domain (users,
// priorities, …) — instantiate per-use in the host model.
package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// PickerItem is one row in a picker. Sub is rendered dim under Label
// (e.g. "@username · email"). Value is whatever the host model needs
// back when the user picks the row.
type PickerItem struct {
	Label string
	Sub   string
	Value any
}

// PickerLoader is the async source for picker items. It receives the
// current query and a token (the picker's internal version counter).
// Implementations should return a tea.Cmd that produces a
// pickerLoadedMsg. The picker discards results whose token is stale,
// so the loader doesn't need its own cancellation logic.
type PickerLoader func(query string, token int) tea.Cmd

// pickerDoneMsg is sent up to the host when the user picks an item or
// cancels. Purpose lets the host route the result when several
// pickers can be active at different times (assignee vs reporter,
// priority vs labels, …) — set it via NewPicker.
//
// Single-select pickers populate Value. Multi-select pickers
// populate Values (slice of selected Value()s in display order).
type pickerDoneMsg struct {
	Purpose   string
	Value     any
	Values    []any
	Cancelled bool
}

// pickerLoadedMsg carries async-loaded items back into the picker.
// Token is the version this load was started with — stale loads are
// dropped so out-of-order responses can never overwrite fresher
// results.
type pickerLoadedMsg struct {
	Token int
	Items []PickerItem
	Err   error
}

// pickerTickMsg is the debounce trigger for async loaders.
type pickerTickMsg struct{ token int }

type pickerKeys struct {
	Up, Down, Enter, Cancel, PgUp, PgDown key.Binding
	Toggle, Commit                        key.Binding // multi-select only
}

func defaultPickerKeys() pickerKeys {
	return pickerKeys{
		Up:     key.NewBinding(key.WithKeys("up", "ctrl+p")),
		Down:   key.NewBinding(key.WithKeys("down", "ctrl+n")),
		Enter:  key.NewBinding(key.WithKeys("enter")),
		Cancel: key.NewBinding(key.WithKeys("esc", "ctrl+c")),
		PgUp:   key.NewBinding(key.WithKeys("pgup")),
		PgDown: key.NewBinding(key.WithKeys("pgdown")),
		Toggle: key.NewBinding(key.WithKeys(" ", "tab")),
		Commit: key.NewBinding(key.WithKeys("ctrl+s")),
	}
}

// pickerModel drives one picker overlay. Construct via NewPicker.
type pickerModel struct {
	purpose string // routed back via pickerDoneMsg.Purpose
	title   string

	input   textinput.Model
	keys    pickerKeys
	spinner spinner.Model

	// items is the *displayed* slice, after filtering (static) or
	// after the most recent async load. cursor is an index into it.
	items  []PickerItem
	cursor int

	// allItems is the unfiltered source for static-mode pickers;
	// nil for async pickers.
	allItems []PickerItem

	loader      PickerLoader
	debounce    time.Duration
	loadVersion int  // bumped on every keystroke
	loading     bool // shows the spinner while waiting

	// Multi-select mode: space toggles, ctrl-s commits the full set.
	multi bool
	// selected is keyed by the item's Value (formatted via fmt.Sprint
	// for stable comparison across reloads). Order is preserved
	// separately in selectedOrder so we can return Values in the
	// order the user picked them.
	selected      map[string]bool
	selectedOrder []any

	// allowFreeText: if true and the input doesn't match any item,
	// show a synthetic "+ Add 'X'" row at the top. Used by labels.
	allowFreeText bool

	width, height int
	maxRows       int // visible rows in the list

	err error
}

// NewStaticPicker builds a picker with an in-process filter over a
// fixed slice (e.g. priorities, labels you already know). Call
// .SetSize and return .Init() before showing the picker.
func NewStaticPicker(purpose, title, placeholder string, items []PickerItem) *pickerModel {
	in := textinput.New()
	in.Placeholder = placeholder
	in.Prompt = "› "
	in.Focus()
	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("13"))
	return &pickerModel{
		purpose:  purpose,
		title:    title,
		input:    in,
		keys:     defaultPickerKeys(),
		spinner:  sp,
		items:    items,
		allItems: items,
		maxRows:  10,
	}
}

// NewAsyncPicker builds a picker that calls loader on every (debounced)
// keystroke. items shown when the input is empty come from the
// initial loader call fired by Init.
func NewAsyncPicker(purpose, title, placeholder string, loader PickerLoader) *pickerModel {
	p := NewStaticPicker(purpose, title, placeholder, nil)
	p.allItems = nil
	p.loader = loader
	p.debounce = 200 * time.Millisecond
	p.loading = true
	return p
}

// EnableMultiSelect turns the picker into a chip editor: each row
// can be toggled on/off with space, the full set commits with
// ctrl-s. preselected populates the initial selection (in display
// order).
func (p *pickerModel) EnableMultiSelect(preselected []any) {
	p.multi = true
	p.selected = map[string]bool{}
	p.selectedOrder = nil
	for _, v := range preselected {
		k := fmt.Sprint(v)
		if !p.selected[k] {
			p.selected[k] = true
			p.selectedOrder = append(p.selectedOrder, v)
		}
	}
}

// EnableFreeText lets the picker emit a synthetic "+ Add 'X'" row at
// the top when the input doesn't match any existing item. Picking it
// adds X as a new value to the selection (multi mode) or commits X
// directly (single mode). Used by the labels picker.
func (p *pickerModel) EnableFreeText() { p.allowFreeText = true }

// Init kicks off the spinner + the initial load (async pickers only).
func (p *pickerModel) Init() tea.Cmd {
	if p.loader == nil {
		return nil
	}
	p.loadVersion++
	tok := p.loadVersion
	return tea.Batch(p.spinner.Tick, p.loader("", tok))
}

// SetSize updates the modal panel dimensions. Call from the host's
// WindowSizeMsg handler so the overlay tracks resize.
func (p *pickerModel) SetSize(w, h int) {
	p.width, p.height = w, h
	rows := h - 8 // chrome: title + input + borders + footer hint
	if rows < 4 {
		rows = 4
	}
	if rows > 16 {
		rows = 16
	}
	p.maxRows = rows
	iw := w - 8
	if iw < 20 {
		iw = 20
	}
	p.input.Width = iw
}

// Update handles the keys + async/spinner messages routed by the host.
// Returns a pickerDoneMsg-emitting Cmd when the user picks/cancels.
func (p *pickerModel) Update(msg tea.Msg) (tea.Cmd, bool /*done*/) {
	switch m := msg.(type) {

	case spinner.TickMsg:
		var cmd tea.Cmd
		p.spinner, cmd = p.spinner.Update(m)
		if p.loading {
			return cmd, false
		}
		return nil, false

	case pickerTickMsg:
		// Debounce fired — only run the load if this token is still
		// the freshest one (i.e. no further keystrokes since).
		if m.token != p.loadVersion || p.loader == nil {
			return nil, false
		}
		p.loading = true
		return tea.Batch(p.spinner.Tick, p.loader(p.input.Value(), m.token)), false

	case pickerLoadedMsg:
		// Drop stale loads.
		if m.Token != p.loadVersion {
			return nil, false
		}
		p.loading = false
		p.err = m.Err
		p.items = m.Items
		if p.cursor >= len(p.items) {
			p.cursor = 0
		}
		return nil, false

	case tea.KeyMsg:
		switch {
		case key.Matches(m, p.keys.Cancel):
			return func() tea.Msg {
				return pickerDoneMsg{Purpose: p.purpose, Cancelled: true}
			}, true
		case key.Matches(m, p.keys.Commit):
			if p.multi {
				return func() tea.Msg {
					return pickerDoneMsg{Purpose: p.purpose, Values: p.commitValues()}
				}, true
			}
		case key.Matches(m, p.keys.Toggle):
			view := p.itemsForView()
			if p.multi && p.cursor >= 0 && p.cursor < len(view) {
				p.toggleAtCursor()
				return nil, false
			}
		case key.Matches(m, p.keys.Enter):
			view := p.itemsForView()
			if p.cursor >= 0 && p.cursor < len(view) {
				v := view[p.cursor].Value
				if p.multi {
					// In multi-select, Enter toggles the row; commit
					// the whole set via ctrl-s. Single-select Enter
					// commits the picked value directly.
					p.toggleAtCursor()
					return nil, false
				}
				return func() tea.Msg {
					return pickerDoneMsg{Purpose: p.purpose, Value: v}
				}, true
			}
			return nil, false
		case key.Matches(m, p.keys.Up):
			if p.cursor > 0 {
				p.cursor--
			}
			return nil, false
		case key.Matches(m, p.keys.Down):
			if p.cursor < len(p.itemsForView())-1 {
				p.cursor++
			}
			return nil, false
		case key.Matches(m, p.keys.PgUp):
			p.cursor -= p.maxRows
			if p.cursor < 0 {
				p.cursor = 0
			}
			return nil, false
		case key.Matches(m, p.keys.PgDown):
			p.cursor += p.maxRows
			if max := len(p.itemsForView()) - 1; p.cursor > max {
				p.cursor = max
			}
			return nil, false
		}

		// Forward to the text input, then re-filter / re-load.
		prev := p.input.Value()
		var cmd tea.Cmd
		p.input, cmd = p.input.Update(m)
		if p.input.Value() == prev {
			return cmd, false
		}
		p.cursor = 0

		if p.loader != nil {
			p.loadVersion++
			tok := p.loadVersion
			tick := tea.Tick(p.debounce, func(time.Time) tea.Msg {
				return pickerTickMsg{token: tok}
			})
			return tea.Batch(cmd, tick), false
		}
		// Static: filter in-process.
		p.items = filterPickerItems(p.allItems, p.input.Value())
		return cmd, false
	}
	return nil, false
}

// toggleAtCursor flips the selected state of the item under the
// cursor. In free-text mode picking the synthetic "+ Add 'X'" row
// inserts X as a new item *and* selects it.
func (p *pickerModel) toggleAtCursor() {
	view := p.itemsForView()
	if p.cursor < 0 || p.cursor >= len(view) {
		return
	}
	it := view[p.cursor]
	k := fmt.Sprint(it.Value)
	if p.selected[k] {
		delete(p.selected, k)
		for i, v := range p.selectedOrder {
			if fmt.Sprint(v) == k {
				p.selectedOrder = append(p.selectedOrder[:i], p.selectedOrder[i+1:]...)
				break
			}
		}
	} else {
		p.selected[k] = true
		p.selectedOrder = append(p.selectedOrder, it.Value)
	}
}

// commitValues returns the current selection in stable order. The
// raw selectedOrder slice already maintains "first-toggled-first".
func (p *pickerModel) commitValues() []any {
	out := make([]any, 0, len(p.selectedOrder))
	for _, v := range p.selectedOrder {
		// Filter out anything that's been toggled off since.
		if p.selected[fmt.Sprint(v)] {
			out = append(out, v)
		}
	}
	return out
}

// itemsForView is items with the synthetic "+ Add 'X'" row prepended
// when free-text is enabled and the input doesn't already match an
// existing entry.
func (p *pickerModel) itemsForView() []PickerItem {
	if !p.allowFreeText {
		return p.items
	}
	q := strings.TrimSpace(p.input.Value())
	if q == "" {
		return p.items
	}
	for _, it := range p.items {
		if strings.EqualFold(it.Label, q) {
			return p.items
		}
	}
	addRow := PickerItem{
		Label: "+ Add \"" + q + "\"",
		Sub:   "create new",
		Value: q,
	}
	return append([]PickerItem{addRow}, p.items...)
}

// View renders the picker as a centred modal box. The host should
// composite this on top of (or instead of) its own view when the
// picker is active.
func (p *pickerModel) View() string {
	w := p.width
	if w == 0 {
		w = 60
	}
	innerW := w - 4
	if innerW < 24 {
		innerW = 24
	}

	// Header.
	header := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("231")).
		Background(lipgloss.Color("57")).Padding(0, 1).Render(p.title)

	// Input row.
	inputLine := p.input.View()

	// List rows.
	view := p.itemsForView()
	var rows []string
	if p.loading && len(view) == 0 {
		rows = append(rows, paneMutedStyle.Render(p.spinner.View()+" loading…"))
	} else if len(view) == 0 {
		rows = append(rows, paneMutedStyle.Render("(no matches)"))
	} else {
		start := 0
		if p.cursor >= p.maxRows {
			start = p.cursor - p.maxRows + 1
		}
		end := start + p.maxRows
		if end > len(view) {
			end = len(view)
		}
		for i := start; i < end; i++ {
			selected := false
			if p.multi {
				selected = p.selected[fmt.Sprint(view[i].Value)]
			}
			rows = append(rows, renderPickerRow(view[i], i == p.cursor, selected, p.multi, innerW))
		}
		if end < len(view) {
			rows = append(rows, paneMutedStyle.Render(
				fmt.Sprintf("  … %d more", len(view)-end)))
		}
	}

	hint := paneMutedStyle.Render("↑/↓ move · enter pick · esc cancel · type to filter")
	if p.multi {
		hint = paneMutedStyle.Render(
			fmt.Sprintf("space toggle · ⌃s save (%d selected) · esc cancel · type to filter",
				len(p.commitValues())))
	}
	body := strings.Join([]string{header, "", inputLine, "", strings.Join(rows, "\n"), "", hint}, "\n")

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("141")).
		Padding(1, 2).
		Width(innerW + 2).
		Render(body)
	return box
}

// renderPickerRow paints one picker row with cursor + optional
// checkbox prefix (multi-select) and a dim sub-label trailing the
// main label.
func renderPickerRow(it PickerItem, isCursor, isChecked, multi bool, w int) string {
	mark := "  "
	main := lipgloss.NewStyle()
	sub := paneMutedStyle
	if isCursor {
		mark = lipgloss.NewStyle().Foreground(lipgloss.Color("11")).Bold(true).Render("▶ ")
		main = main.Bold(true).Foreground(lipgloss.Color("231"))
		sub = sub.Foreground(lipgloss.Color("245"))
	}
	check := ""
	if multi {
		if isChecked {
			check = lipgloss.NewStyle().Foreground(lipgloss.Color("10")).Render("[✓] ")
		} else {
			check = paneMutedStyle.Render("[ ] ")
		}
	}
	subText := ""
	if it.Sub != "" {
		subText = "  " + sub.Render("· "+it.Sub)
	}
	line := mark + check + main.Render(it.Label) + subText
	if lipgloss.Width(line) > w {
		runes := []rune(line)
		if len(runes) > w {
			line = string(runes[:w-1]) + "…"
		}
	}
	return line
}

// filterPickerItems returns items whose Label or Sub contains q
// (case-insensitive). Empty q passes everything through.
func filterPickerItems(items []PickerItem, q string) []PickerItem {
	q = strings.TrimSpace(strings.ToLower(q))
	if q == "" {
		return items
	}
	out := make([]PickerItem, 0, len(items))
	for _, it := range items {
		if strings.Contains(strings.ToLower(it.Label), q) ||
			strings.Contains(strings.ToLower(it.Sub), q) {
			out = append(out, it)
		}
	}
	return out
}


