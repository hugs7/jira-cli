package api

import (
	"encoding/json"
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

	// Custom-field IDs cached after first /field discovery. Empty
	// strings mean "not yet discovered or absent on this instance".
	cfStoryPoints string
	cfDiscovered  bool
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
	DueDate    string `json:"duedate"`
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
	ID   string `json:"id"`
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
	out.DueDate = in.Fields.DueDate
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
	endpoint := "issue/" + key
	body, err := s.client.getRaw(endpoint)
	if err != nil {
		return nil, err
	}
	var raw srvIssue
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}
	out := s.toIssue(raw)
	// Second pass over the same JSON: pull customfield values we
	// don't know at compile time (story points, sprint, …).
	var generic struct {
		Fields map[string]json.RawMessage `json:"fields"`
	}
	if err := json.Unmarshal(body, &generic); err == nil {
		s.applyCustomFields(&out, generic.Fields)
	}
	return &out, nil
}

// applyCustomFields fills in the parts of Issue that depend on
// per-instance custom-field IDs (story points today; epic link /
// sprint name later). Best-effort — silent no-op if discovery
// hasn't found the field on this instance.
func (s *serverService) applyCustomFields(out *Issue, fields map[string]json.RawMessage) {
	if !s.cfDiscovered {
		s.discoverCustomFields()
	}
	if s.cfStoryPoints != "" {
		if v, ok := fields[s.cfStoryPoints]; ok && len(v) > 0 && string(v) != "null" {
			var f float64
			if err := json.Unmarshal(v, &f); err == nil {
				out.StoryPoints = f
			}
		}
	}
}

// discoverCustomFields hits /field once and caches the IDs of the
// custom fields we care about (story points). Cheap (~1 HTTP call,
// dozens-of-KB response) and amortised across the whole session.
func (s *serverService) discoverCustomFields() {
	s.cfDiscovered = true // mark even on failure to avoid retrying every call
	var fields []struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		Custom bool   `json:"custom"`
		Schema struct {
			Custom string `json:"custom"`
		} `json:"schema"`
	}
	if err := s.client.getJSON("field", &fields); err != nil {
		return
	}
	for _, f := range fields {
		if !f.Custom {
			continue
		}
		// Match by canonical schema first (stable across instances),
		// then by name as a fallback.
		switch {
		case f.Schema.Custom == "com.atlassian.jira.plugin.system.customfieldtypes:float" &&
			strings.EqualFold(f.Name, "Story Points"):
			s.cfStoryPoints = f.ID
		case strings.EqualFold(f.Name, "Story Points") && s.cfStoryPoints == "":
			s.cfStoryPoints = f.ID
		case strings.EqualFold(f.Name, "Story point estimate") && s.cfStoryPoints == "":
			s.cfStoryPoints = f.ID
		}
	}
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

// UpdatePriority sets the priority by name. Empty string clears the
// field (sends `priority: null`).
func (s *serverService) UpdatePriority(key, priority string) error {
	var pv any
	if priority == "" {
		pv = nil
	} else {
		pv = map[string]string{"name": priority}
	}
	body := map[string]any{"fields": map[string]any{"priority": pv}}
	return s.client.putJSON("issue/"+key, body, nil)
}

// UpdateIssueType changes the issue type by name. Some workflows
// reject this if the new type lacks the current status — Jira will
// return an error and we surface it verbatim.
func (s *serverService) UpdateIssueType(key, typeName string) error {
	if typeName == "" {
		return fmt.Errorf("issue type cannot be empty")
	}
	body := map[string]any{
		"fields": map[string]any{
			"issuetype": map[string]string{"name": typeName},
		},
	}
	return s.client.putJSON("issue/"+key, body, nil)
}

// UpdateLabels replaces the issue's full label set. Sending an empty
// slice intentionally clears the field — Jira accepts `[]` here.
func (s *serverService) UpdateLabels(key string, labels []string) error {
	if labels == nil {
		labels = []string{}
	}
	body := map[string]any{"fields": map[string]any{"labels": labels}}
	return s.client.putJSON("issue/"+key, body, nil)
}

