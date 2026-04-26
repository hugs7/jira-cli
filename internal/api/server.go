package api

import (
	"fmt"
	"strings"
	"time"
)

// serverService implements Service against Jira Data Center / Server
// using REST API v2. Description and comment bodies are plain wiki
// markup strings (much simpler to round-trip than Cloud's ADF).
type serverService struct {
	client *Client
	host   string
}

func (s *serverService) Host() string    { return s.host }
func (s *serverService) WebBase() string { return s.client.HostRoot() }
func (s *serverService) Me() string      { return s.client.cfg.Username }

// --- raw response shapes ---

type srvUser struct {
	Name         string `json:"name"`
	Key          string `json:"key"`
	DisplayName  string `json:"displayName"`
	EmailAddress string `json:"emailAddress"`
}

type srvIssueType struct{ Name string `json:"name"` }
type srvStatus struct {
	Name           string `json:"name"`
	StatusCategory struct {
		Key  string `json:"key"`
		Name string `json:"name"`
	} `json:"statusCategory"`
}

type srvNamed struct{ Name string `json:"name"` }

type srvFields struct {
	Summary     string       `json:"summary"`
	Description string       `json:"description"`
	IssueType   srvIssueType `json:"issuetype"`
	Status      srvStatus    `json:"status"`
	Priority    *srvNamed    `json:"priority"`
	Resolution  *srvNamed    `json:"resolution"`
	Project     struct {
		Key  string `json:"key"`
		Name string `json:"name"`
	} `json:"project"`
	Reporter   *srvUser  `json:"reporter"`
	Assignee   *srvUser  `json:"assignee"`
	Labels     []string  `json:"labels"`
	Components []srvNamed `json:"components"`
	FixVersions []srvNamed `json:"fixVersions"`
	Created    string `json:"created"`
	Updated    string `json:"updated"`
	Parent     *struct {
		Key    string `json:"key"`
		Fields struct {
			IssueType srvIssueType `json:"issuetype"`
		} `json:"fields"`
	} `json:"parent"`
	IssueLinks []srvIssueLink `json:"issuelinks"`
}

type srvIssueLink struct {
	Type struct {
		Name    string `json:"name"`
		Inward  string `json:"inward"`
		Outward string `json:"outward"`
	} `json:"type"`
	InwardIssue *struct {
		Key    string `json:"key"`
		Fields struct {
			Summary string `json:"summary"`
		} `json:"fields"`
	} `json:"inwardIssue"`
	OutwardIssue *struct {
		Key    string `json:"key"`
		Fields struct {
			Summary string `json:"summary"`
		} `json:"fields"`
	} `json:"outwardIssue"`
}

type srvIssue struct {
	Key    string    `json:"key"`
	Fields srvFields `json:"fields"`
}

type srvSearchResp struct {
	Issues     []srvIssue `json:"issues"`
	Total      int        `json:"total"`
	StartAt    int        `json:"startAt"`
	MaxResults int        `json:"maxResults"`
}

type srvComment struct {
	ID      string  `json:"id"`
	Body    string  `json:"body"`
	Author  srvUser `json:"author"`
	Created string  `json:"created"`
	Updated string  `json:"updated"`
}

type srvCommentsResp struct {
	Comments []srvComment `json:"comments"`
}

type srvTransition struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	To   struct {
		Name string `json:"name"`
	} `json:"to"`
}
type srvTransitionsResp struct {
	Transitions []srvTransition `json:"transitions"`
}

type srvProject struct {
	Key  string `json:"key"`
	Name string `json:"name"`
}

// --- conversions ---

