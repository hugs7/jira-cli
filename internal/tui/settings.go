// Settings overlay for jr — mirrors bb's settings.Model so a user
// flipping between the two CLIs lands on the same surface for
// universal toggles (theme, …). Opened with `,` from any host TUI;
// esc closes; enter / space toggles the highlighted item.

package tui

import (
	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// settingsItem is one row in the settings list. ValueFn renders the
// current value as a chip-suffixed string; ToggleFn flips it (and
// persists via config).
type settingsItem struct {
	Label    string
	Hint     string
	ValueFn  func() string
	ToggleFn func() error
}

func (i settingsItem) FilterValue() string { return i.Label }
func (i settingsItem) Title() string {
	v := ""
	if i.ValueFn != nil {
		v = i.ValueFn()
	}
	return i.Label + "  " + titleChip.Render(v)
}
func (i settingsItem) Description() string { return i.Hint }

// universalSettingsItems is the list of toggles every TUI exposes.
// Currently just Theme cycling — kept as a function so future entries
// can be added in one place and picked up by every host model.
func universalSettingsItems() []list.Item {
	return []list.Item{
		settingsItem{
			Label:   "Theme",
			Hint:    "Cycle through built-in colour themes (default · dracula · solarized-dark · nord · 3270)",
			ValueFn: func() string { return currentTheme.Name },
			ToggleFn: func() error {
				cycleTheme()
				return nil
			},
		},
	}
}

// settingsKeymap is the single binding the overlay needs externally
// (toggle). Open / close keys are owned by the host TUI so each can
// scope esc / "," to its own dispatch.
type settingsKeymap struct {
	Toggle key.Binding
}

func defaultSettingsKeymap() settingsKeymap {
	return settingsKeymap{
		Toggle: key.NewBinding(
			key.WithKeys("enter", " "),
			key.WithHelp("enter/space", "toggle"),
		),
	}
}

// settingsModel is the lightweight overlay: a list.Model plus a help
// bar. Embed in any host model and route keys to it via Update while
// the overlay is active.
type settingsModel struct {
	list list.Model
	keys settingsKeymap
	help help.Model
	w, h int
}

// newSettings constructs an overlay seeded with the universal items.
func newSettings() settingsModel {
	delegate := list.NewDefaultDelegate()
	l := list.New(universalSettingsItems(), delegate, 0, 0)
	l.Title = "Settings"
	l.SetShowHelp(false)
	l.SetShowStatusBar(false)
	l.Styles.Title = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	return settingsModel{list: l, keys: defaultSettingsKeymap(), help: help.New()}
}

// SetSize resizes the inner list to the given dimensions. Hosts call
// this from their layout so the overlay tracks terminal resizes.
func (m *settingsModel) SetSize(w, h int) {
	m.w, m.h = w, h
	m.list.SetSize(w, h-2) // -2 for the help line
}

// Update routes a tea.Msg through the list. KeyMsgs trigger a toggle
// when they match Keymap.Toggle; everything else falls through to
// list navigation. Returns the (possibly mutated) Model plus any Cmd
// produced by the underlying list.
func (m settingsModel) Update(msg tea.Msg) (settingsModel, tea.Cmd) {
	if km, ok := msg.(tea.KeyMsg); ok {
		if key.Matches(km, m.keys.Toggle) {
			if it, ok := m.list.SelectedItem().(settingsItem); ok && it.ToggleFn != nil {
				_ = it.ToggleFn()
				// Rebuild items so the value chip reflects the new
				// state. Items capture state by closure; re-rendering
				// the list view is what re-evaluates ValueFn.
				idx := m.list.Index()
				m.list.SetItems(universalSettingsItems())
				if idx < len(m.list.Items()) {
					m.list.Select(idx)
				}
			}
			return m, nil
		}
	}
	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

// View renders the overlay body (list + help line).
func (m settingsModel) View() string {
	hint := titleChipDim.Render("enter/space toggles · esc closes")
	return m.list.View() + "\n" + hint
}