// UpdateComponents replaces the issue's component set by *name*
// (Server resolves names to IDs server-side as long as they exist
// in the project's component catalogue).
func (s *serverService) UpdateComponents(key string, components []string) error {
	objs := make([]map[string]string, 0, len(components))
	for _, c := range components {
		if c == "" {
			continue
		}
		objs = append(objs, map[string]string{"name": c})
	}
	body := map[string]any{"fields": map[string]any{"components": objs}}
	return s.client.putJSON("issue/"+key, body, nil)
}

// ListLabels hits Server's autocomplete endpoint. The result count is
// hard-capped by the server (typically ~20) regardless of `limit`,
// which is fine — the picker filters in-process anyway.
func (s *serverService) ListLabels(prefix string, limit int) ([]string, error) {
	if limit <= 0 {
		limit = 50
	}
	endpoint := "jql/autocompletedata/suggestions" + queryString(map[string]string{
		"fieldName":  "labels",
		"fieldValue": prefix,
	})
	var raw struct {
		Results []struct {
			Value       string `json:"value"`
			DisplayName string `json:"displayName"`
		} `json:"results"`
	}
	if err := s.client.getJSON(endpoint, &raw); err != nil {
		// Older servers don't expose this endpoint — return empty so
		// the picker still works in pure free-text mode.
		return []string{}, nil
	}
	out := make([]string, 0, len(raw.Results))
	for _, r := range raw.Results {
		v := r.Value
		if v == "" {
			v = r.DisplayName
		}
		if v != "" {
			out = append(out, v)
		}
	}
	return out, nil
}

// ListProjectComponents returns the component catalogue for a
// project. Empty slice for projects that don't use components.
func (s *serverService) ListProjectComponents(projectKey string) ([]NamedItem, error) {
	if projectKey == "" {
		return []NamedItem{}, nil
	}
	var raw []struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := s.client.getJSON("project/"+projectKey+"/components", &raw); err != nil {
		return nil, err
	}
	out := make([]NamedItem, 0, len(raw))
	for _, r := range raw {
		out = append(out, NamedItem{ID: r.ID, Name: r.Name, Description: r.Description})
	}
	return out, nil
}

// UpdateFixVersions replaces the issue's fixVersions field. Versions
// must already exist in the project's version catalogue.
func (s *serverService) UpdateFixVersions(key string, versions []string) error {
	objs := make([]map[string]string, 0, len(versions))
	for _, v := range versions {
		if v == "" {
			continue
		}
		objs = append(objs, map[string]string{"name": v})
	}
	body := map[string]any{"fields": map[string]any{"fixVersions": objs}}
	return s.client.putJSON("issue/"+key, body, nil)
}

// UpdateDueDate sets the standard `duedate` field. Empty clears it.
// Format must be YYYY-MM-DD; Jira rejects anything else.
func (s *serverService) UpdateDueDate(key, date string) error {
	var v any
	if date == "" {
		v = nil
	} else {
		v = date
	}
	body := map[string]any{"fields": map[string]any{"duedate": v}}
	return s.client.putJSON("issue/"+key, body, nil)
}

// UpdateStoryPoints writes the per-instance story-points custom
// field. Returns an explanatory error if the field couldn't be
// discovered on this instance.
func (s *serverService) UpdateStoryPoints(key string, points float64) error {
	if !s.cfDiscovered {
		s.discoverCustomFields()
	}
	if s.cfStoryPoints == "" {
		return fmt.Errorf("story-points custom field not found on this instance")
	}
	body := map[string]any{"fields": map[string]any{s.cfStoryPoints: points}}
	return s.client.putJSON("issue/"+key, body, nil)
}

// ListProjectVersions returns the fix-version catalogue for the
// project. Released / archived state is captured in Description so
// the picker can show it.
func (s *serverService) ListProjectVersions(projectKey string) ([]NamedItem, error) {
	if projectKey == "" {
		return []NamedItem{}, nil
	}
	var raw []struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Description string `json:"description"`
		Released    bool   `json:"released"`
		Archived    bool   `json:"archived"`
		ReleaseDate string `json:"releaseDate"`
	}
	if err := s.client.getJSON("project/"+projectKey+"/versions", &raw); err != nil {
		return nil, err
	}
	out := make([]NamedItem, 0, len(raw))
	for _, r := range raw {
		desc := r.Description
		var tags []string
		if r.Archived {
			tags = append(tags, "archived")
		}
		if r.Released {
			tags = append(tags, "released")
		}
		if r.ReleaseDate != "" {
			tags = append(tags, r.ReleaseDate)
		}
		if len(tags) > 0 {
			if desc != "" {
				desc += " · "
			}
			desc += strings.Join(tags, " · ")
		}
		out = append(out, NamedItem{ID: r.ID, Name: r.Name, Description: desc})
	}
	return out, nil
}

