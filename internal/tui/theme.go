// Theme registry for jr — mirrors the bb (bitbucket-cli) theme system
// so a user flipping between the two CLIs gets the same named palettes
// (default, dracula, solarized-dark, nord, 3270) and the same cycling
// behaviour. Themes are registered as Theme structs; Apply rebinds the
// package-level style vars in styles.go so call sites just reference
// titleBadge / titleChip / statusOK etc. without ever needing to know
// about the current theme.

package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/hugs7/jira-cli/internal/config"
)

// Theme is a small palette plus a name. Colours are lipgloss.Color
// values (ANSI index "9" or hex "#ff0000").
type Theme struct {
	Name string

	StatusOK   lipgloss.Color
	StatusErr  lipgloss.Color
	StatusInfo lipgloss.Color

	TitleChip     lipgloss.Color
	TitleChipDim  lipgloss.Color
	TitleChipWarn lipgloss.Color
	TitleBadgeBg  lipgloss.Color
	TitleBadgeFg  lipgloss.Color

	PaneTitle lipgloss.Color
	PaneChip  lipgloss.Color
	PaneMuted lipgloss.Color
}

// builtinThemes is the registry of named themes shipped with jr.
// Add new ones here and they're automatically picked up by the cycler
// and the `theme:` config field.
var builtinThemes = []Theme{
	{
		Name:          "default",
		StatusOK:      lipgloss.Color("10"),
		StatusErr:     lipgloss.Color("9"),
		StatusInfo:    lipgloss.Color("11"),
		TitleChip:     lipgloss.Color("159"),
		TitleChipDim:  lipgloss.Color("245"),
		TitleChipWarn: lipgloss.Color("221"),
		TitleBadgeBg:  lipgloss.Color("57"),
		TitleBadgeFg:  lipgloss.Color("231"),
		PaneTitle:     lipgloss.Color("141"),
		PaneChip:      lipgloss.Color("159"),
		PaneMuted:     lipgloss.Color("8"),
	},
	{
		Name:          "dracula",
		StatusOK:      lipgloss.Color("#50fa7b"),
		StatusErr:     lipgloss.Color("#ff5555"),
		StatusInfo:    lipgloss.Color("#f1fa8c"),
		TitleChip:     lipgloss.Color("#8be9fd"),
		TitleChipDim:  lipgloss.Color("#6272a4"),
		TitleChipWarn: lipgloss.Color("#ffb86c"),
		TitleBadgeBg:  lipgloss.Color("#bd93f9"),
		TitleBadgeFg:  lipgloss.Color("#282a36"),
		PaneTitle:     lipgloss.Color("#bd93f9"),
		PaneChip:      lipgloss.Color("#8be9fd"),
		PaneMuted:     lipgloss.Color("#6272a4"),
	},
	{
		Name:          "solarized-dark",
		StatusOK:      lipgloss.Color("#859900"),
		StatusErr:     lipgloss.Color("#dc322f"),
		StatusInfo:    lipgloss.Color("#b58900"),
		TitleChip:     lipgloss.Color("#2aa198"),
		TitleChipDim:  lipgloss.Color("#586e75"),
		TitleChipWarn: lipgloss.Color("#cb4b16"),
		TitleBadgeBg:  lipgloss.Color("#268bd2"),
		TitleBadgeFg:  lipgloss.Color("#fdf6e3"),
		PaneTitle:     lipgloss.Color("#268bd2"),
		PaneChip:      lipgloss.Color("#2aa198"),
		PaneMuted:     lipgloss.Color("#586e75"),
	},
	{
		Name:          "nord",
		StatusOK:      lipgloss.Color("#a3be8c"),
		StatusErr:     lipgloss.Color("#bf616a"),
		StatusInfo:    lipgloss.Color("#ebcb8b"),
		TitleChip:     lipgloss.Color("#88c0d0"),
		TitleChipDim:  lipgloss.Color("#4c566a"),
		TitleChipWarn: lipgloss.Color("#d08770"),
		TitleBadgeBg:  lipgloss.Color("#5e81ac"),
		TitleBadgeFg:  lipgloss.Color("#eceff4"),
		PaneTitle:     lipgloss.Color("#81a1c1"),
		PaneChip:      lipgloss.Color("#8fbcbb"),
		PaneMuted:     lipgloss.Color("#4c566a"),
	},
	{
		// IBM 3270 / Reflection green-screen tribute. Bright cyan
		// for protected fields, bright green for the operator
		// status line, bright red for errors, bright yellow for
		// attention/warnings — the same palette every Westpac
		// mainframe terminal has shipped since the 80s. Pair with
		// a black terminal background for full effect.
		Name:          "3270",
		StatusOK:      lipgloss.Color("10"), // bright green
		StatusErr:     lipgloss.Color("9"),  // bright red
		StatusInfo:    lipgloss.Color("14"), // bright cyan
		TitleChip:     lipgloss.Color("14"),
		TitleChipDim:  lipgloss.Color("6"),
		TitleChipWarn: lipgloss.Color("11"),
		TitleBadgeBg:  lipgloss.Color("14"),
		TitleBadgeFg:  lipgloss.Color("0"),
		PaneTitle:     lipgloss.Color("14"),
		PaneChip:      lipgloss.Color("10"),
		PaneMuted:     lipgloss.Color("6"),
	},
}