func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	// Jira uses "2006-01-02T15:04:05.000-0700" style timestamps.
	for _, layout := range []string{
		"2006-01-02T15:04:05.000-0700",
		"2006-01-02T15:04:05-0700",
		time.RFC3339Nano,
		time.RFC3339,
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

func (s *serverService) issueWebURL(key string) string {
	return s.client.HostRoot() + "/browse/" + key
}

func (s *serverService) toIssue(in srvIssue) Issue {
	out := Issue{
		Key:         in.Key,
		Summary:     in.Fields.Summary,
		Description: in.Fields.Description,
		IssueType:   in.Fields.IssueType.Name,
		Status:      in.Fields.Status.Name,
		StatusCat:   in.Fields.Status.StatusCategory.Key,
		Project:     in.Fields.Project.Key,
		Labels:      in.Fields.Labels,
		WebURL:      s.issueWebURL(in.Key),
		CreatedAt:   parseTime(in.Fields.Created),
		UpdatedAt:   parseTime(in.Fields.Updated),
	}
	if in.Fields.Priority != nil {
		out.Priority = in.Fields.Priority.Name
	}
	if in.Fields.Resolution != nil {
		out.Resolution = in.Fields.Resolution.Name
	}
	if in.Fields.Reporter != nil {
		out.Reporter = in.Fields.Reporter.DisplayName
	}
	if in.Fields.Assignee != nil {
		out.Assignee = in.Fields.Assignee.DisplayName
	}
	for _, c := range in.Fields.Components {
		out.Components = append(out.Components, c.Name)
	}
	for _, v := range in.Fields.FixVersions {
		out.FixVersions = append(out.FixVersions, v.Name)
	}
	if in.Fields.Parent != nil {
		out.ParentKey = in.Fields.Parent.Key
		// Server marks epics by issuetype name "Epic"; if the parent
		// is one, expose as EpicKey too so the TUI can group it.
		if strings.EqualFold(in.Fields.Parent.Fields.IssueType.Name, "Epic") {
			out.EpicKey = in.Fields.Parent.Key
		}
	}
	return out
}

// --- methods ---

func (s *serverService) GetIssue(key string) (*Issue, error) {
	var raw srvIssue
	endpoint := "issue/" + key
	if err := s.client.getJSON(endpoint, &raw); err != nil {
		return nil, err
	}
	out := s.toIssue(raw)
	return &out, nil
}

func (s *serverService) SearchIssues(in SearchInput) ([]Issue, error) {
	max := in.MaxResults
	if max <= 0 {
		max = 50
	}
	body := map[string]any{
		"jql":        in.JQL,
		"maxResults": max,
		"fields": []string{
			"summary", "status", "issuetype", "priority", "resolution",
			"project", "reporter", "assignee", "labels", "components",
			"fixVersions", "created", "updated", "parent",
		},
	}
	var resp srvSearchResp
	if err := s.client.postJSON("search", body, &resp); err != nil {
		return nil, err
	}
	out := make([]Issue, 0, len(resp.Issues))
	for _, i := range resp.Issues {
		out = append(out, s.toIssue(i))
	}
	return out, nil
}

func (s *serverService) UpdateSummary(key, summary string) error {
	body := map[string]any{
		"fields": map[string]any{"summary": summary},
	}
	return s.client.putJSON("issue/"+key, body, nil)
}

func (s *serverService) UpdateDescription(key, description string) error {
	body := map[string]any{
		"fields": map[string]any{"description": description},
	}
	return s.client.putJSON("issue/"+key, body, nil)
}

func (s *serverService) AssignIssue(key, name string) error {
	// Server expects { "name": "<login>" } or { "name": null } to
	// unassign. Accept the literal string "-1" as "automatic".
	value := any(name)
	if name == "" {
		value = nil
	}
	body := map[string]any{"name": value}
	return s.client.putJSON("issue/"+key+"/assignee", body, nil)
}

func (s *serverService) ListComments(key string) ([]Comment, error) {
	var resp srvCommentsResp
	if err := s.client.getJSON("issue/"+key+"/comment", &resp); err != nil {
		return nil, err
	}
	out := make([]Comment, 0, len(resp.Comments))
	for _, c := range resp.Comments {
		out = append(out, Comment{
			ID:        c.ID,
			Author:    c.Author.DisplayName,
			Body:      c.Body,
			CreatedAt: parseTime(c.Created),
			UpdatedAt: parseTime(c.Updated),
		})
	}
	return out, nil
}

func (s *serverService) AddComment(key, body string) (*Comment, error) {
	in := map[string]any{"body": body}
	var c srvComment
	if err := s.client.postJSON("issue/"+key+"/comment", in, &c); err != nil {
		return nil, err
	}
	return &Comment{
		ID: c.ID, Author: c.Author.DisplayName, Body: c.Body,
		CreatedAt: parseTime(c.Created), UpdatedAt: parseTime(c.Updated),
	}, nil
}

func (s *serverService) EditComment(key, commentID, body string) (*Comment, error) {
	in := map[string]any{"body": body}
	var c srvComment
	if err := s.client.putJSON(fmt.Sprintf("issue/%s/comment/%s", key, commentID), in, &c); err != nil {
		return nil, err
	}
	return &Comment{
		ID: c.ID, Author: c.Author.DisplayName, Body: c.Body,
		CreatedAt: parseTime(c.Created), UpdatedAt: parseTime(c.Updated),
	}, nil
}

func (s *serverService) DeleteComment(key, commentID string) error {
	return s.client.deleteJSON(fmt.Sprintf("issue/%s/comment/%s", key, commentID))
}

func (s *serverService) ListTransitions(key string) ([]Transition, error) {
	var resp srvTransitionsResp
	if err := s.client.getJSON("issue/"+key+"/transitions", &resp); err != nil {
		return nil, err
	}
	out := make([]Transition, 0, len(resp.Transitions))
	for _, t := range resp.Transitions {
		out = append(out, Transition{ID: t.ID, Name: t.Name, To: t.To.Name})
	}
	return out, nil
}

func (s *serverService) DoTransition(key, transitionID string) error {
	body := map[string]any{
		"transition": map[string]string{"id": transitionID},
	}
	return s.client.postJSON("issue/"+key+"/transitions", body, nil)
}

func (s *serverService) ListLinks(key string) ([]IssueLink, error) {
	// /issue/{key}?fields=issuelinks gives us the same shape.
	var raw struct {
		Fields struct {
			IssueLinks []srvIssueLink `json:"issuelinks"`
		} `json:"fields"`
	}
	if err := s.client.getJSON("issue/"+key+"?fields=issuelinks", &raw); err != nil {
		return nil, err
	}
	out := make([]IssueLink, 0, len(raw.Fields.IssueLinks))
	for _, l := range raw.Fields.IssueLinks {
		switch {
		case l.OutwardIssue != nil:
			out = append(out, IssueLink{
				Type:      l.Type.Outward,
				OtherKey:  l.OutwardIssue.Key,
				OtherSum:  l.OutwardIssue.Fields.Summary,
				Direction: "outward",
			})
		case l.InwardIssue != nil:
			out = append(out, IssueLink{
				Type:      l.Type.Inward,
				OtherKey:  l.InwardIssue.Key,
				OtherSum:  l.InwardIssue.Fields.Summary,
				Direction: "inward",
			})
		}
	}
	return out, nil
}

func (s *serverService) ListProjects() ([]Project, error) {
	var raw []srvProject
	if err := s.client.getJSON("project", &raw); err != nil {
		return nil, err
	}
	out := make([]Project, 0, len(raw))
	for _, p := range raw {
		out = append(out, Project{Key: p.Key, Name: p.Name})
	}
	return out, nil
}

// --- dashboard helpers ---
//
// Each one is just a JQL preset against the search endpoint. We use
// currentUser() so the same query works regardless of whether the
// user typed an email or login.

func (s *serverService) jqlList(jql string, max int) ([]Issue, error) {
	if max <= 0 {
		max = 25
	}
	return s.SearchIssues(SearchInput{JQL: jql, MaxResults: max})
}

func (s *serverService) ListMyAssigned(max int) ([]Issue, error) {
	return s.jqlList("assignee = currentUser() AND resolution = Unresolved ORDER BY updated DESC", max)
}

func (s *serverService) ListMentioned(max int) ([]Issue, error) {
	return s.jqlList("text ~ currentUser() AND resolution = Unresolved ORDER BY updated DESC", max)
}

func (s *serverService) ListWatching(max int) ([]Issue, error) {
	return s.jqlList("watcher = currentUser() AND resolution = Unresolved ORDER BY updated DESC", max)
}

func (s *serverService) ListCurrentSprint(max int) ([]Issue, error) {
	// Best-effort: works on instances that have the agile addon
	// (which is most of them on Server). Falls back to empty slice
	// on instances without it.
	issues, err := s.jqlList("assignee = currentUser() AND sprint in openSprints() ORDER BY rank", max)
	if err != nil {
		return []Issue{}, nil
	}
	return issues, nil
}

// --- agile / boards (REST /rest/agile/1.0) ---

// agileURL builds an absolute URL into the Jira Agile namespace,
// which lives next to /rest/api/2 under the same host root.
func (s *serverService) agileURL(path string) string {
	return s.client.HostRoot() + "/rest/agile/1.0/" + strings.TrimLeft(path, "/")
}

type srvBoard struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	Type     string `json:"type"`
	Location struct {
		ProjectKey  string `json:"projectKey"`
		ProjectName string `json:"projectName"`
	} `json:"location"`
}

