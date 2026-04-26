// Package gitctx detects Jira context from the current git working
// tree — primarily the issue key encoded in the active branch name —
// and provides small helpers for creating / switching branches when
// starting work on an issue.
package gitctx

import (
	"fmt"
	"os/exec"
	"regexp"
	"strings"
)

// issueKeyRE matches a Jira issue key (PROJECT-123). Project keys are
// uppercase letters and digits, must start with a letter, and are
// joined to a numeric ID by a hyphen. The pattern is anchored on
// non-alphanumeric boundaries so it picks `ABC-12` out of
// `feature/ABC-12-add-thing` but not `XABC-12foo`.
var issueKeyRE = regexp.MustCompile(`(?:^|[^A-Z0-9])([A-Z][A-Z0-9]+-\d+)(?:$|[^A-Za-z0-9])`)

// CurrentBranch returns the active branch name in the current working
// directory's git repository. Works on freshly-initialised repos with
// no commits yet (where `rev-parse --abbrev-ref HEAD` errors out).
func CurrentBranch() (string, error) {
	if out, err := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD").Output(); err == nil {
		b := strings.TrimSpace(string(out))
		if b != "" && b != "HEAD" {
			return b, nil
		}
	}
	// Fallback for unborn branches (no commits yet).
	out, err := exec.Command("git", "symbolic-ref", "--short", "HEAD").Output()
	if err != nil {
		return "", fmt.Errorf("not in a git repository or detached HEAD: %w", err)
	}
	b := strings.TrimSpace(string(out))
	if b == "" {
		return "", fmt.Errorf("not on a named branch")
	}
	return b, nil
}

// IssueKeyFromBranch extracts the first Jira-style issue key from a
// branch name. Returns "" when no key is present.
func IssueKeyFromBranch(branch string) string {
	// Uppercase a copy so we tolerate keys that were lowercased by
	// the user (e.g. `feature/proj-123-thing`).
	upper := strings.ToUpper(branch)
	m := issueKeyRE.FindStringSubmatch(upper)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

// CurrentIssueKey returns the Jira issue key for the active branch,
// or an error when no branch is checked out or no key is present in
// the branch name.
func CurrentIssueKey() (string, error) {
	b, err := CurrentBranch()
	if err != nil {
		return "", err
	}
	k := IssueKeyFromBranch(b)
	if k == "" {
		return "", fmt.Errorf("no Jira key found in branch %q", b)
	}
	return k, nil
}

// BranchName builds a conventional branch name from an issue key and
// summary, e.g. ("PROJ-123", "Fix the thing!") → "PROJ-123-fix-the-thing".
// Prefix is prepended (with a trailing slash) when non-empty, so
// callers can opt into "feature/PROJ-123-…" style names.
func BranchName(prefix, key, summary string) string {
	slug := slugify(summary)
	name := key
	if slug != "" {
		name = key + "-" + slug
	}
	if prefix != "" {
		name = strings.TrimRight(prefix, "/") + "/" + name
	}
	return name
}

// slugify converts free text into a kebab-case branch slug: lowercase,
// non-alphanumerics collapsed to single hyphens, trimmed to a
// reasonable length so the branch name doesn't blow past common
// terminal widths.
func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var sb strings.Builder
	prevDash := true // suppress leading dashes
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			sb.WriteRune(r)
			prevDash = false
		default:
			if !prevDash {
				sb.WriteByte('-')
				prevDash = true
			}
		}
	}
	out := strings.TrimRight(sb.String(), "-")
	const maxLen = 50
	if len(out) > maxLen {
		out = strings.TrimRight(out[:maxLen], "-")
	}
	return out
}

// CheckoutNewBranch runs `git checkout -b <name>` from the current
// working directory. The user sees git's own success / error output.
func CheckoutNewBranch(name string) error {
	cmd := exec.Command("git", "checkout", "-b", name)
	cmd.Stdout = nil
	cmd.Stderr = nil
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git checkout -b %s: %w: %s", name, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// BranchExists reports whether a local branch with the given name
// already exists.
func BranchExists(name string) bool {
	err := exec.Command("git", "rev-parse", "--verify", "--quiet", "refs/heads/"+name).Run()
	return err == nil
}

// CheckoutBranch switches to an existing local branch.
func CheckoutBranch(name string) error {
	cmd := exec.Command("git", "checkout", name)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git checkout %s: %w: %s", name, err, strings.TrimSpace(string(out)))
	}
	return nil
}
