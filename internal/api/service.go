package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/hugs7/jira-cli/internal/config"
)

// Service is the high-level Jira API jr commands talk to.
// Implementations cover Bitbucket-style (Cloud vs Server / Data Center)
// behavioural splits.
type Service interface {
	Host() string
	WebBase() string

	// Me returns the configured user identifier (account ID on Cloud,
	// login on Server) — used for "my issues"-style queries and to
	// detect "this is mine" in the TUI.
	Me() string

	GetIssue(key string) (*Issue, error)
	SearchIssues(in SearchInput) ([]Issue, error)

	UpdateSummary(key, summary string) error
	UpdateDescription(key, description string) error
	AssignIssue(key, accountIDOrName string) error

	// Field setters used by the picker overlays. Empty string clears
	// the field (Jira API shape varies — implementations translate).
	UpdatePriority(key, priority string) error
	UpdateIssueType(key, typeName string) error

	// MoveIssueToSprint puts the issue into the given sprint
	// (sprintID > 0) or back onto the project backlog (sprintID == 0).
	MoveIssueToSprint(key string, sprintID int) error

	// UpdateLabels replaces the issue's full label set. Empty slice
	// clears all labels. Labels are free-text on Jira so values are
	// not validated against any catalogue.
	UpdateLabels(key string, labels []string) error

	// UpdateComponents replaces the issue's component set with the
	// given component names (which must exist in the project).
	UpdateComponents(key string, components []string) error

	// UpdateFixVersions replaces the fixVersions set on the issue.
	// Empty slice clears the field. Names must exist on the project.
	UpdateFixVersions(key string, versions []string) error

	// UpdateDueDate sets the issue's due date in YYYY-MM-DD form.
	// Empty string clears the field.
	UpdateDueDate(key, date string) error

	// UpdateStoryPoints sets the issue's story-points custom field.
	// Implementations discover the right customfield_XXXXX id from
	// /rest/api/2/field on first use and cache it.
	UpdateStoryPoints(key string, points float64) error

	// ListWatchers returns the users currently watching the issue.
	ListWatchers(key string) ([]User, error)
	// AddWatcher adds username/accountID to the issue's watcher list.
	// Empty user adds the authenticated user (matches Jira default).
	AddWatcher(key, user string) error
	// RemoveWatcher removes username/accountID from the watcher list.
	RemoveWatcher(key, user string) error

	// ListAttachments returns the attachments on an issue. The Issue
	// returned by GetIssue already includes them; this is mainly for
	// post-action refreshes that don't want a full GetIssue.
	ListAttachments(key string) ([]Attachment, error)
	// AddAttachment uploads a single file to the issue and returns
	// the parsed list of attachments Jira accepted (Jira responds
	// with the new attachments only).
	AddAttachment(key, path string) ([]Attachment, error)
	// DeleteAttachment removes an attachment by id.
	DeleteAttachment(id string) error

	// CreateIssue creates a new issue and returns the freshly
	// fetched record. Project + Summary are required; IssueType
	// defaults to "Task" if blank.
	CreateIssue(in CreateIssueInput) (*Issue, error)

	// Catalogue endpoints used to populate static pickers.
	ListPriorities() ([]NamedItem, error)
	ListIssueTypes(projectKey string) ([]NamedItem, error)
	// ListLabels returns the labels in use across the instance,
	// matching the optional `prefix` query so the picker can
	// autocomplete. Best-effort — Server returns nothing on older
	// versions and falls back to "free text only".
	ListLabels(prefix string, limit int) ([]string, error)
	// ListProjectComponents returns the component catalogue defined
	// for the given project. Empty slice if components aren't used.
	ListProjectComponents(projectKey string) ([]NamedItem, error)
	// ListProjectVersions returns the fix-version catalogue for the
	// project. Released and archived versions included; the picker
	// dims them via the Sub field.
	ListProjectVersions(projectKey string) ([]NamedItem, error)
	// ListProjectSprints returns active+future sprints across every
	// board associated with the project (deduped). Empty state =>
	// "active,future". Used for the sprint picker.
	ListProjectSprints(projectKey, state string) ([]Sprint, error)

	ListComments(key string) ([]Comment, error)
	AddComment(key, body string) (*Comment, error)
	EditComment(key, commentID, body string) (*Comment, error)
	DeleteComment(key, commentID string) error

	ListTransitions(key string) ([]Transition, error)
	DoTransition(key, transitionID string) error

	ListLinks(key string) ([]IssueLink, error)
	// ListIssueLinkTypes returns the catalogue of link types
	// configured on the instance ("Blocks", "Relates", …).
	ListIssueLinkTypes() ([]IssueLinkType, error)
	// AddIssueLink creates a link between two issues. typeName must
	// be the canonical link-type name (e.g. "Blocks"). When
	// direction == "outward" the source is fromKey; when "inward"
	// the source is toKey (Jira flips the relationship for you).
	AddIssueLink(fromKey, toKey, typeName, direction string) error
	// DeleteIssueLink removes a link by its Jira id (from IssueLink.ID).
	DeleteIssueLink(linkID string) error

	ListProjects() ([]Project, error)

	// Dashboard helpers — each returns the freshest results first.
	ListMyAssigned(maxResults int) ([]Issue, error)
	ListMentioned(maxResults int) ([]Issue, error)
	ListWatching(maxResults int) ([]Issue, error)
	ListCurrentSprint(maxResults int) ([]Issue, error)

	SearchUsers(query string, limit int) ([]User, error)

	// SearchAssignableUsers returns users who can be assigned to the
	// given issue. Empty issueKey falls back to a project-scoped or
	// global search. Used by the interactive assignee picker.
	SearchAssignableUsers(issueKey, query string, limit int) ([]User, error)

	// --- Agile / Boards ---
	//
	// Boards live under a sibling REST namespace (/rest/agile/1.0)
	// and may not be enabled on every instance — implementations
	// return ([]Board{}, nil) when the addon isn't installed rather
	// than blowing up.

	ListBoards(projectKey, kind string, max int) ([]Board, error)
	GetBoardConfig(boardID int) (*BoardConfig, error)
	ListBoardSprints(boardID int, state string) ([]Sprint, error)
	ListBoardIssues(boardID int, sprintID int, jqlFilter string, max int) ([]Issue, error)
}

