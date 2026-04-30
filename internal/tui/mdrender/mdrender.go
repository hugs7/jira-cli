// Package mdrender is a tiny shared wrapper around Charm's glamour
// markdown renderer used by the issue TUI to draw description and
// comment bodies. Funnelling them through a single function keeps the
// styling consistent and lets us swap implementations later without
// chasing call-sites.
//
// Mirrors the bb-cli mdrender package so the two CLIs stay aligned.
package mdrender

import (
	"strings"

	"github.com/charmbracelet/glamour"
)

// Render returns body rendered as styled markdown sized for width
// columns. On any error (renderer init failure, parse error, …) we
// return the original body so callers always get something readable
// — a degraded preview is better than an empty one.
//
// The width parameter is the soft-wrap target for code blocks /
// paragraphs; pass the available pane width. width<=0 falls back to
// a sensible default so callers don't need to special-case the very
// first paint before WindowSizeMsg.
func Render(body string, width int) string {
	body = strings.TrimRight(body, "\n")
	if strings.TrimSpace(body) == "" {
		return ""
	}
	if width <= 0 {
		width = 80
	}
	r, err := glamour.NewTermRenderer(
		glamour.WithAutoStyle(),
		glamour.WithWordWrap(width),
	)
	if err != nil {
		return body
	}
	out, err := r.Render(body)
	if err != nil {
		return body
	}
	// Glamour pads the output with a leading + trailing newline by
	// default; trim them so callers can compose the result with
	// their own headers / blank lines without spurious gaps.
	return strings.Trim(out, "\n")
}
