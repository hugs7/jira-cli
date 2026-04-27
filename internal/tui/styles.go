package tui

import (
	"os/exec"
	"runtime"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Shared title-bar / chip styling. Mirrors the look bb uses so a user
// flipping between the two CLIs feels at home.
//
// These vars are reassigned by applyTheme() in theme.go on theme
// changes; they MUST stay declared as plain `var` (not `const`) so
// the cycler can rebind them at runtime without every call site
// needing to know.
var (
	titleBarPad    = lipgloss.NewStyle().Padding(0, 1)
	titleBadge     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("231")).Background(lipgloss.Color("57")).Padding(0, 1)
	titleSep       = lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Render(" • ")
	titleChip      = lipgloss.NewStyle().Foreground(lipgloss.Color("159"))
	titleChipDim   = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	titleChipWarn  = lipgloss.NewStyle().Foreground(lipgloss.Color("221"))

	paneTitleStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("141"))
	paneChipStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("159"))
	paneMutedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))

	statusOK   = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	statusErr  = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	statusInfo = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
)

// titleBar composes a uniform header line: a coloured badge with the
// section name, followed by optional context "chips" separated by dim
// bullets. Empty chips are skipped so callers can pass conditional
// strings without worrying about double-separators.
func titleBar(section string, chips ...string) string {
	parts := []string{titleBadge.Render(section)}
	for _, c := range chips {
		if strings.TrimSpace(c) == "" {
			continue
		}
		parts = append(parts, titleSep, c)
	}
	return titleBarPad.Render(strings.Join(parts, ""))
}

// styleStatus paints a status name based on its Jira status category
// key ("new" / "indeterminate" / "done"). Falls back to an unstyled
// run when the category is unknown.
func styleStatus(category string) lipgloss.Style {
	switch category {
	case "new":
		return lipgloss.NewStyle().Foreground(lipgloss.Color("12")) // blue
	case "indeterminate":
		return lipgloss.NewStyle().Foreground(lipgloss.Color("11")) // yellow
	case "done":
		return lipgloss.NewStyle().Foreground(lipgloss.Color("10")) // green
	}
	return lipgloss.NewStyle()
}

// openInBrowser opens a URL in the user's default browser.
func openInBrowser(url string) error {
	var cmd string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		cmd = "open"
	case "windows":
		cmd = "rundll32"
		args = []string{"url.dll,FileProtocolHandler"}
	default:
		cmd = "xdg-open"
	}
	args = append(args, url)
	return exec.Command(cmd, args...).Start()
}