// MoveIssueToSprint uses the Agile API: PUT /sprint/{id}/issue to
// add, POST /backlog/issue when sprintID == 0.
func (s *serverService) MoveIssueToSprint(key string, sprintID int) error {
	body := map[string]any{"issues": []string{key}}
	if sprintID == 0 {
		return s.client.postJSON(s.agileURL("backlog/issue"), body, nil)
	}
	return s.client.postJSON(s.agileURL(fmt.Sprintf("sprint/%d/issue", sprintID)), body, nil)
}

// ListPriorities returns the global priority catalogue.
func (s *serverService) ListPriorities() ([]NamedItem, error) {
	var raw []struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Description string `json:"description"`
		IconURL     string `json:"iconUrl"`
	}
	if err := s.client.getJSON("priority", &raw); err != nil {
		return nil, err
	}
	out := make([]NamedItem, 0, len(raw))
	for _, r := range raw {
		out = append(out, NamedItem{ID: r.ID, Name: r.Name, Description: r.Description, IconURL: r.IconURL})
	}
	return out, nil
}

// ListIssueTypes returns the issue types available for a project (or
// the global set when projectKey is empty).
func (s *serverService) ListIssueTypes(projectKey string) ([]NamedItem, error) {
	if projectKey != "" {
		var proj struct {
			IssueTypes []struct {
				ID          string `json:"id"`
				Name        string `json:"name"`
				Description string `json:"description"`
				IconURL     string `json:"iconUrl"`
				Subtask     bool   `json:"subtask"`
			} `json:"issueTypes"`
		}
		if err := s.client.getJSON("project/"+projectKey, &proj); err == nil && len(proj.IssueTypes) > 0 {
			out := make([]NamedItem, 0, len(proj.IssueTypes))
			for _, t := range proj.IssueTypes {
				out = append(out, NamedItem{
					ID: t.ID, Name: t.Name, Description: t.Description, IconURL: t.IconURL,
				})
			}
			return out, nil
		}
		// fall through to global catalogue if the project lookup failed
	}
	var raw []struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Description string `json:"description"`
		IconURL     string `json:"iconUrl"`
	}
	if err := s.client.getJSON("issuetype", &raw); err != nil {
		return nil, err
	}
	out := make([]NamedItem, 0, len(raw))
	for _, r := range raw {
		out = append(out, NamedItem{ID: r.ID, Name: r.Name, Description: r.Description, IconURL: r.IconURL})
	}
	return out, nil
}

// ListProjectSprints walks every Scrum board for the project and
// unions their sprint lists. Deduped by sprint ID. State defaults to
// "active,future" — closed sprints are noisy in pickers.
func (s *serverService) ListProjectSprints(projectKey, state string) ([]Sprint, error) {
	if state == "" {
		state = "active,future"
	}
	boards, err := s.ListBoards(projectKey, "scrum", 50)
	if err != nil {
		return nil, err
	}
	seen := map[int]bool{}
	var out []Sprint
	for _, b := range boards {
		sps, _ := s.ListBoardSprints(b.ID, state)
		for _, sp := range sps {
			if seen[sp.ID] {
				continue
			}
			seen[sp.ID] = true
			out = append(out, sp)
		}
	}
	return out, nil
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
				ID:        l.ID,
				Type:      l.Type.Outward,
				OtherKey:  l.OutwardIssue.Key,
				OtherSum:  l.OutwardIssue.Fields.Summary,
				Direction: "outward",
			})
		case l.InwardIssue != nil:
			out = append(out, IssueLink{
				ID:        l.ID,
				Type:      l.Type.Inward,
				OtherKey:  l.InwardIssue.Key,
				OtherSum:  l.InwardIssue.Fields.Summary,
				Direction: "inward",
			})
		}
	}
	return out, nil
}

