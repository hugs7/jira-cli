package gitctx

import "testing"

func TestIssueKeyFromBranch(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"PROJ-123", "PROJ-123"},
		{"feature/PROJ-123-add-thing", "PROJ-123"},
		{"bugfix/AB-7", "AB-7"},
		{"hugo/proj-42-lowercased", "PROJ-42"},
		{"PROJ2-99-numeric-prefix", "PROJ2-99"},
		{"main", ""},
		{"feature/no-key-here", ""},
		// Make sure we don't grab a substring from an unrelated word.
		{"release/v1.2.3", ""},
		// Multiple keys: first wins.
		{"epic/FOO-1-do-BAR-2", "FOO-1"},
	}
	for _, c := range cases {
		got := IssueKeyFromBranch(c.in)
		if got != c.want {
			t.Errorf("IssueKeyFromBranch(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestBranchName(t *testing.T) {
	cases := []struct {
		prefix, key, summary, want string
	}{
		{"", "PROJ-1", "Fix the thing!", "PROJ-1-fix-the-thing"},
		{"feature", "PROJ-1", "Fix the thing!", "feature/PROJ-1-fix-the-thing"},
		{"feature/", "PROJ-1", "Fix the thing!", "feature/PROJ-1-fix-the-thing"},
		{"", "PROJ-1", "", "PROJ-1"},
		{"", "PROJ-1", "  Multiple   spaces -- and punctuation??", "PROJ-1-multiple-spaces-and-punctuation"},
	}
	for _, c := range cases {
		got := BranchName(c.prefix, c.key, c.summary)
		if got != c.want {
			t.Errorf("BranchName(%q,%q,%q) = %q, want %q",
				c.prefix, c.key, c.summary, got, c.want)
		}
	}
}

func TestSlugifyTruncation(t *testing.T) {
	long := "this is a really long summary that should be safely truncated to keep branch names manageable on the terminal"
	got := slugify(long)
	if len(got) > 50 {
		t.Errorf("slugify length %d > 50: %q", len(got), got)
	}
	if got[len(got)-1] == '-' {
		t.Errorf("slugify result must not end with dash: %q", got)
	}
}
