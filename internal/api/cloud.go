package api

import (
	"fmt"
	"strings"
)

// cloudService is a placeholder Service implementation for Jira Cloud
// (REST v3). Cloud uses the Atlassian Document Format (ADF) for
// description and comment bodies, which needs a markdown-roundtripping
// layer before edits are safe. Until that's wired up, write operations
// that touch ADF return an explanatory error and read operations that
// would otherwise dump JSON return placeholder text.
type cloudService struct {
	client *Client
	host   string
}

var errCloudTodo = fmt.Errorf("not yet implemented for Jira Cloud — open an issue to prioritise")

func (c *cloudService) Host() string    { return c.host }
func (c *cloudService) WebBase() string { return c.client.HostRoot() }
func (c *cloudService) Me() string      { return c.client.cfg.Username }

// adfPlain extracts the visible text from an ADF document JSON value
// (best-effort). Returns "" for nil / empty docs.
func adfPlain(v any) string {
	if v == nil {
		return ""
	}
	var b strings.Builder
	var walk func(any)
	walk = func(node any) {
		switch n := node.(type) {
		case map[string]any:
			if t, _ := n["text"].(string); t != "" {
				b.WriteString(t)
			}
			if kids, ok := n["content"].([]any); ok {
				for _, k := range kids {
					walk(k)
				}
				if t, _ := n["type"].(string); t == "paragraph" || t == "heading" {
					b.WriteByte('\n')
				}
			}
		case []any:
			for _, k := range n {
				walk(k)
			}
		}
	}
	walk(v)
	return strings.TrimRight(b.String(), "\n")
}

func (c *cloudService) GetIssue(key string) (*Issue, error)               { return nil, errCloudTodo }
func (c *cloudService) SearchIssues(in SearchInput) ([]Issue, error)      { return nil, errCloudTodo }
func (c *cloudService) UpdateSummary(key, summary string) error           { return errCloudTodo }
func (c *cloudService) UpdateDescription(key, description string) error   { return errCloudTodo }
func (c *cloudService) AssignIssue(key, accountID string) error           { return errCloudTodo }
func (c *cloudService) ListComments(key string) ([]Comment, error)        { return nil, errCloudTodo }
func (c *cloudService) AddComment(key, body string) (*Comment, error)     { return nil, errCloudTodo }
func (c *cloudService) EditComment(key, id, body string) (*Comment, error) {
	return nil, errCloudTodo
}
func (c *cloudService) DeleteComment(key, id string) error          { return errCloudTodo }
func (c *cloudService) ListTransitions(key string) ([]Transition, error) {
	return nil, errCloudTodo
}
func (c *cloudService) DoTransition(key, id string) error            { return errCloudTodo }
func (c *cloudService) ListLinks(key string) ([]IssueLink, error)    { return nil, errCloudTodo }
func (c *cloudService) ListProjects() ([]Project, error)             { return nil, errCloudTodo }
func (c *cloudService) ListMyAssigned(max int) ([]Issue, error)      { return []Issue{}, nil }
func (c *cloudService) ListMentioned(max int) ([]Issue, error)       { return []Issue{}, nil }
func (c *cloudService) ListWatching(max int) ([]Issue, error)        { return []Issue{}, nil }
func (c *cloudService) ListCurrentSprint(max int) ([]Issue, error)   { return []Issue{}, nil }
func (c *cloudService) SearchUsers(q string, lim int) ([]User, error) {
	return nil, errCloudTodo
}

func (c *cloudService) ListBoards(projectKey, kind string, max int) ([]Board, error) {
	return []Board{}, nil
}
func (c *cloudService) GetBoardConfig(boardID int) (*BoardConfig, error) { return nil, errCloudTodo }
func (c *cloudService) ListBoardSprints(boardID int, state string) ([]Sprint, error) {
	return []Sprint{}, nil
}
func (c *cloudService) ListBoardIssues(boardID, sprintID int, jql string, max int) ([]Issue, error) {
	return []Issue{}, nil
}