// currentTheme is the in-process active theme. Read by applyTheme on
// init and by the theme-cycle keybinding.
var currentTheme = builtinThemes[0]

// lookupTheme returns the named theme, or the default theme when the
// name is unknown / empty.
func lookupTheme(name string) Theme {
	if name == "" {
		return builtinThemes[0]
	}
	want := strings.ToLower(strings.TrimSpace(name))
	for _, t := range builtinThemes {
		if strings.ToLower(t.Name) == want {
			return t
		}
	}
	return builtinThemes[0]
}

// applyTheme swaps the active theme and rebinds the package-level
// style variables in styles.go so subsequent View() calls pick up the
// new colours without callers needing to refactor every render call.
func applyTheme(t Theme) {
	currentTheme = t

	statusOK = lipgloss.NewStyle().Foreground(t.StatusOK)
	statusErr = lipgloss.NewStyle().Foreground(t.StatusErr)
	statusInfo = lipgloss.NewStyle().Foreground(t.StatusInfo)

	titleChip = lipgloss.NewStyle().Foreground(t.TitleChip)
	titleChipDim = lipgloss.NewStyle().Foreground(t.TitleChipDim)
	titleChipWarn = lipgloss.NewStyle().Foreground(t.TitleChipWarn)
	titleBadge = lipgloss.NewStyle().Bold(true).
		Foreground(t.TitleBadgeFg).Background(t.TitleBadgeBg).Padding(0, 1)

	sep := " • "
	if t.Name == "3270" {
		// CICS panels separate fields with double-bars, not bullets.
		sep = " || "
	}
	titleSep = lipgloss.NewStyle().Foreground(t.TitleChipDim).Render(sep)

	paneTitleStyle = lipgloss.NewStyle().Bold(true).Foreground(t.PaneTitle)
	paneChipStyle = lipgloss.NewStyle().Foreground(t.PaneChip)
	paneMutedStyle = lipgloss.NewStyle().Foreground(t.PaneMuted)
}

// nextTheme returns the theme name following `current` in the cycle,
// wrapping around at the end. Drives the theme-cycle keybinding.
func nextTheme(current string) string {
	for i, t := range builtinThemes {
		if strings.EqualFold(t.Name, current) {
			return builtinThemes[(i+1)%len(builtinThemes)].Name
		}
	}
	return builtinThemes[0].Name
}

// initTheme applies whichever theme the user has configured. Called
// once per TUI launch from each model constructor so the first paint
// already shows the chosen palette.
func initTheme() {
	applyTheme(lookupTheme(config.Get().Theme))
}

// cycleTheme advances to the next theme, persists it, and returns the
// new theme name (for status messages).
func cycleTheme() string {
	next := nextTheme(currentTheme.Name)
	applyTheme(lookupTheme(next))
	_ = config.SetTheme(next)
	return next
}
