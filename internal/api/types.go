package api

import "time"

// Issue is the unified Jira issue representation used by jr commands
// and the TUI. It deliberately flattens the most-used fields rather
// than carrying the raw JSON tree (which differs between Cloud and
// Server).
type Issue struct {
	Key         string
	Summary     string
	Description string
	IssueType   string
	Status      string
	StatusCat   string // "new" | "indeterminate" | "done" — colour cue
	Priority    string
	Resolution  string
	Project     string // project key (e.g. "FOO")
	Reporter    string
	Assignee    string
	Labels      []string
	Components  []string
	FixVersions []string
	Sprint      string // active sprint name (best-effort)
	StoryPoints float64
	ParentKey   string // for sub-tasks / stories under epics
	EpicKey     string // for issues in an epic
	WebURL      string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Comment is a unified Jira comment representation.
type Comment struct {
	ID        string
	Author    string
	Body      string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Transition describes one workflow step a user can apply to an
// issue. The ID is what Jira's transition API expects; Name is what
// we show in the UI; To is the destination status name.
type Transition struct {
	ID   string
	Name string
	To   string
}

// User is the small subset of a Jira user we care about (assignee,
// reporter, mentions, autocomplete).
type User struct {
	AccountID   string // Cloud
	Name        string // Server (login name)
	DisplayName string
	Email       string
}

// IssueLink describes a link between two issues (blocks, relates to,
// duplicates, etc.).
type IssueLink struct {
	Type      string // human "blocks" / "is blocked by" / "relates to"
	OtherKey  string
	OtherSum  string
	Direction string // "inward" or "outward"
}

// Project is a workspace-level container for issues.
type Project struct {
	Key  string
	Name string
}

// SearchInput is the query for ListIssues / Search.
type SearchInput struct {
	JQL        string
	MaxResults int
}

// Board is one Jira Software / Agile board (Scrum or Kanban).
type Board struct {
	ID         int
	Name       string
	Type       string // "scrum" | "kanban" | "simple"
	ProjectKey string
}

// BoardColumn is one swim column on a board, mapped to a list of
// Jira statuses. The column count plus the status set drive the
// Kanban TUI layout.
type BoardColumn struct {
	Name       string
	StatusIDs  []string
	StatusKeys []string // status names (lowercased) for case-insensitive matching
}

// BoardConfig couples a Board with its column layout.
type BoardConfig struct {
	Board   Board
	Columns []BoardColumn
}

// Sprint is one iteration on a Scrum board.
type Sprint struct {
	ID    int
	Name  string
	State string // "active" | "future" | "closed"
}