// ListIssueLinkTypes hits /rest/api/2/issueLinkType which is the
// canonical catalogue. Returns inward+outward verbs so the picker
// can offer both directions.
func (s *serverService) ListIssueLinkTypes() ([]IssueLinkType, error) {
	var raw struct {
		IssueLinkTypes []struct {
			ID      string `json:"id"`
			Name    string `json:"name"`
			Inward  string `json:"inward"`
			Outward string `json:"outward"`
		} `json:"issueLinkTypes"`
	}
	if err := s.client.getJSON("issueLinkType", &raw); err != nil {
		return nil, err
	}
	out := make([]IssueLinkType, 0, len(raw.IssueLinkTypes))
	for _, t := range raw.IssueLinkTypes {
		out = append(out, IssueLinkType{
			ID: t.ID, Name: t.Name, Inward: t.Inward, Outward: t.Outward,
		})
	}
	return out, nil
}

// AddIssueLink creates a link via POST /issueLink. Direction
// determines which side is "inward" vs "outward" — the API takes
// inwardIssue + outwardIssue keys explicitly.
func (s *serverService) AddIssueLink(fromKey, toKey, typeName, direction string) error {
	inward, outward := fromKey, toKey
	if direction == "inward" {
		inward, outward = toKey, fromKey
	}
	body := map[string]any{
		"type":         map[string]string{"name": typeName},
		"inwardIssue":  map[string]string{"key": inward},
		"outwardIssue": map[string]string{"key": outward},
	}
	return s.client.postJSON("issueLink", body, nil)
}

// DeleteIssueLink calls DELETE /issueLink/{id}.
func (s *serverService) DeleteIssueLink(linkID string) error {
	return s.client.deleteJSON("issueLink/" + linkID)
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

// ListBoards paginates through every board on the host (or every
// board in `projectKey` when set) until the server reports IsLast or
// max is reached. Passing max <= 0 means "fetch them all".
func (s *serverService) ListBoards(projectKey, kind string, max int) ([]Board, error) {
	const pageSize = 100 // Jira caps individual board responses
	if max == 0 {
		max = -1 // signal "everything"
	}

	out := []Board{}
	startAt := 0
	for {
		params := map[string]string{
			"maxResults": itoa(pageSize),
			"startAt":    itoa(startAt),
		}
		if projectKey != "" {
			params["projectKeyOrId"] = projectKey
		}
		if kind != "" {
			params["type"] = strings.ToLower(kind)
		}
		var page srvBoardPage
		if err := s.client.getJSON(s.agileURL("board")+queryString(params), &page); err != nil {
			// 404 → addon isn't installed. Otherwise surface what
			// we have so far plus the error.
			if strings.HasPrefix(err.Error(), "HTTP 404") {
				return []Board{}, nil
			}
			if startAt > 0 {
				return out, nil // partial result is better than none
			}
			return nil, err
		}
		for _, b := range page.Values {
			out = append(out, Board{
				ID:         b.ID,
				Name:       b.Name,
				Type:       b.Type,
				ProjectKey: b.Location.ProjectKey,
			})
			if max > 0 && len(out) >= max {
				return out, nil
			}
		}
		if page.IsLast || len(page.Values) == 0 {
			return out, nil
		}
		startAt += len(page.Values)
	}
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

// SearchAssignableUsers hits /user/assignable/search which returns
// only users with permission to be assigned to the given issue. An
// empty query is interpreted by Server as "all assignables" — handy
// for showing a default candidate list when the picker first opens.
func (s *serverService) SearchAssignableUsers(issueKey, query string, limit int) ([]User, error) {
	if limit <= 0 {
		limit = 20
	}
	params := map[string]string{
		"username":   query,
		"maxResults": itoa(limit),
	}
	if issueKey != "" {
		params["issueKey"] = issueKey
	}
	// Server requires *one* of issueKey/project/projectKey/username
	// to be non-empty. If we have neither an issue context nor a
	// query, fall back to a wildcard so the API still returns
	// something (most Server installs treat "." as match-all).
	if issueKey == "" && query == "" {
		params["username"] = "."
	}
	var raw []srvUser
	if err := s.client.getJSON("user/assignable/search"+queryString(params), &raw); err != nil {
		// Fall back to the generic user search if the assignable
		// endpoint rejects us (e.g. the issue key wasn't accepted by
		// this Server version).
		return s.SearchUsers(query, limit)
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