type srvBoardPage struct {
	Values     []srvBoard `json:"values"`
	IsLast     bool       `json:"isLast"`
	StartAt    int        `json:"startAt"`
	MaxResults int        `json:"maxResults"`
	Total      int        `json:"total"`
}

func (s *serverService) ListBoards(projectKey, kind string, max int) ([]Board, error) {
	if max <= 0 {
		max = 50
	}
	params := map[string]string{"maxResults": itoa(max)}
	if projectKey != "" {
		params["projectKeyOrId"] = projectKey
	}
	if kind != "" {
		params["type"] = strings.ToLower(kind)
	}
	var page srvBoardPage
	if err := s.client.getJSON(s.agileURL("board")+queryString(params), &page); err != nil {
		// 404 → addon isn't installed; swallow that one. Surface
		// every other failure (auth, transport, server bugs).
		if strings.HasPrefix(err.Error(), "HTTP 404") {
			return []Board{}, nil
		}
		return nil, err
	}
	out := make([]Board, 0, len(page.Values))
	for _, b := range page.Values {
		out = append(out, Board{
			ID:         b.ID,
			Name:       b.Name,
			Type:       b.Type,
			ProjectKey: b.Location.ProjectKey,
		})
	}
	return out, nil
}

type srvBoardConfig struct {
	ID           int    `json:"id"`
	Name         string `json:"name"`
	Type         string `json:"type"`
	Location     struct {
		ProjectKey string `json:"projectKey"`
	} `json:"location"`
	ColumnConfig struct {
		Columns []struct {
			Name     string `json:"name"`
			Statuses []struct {
				ID string `json:"id"`
			} `json:"statuses"`
		} `json:"columns"`
	} `json:"columnConfig"`
}

