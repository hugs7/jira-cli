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

	ListComments(key string) ([]Comment, error)
	AddComment(key, body string) (*Comment, error)
	EditComment(key, commentID, body string) (*Comment, error)
	DeleteComment(key, commentID string) error

	ListTransitions(key string) ([]Transition, error)
	DoTransition(key, transitionID string) error

	ListLinks(key string) ([]IssueLink, error)
	ListProjects() ([]Project, error)

	// Dashboard helpers — each returns the freshest results first.
	ListMyAssigned(maxResults int) ([]Issue, error)
	ListMentioned(maxResults int) ([]Issue, error)
	ListWatching(maxResults int) ([]Issue, error)
	ListCurrentSprint(maxResults int) ([]Issue, error)

	SearchUsers(query string, limit int) ([]User, error)

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