// NewService picks the right implementation for a configured host.
func NewService(hostName string, h config.Host) (Service, error) {
	c := New(hostName, h)
	switch h.Type {
	case "cloud":
		return &cloudService{client: c, host: hostName}, nil
	case "server":
		return &serverService{client: c, host: hostName}, nil
	default:
		return nil, fmt.Errorf("unknown host type %q", h.Type)
	}
}

// ---------- helpers shared by both impls ----------

func decode(resp *http.Response, v any) error {
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if v == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

func (c *Client) getJSON(endpoint string, v any) error {
	req, err := c.NewRequest("GET", endpoint, nil)
	if err != nil {
		return err
	}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	return decode(resp, v)
}

// getRaw fetches an endpoint and returns the raw response bytes
// (after a >=400 check). Used when callers need to decode the same
// JSON twice — once into a typed shape and once into a generic map
// for fields we don't statically know (e.g. customfield_XXXXX).
func (c *Client) getRaw(endpoint string) ([]byte, error) {
	req, err := c.NewRequest("GET", endpoint, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return body, nil
}

func (c *Client) postJSON(endpoint string, in, out any) error {
	return c.bodyJSON("POST", endpoint, in, out)
}

func (c *Client) putJSON(endpoint string, in, out any) error {
	return c.bodyJSON("PUT", endpoint, in, out)
}

func (c *Client) deleteJSON(endpoint string) error {
	req, err := c.NewRequest("DELETE", endpoint, nil)
	if err != nil {
		return err
	}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	return decode(resp, nil)
}

func (c *Client) bodyJSON(method, endpoint string, in, out any) error {
	body := strings.NewReader("")
	contentType := ""
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = strings.NewReader(string(b))
		contentType = "application/json"
	}
	req, err := c.NewRequest(method, endpoint, body)
	if err != nil {
		return err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	return decode(resp, out)
}

// queryString joins a map of params into ?a=1&b=2.
func queryString(params map[string]string) string {
	if len(params) == 0 {
		return ""
	}
	v := url.Values{}
	for k, val := range params {
		if val == "" {
			continue
		}
		v.Set(k, val)
	}
	if len(v) == 0 {
		return ""
	}
	return "?" + v.Encode()
}

func itoa(i int) string { return strconv.Itoa(i) }
