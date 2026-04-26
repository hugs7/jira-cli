package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/hugs7/jira-cli/internal/config"
)

// captureEditorBody opens the user's $EDITOR on a temp file pre-filled
// with `initial` and returns the trimmed body. Comment-style lines (#)
// are stripped so we can include hints without polluting the output.
func captureEditorBody(hint, initial string) (string, error) {
	f, err := os.CreateTemp("", "jr-edit-*-"+sanitize(hint)+".md")
	if err != nil {
		return "", err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if initial != "" {
		if _, err := f.WriteString(initial); err != nil {
			f.Close()
			return "", err
		}
	}
	f.Close()

	editor := config.Get().EditorCmd()
	parts := strings.Fields(editor)
	if len(parts) == 0 {
		return "", fmt.Errorf("no editor configured")
	}
	args := append(parts[1:], tmp)
	cmd := exec.Command(parts[0], args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", err
	}
	data, err := os.ReadFile(tmp)
	if err != nil {
		return "", err
	}
	out := stripComments(string(data))
	return strings.TrimSpace(out), nil
}

// stripComments drops lines starting with "#" or "<!--" so users can
// add hints inside templates without breaking the body.
func stripComments(s string) string {
	var b strings.Builder
	for _, line := range strings.Split(s, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "#") || strings.HasPrefix(t, "<!--") {
			continue
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}

func sanitize(s string) string {
	r := strings.NewReplacer("/", "-", "\\", "-", " ", "_")
	return r.Replace(s)
}