func (s *serverService) GetBoardConfig(boardID int) (*BoardConfig, error) {
	var raw srvBoardConfig
	endpoint := s.agileURL(fmt.Sprintf("board/%d/configuration", boardID))
	if err := s.client.getJSON(endpoint, &raw); err != nil {
		return nil, err
	}

	// We need status names (not just IDs) so we can match issues by
	// their human status string. Resolve the IDs in one hop.
	idToName, _ := s.statusIDLookup()

	out := BoardConfig{Board: Board{
		ID:         raw.ID,
		Name:       raw.Name,
		Type:       raw.Type,
		ProjectKey: raw.Location.ProjectKey,
	}}
	for _, c := range raw.ColumnConfig.Columns {
		col := BoardColumn{Name: c.Name}
		for _, st := range c.Statuses {
			col.StatusIDs = append(col.StatusIDs, st.ID)
			if name, ok := idToName[st.ID]; ok {
				col.StatusKeys = append(col.StatusKeys, strings.ToLower(name))
			}
		}
		out.Columns = append(out.Columns, col)
	}
	return &out, nil
}

// statusIDLookup fetches every status definition on the instance and
// returns an id→name map. Cached implicitly per call (cheap, the
// payload is a few KB).
func (s *serverService) statusIDLookup() (map[string]string, error) {
	var raw []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := s.client.getJSON("status", &raw); err != nil {
		return map[string]string{}, err
	}
	out := make(map[string]string, len(raw))
	for _, r := range raw {
		out[r.ID] = r.Name
	}
	return out, nil
}

type srvSprint struct {
	ID    int    `json:"id"`
	Name  string `json:"name"`
	State string `json:"state"`
}

type srvSprintPage struct {
	Values []srvSprint `json:"values"`
	IsLast bool        `json:"isLast"`
}

func (s *serverService) ListBoardSprints(boardID int, state string) ([]Sprint, error) {
	params := map[string]string{}
	if state != "" {
		params["state"] = state
	}
	endpoint := s.agileURL(fmt.Sprintf("board/%d/sprint", boardID)) + queryString(params)
	var page srvSprintPage
	if err := s.client.getJSON(endpoint, &page); err != nil {
		// Kanban boards (and addon-less hosts) return 4xx for sprint
		// queries — that's expected, not an error worth surfacing.
		if strings.HasPrefix(err.Error(), "HTTP 4") {
			return []Sprint{}, nil
		}
		return nil, err
	}
	out := make([]Sprint, 0, len(page.Values))
	for _, sp := range page.Values {
		out = append(out, Sprint{ID: sp.ID, Name: sp.Name, State: sp.State})
	}
	return out, nil
}

func (s *serverService) ListBoardIssues(boardID, sprintID int, jqlFilter string, max int) ([]Issue, error) {
	if max <= 0 {
		max = 100
	}
	// Choose the most precise endpoint: sprint scope is tighter than
	// board scope, and avoids pulling backlog noise into the view.
	var endpoint string
	if sprintID > 0 {
		endpoint = s.agileURL(fmt.Sprintf("board/%d/sprint/%d/issue", boardID, sprintID))
	} else {
		endpoint = s.agileURL(fmt.Sprintf("board/%d/issue", boardID))
	}
	params := map[string]string{"maxResults": itoa(max)}
	if jqlFilter != "" {
		params["jql"] = jqlFilter
	}
	endpoint += queryString(params)

	var resp srvSearchResp
	if err := s.client.getJSON(endpoint, &resp); err != nil {
		return nil, err
	}
	out := make([]Issue, 0, len(resp.Issues))
	for _, i := range resp.Issues {
		out = append(out, s.toIssue(i))
	}
	return out, nil
}

func (s *serverService) SearchUsers(query string, limit int) ([]User, error) {
	if limit <= 0 {
		limit = 20
	}
	endpoint := "user/search" + queryString(map[string]string{
		"username":   query,
		"maxResults": itoa(limit),
	})
	var raw []srvUser
	if err := s.client.getJSON(endpoint, &raw); err != nil {
		return nil, err
	}
	out := make([]User, 0, len(raw))
	for _, u := range raw {
		out = append(out, User{
			Name:        u.Name,
			DisplayName: u.DisplayName,
			Email:       u.EmailAddress,
		})
	}
	return out, nil
}
