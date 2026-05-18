package api

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// Filter is a saved Jira JQL search.
//
// On Jira Server / Data Center a filter is owned by a single user
// and may be marked favourite per-user. The `viewUrl` Jira returns
// points at the in-app filter page; we surface it via ViewURL so
// `jr filter view` can render a clickable link.
type Filter struct {
	ID          int
	Name        string
	Description string
	JQL         string
	Owner       string // display name (e.g. "Hugo Burton")
	OwnerName   string // login / accountId — stable identifier
	Favourite   bool
	ViewURL     string
}

// FilterInput is the body shape for POST /rest/api/2/filter and PUT
// /rest/api/2/filter/{id}. Jira Server's PUT is a full replacement
// (not a patch), so callers must fetch the existing filter, merge
// user-supplied overrides, then call UpdateFilter — otherwise
// unchanged fields silently get wiped.
type FilterInput struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	JQL         string `json:"jql"`
	Favourite   bool   `json:"favourite"`
}

// --- raw JSON shapes ---

type srvFilter struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	JQL         string   `json:"jql"`
	Owner       *srvUser `json:"owner"`
	Favourite   bool     `json:"favourite"`
	ViewURL     string   `json:"viewUrl"`
}

func (s *serverService) toFilter(in srvFilter) Filter {
	var owner, ownerName string
	if in.Owner != nil {
		owner = in.Owner.DisplayName
		ownerName = in.Owner.Name
	}
	id, _ := strconv.Atoi(in.ID)
	return Filter{
		ID:          id,
		Name:        in.Name,
		Description: in.Description,
		JQL:         in.JQL,
		Owner:       owner,
		OwnerName:   ownerName,
		Favourite:   in.Favourite,
		ViewURL:     in.ViewURL,
	}
}

// ListFilters returns saved filters. Empty owner returns the
// authenticated user's favourite filters (via /filter/favourite);
// owner == "me" resolves to the authenticated user; any other value
// is treated as a Jira username and queried via /filter/search.
func (s *serverService) ListFilters(owner string) ([]Filter, error) {
	if owner == "" {
		var raw []srvFilter
		if err := s.client.getJSON("filter/favourite", &raw); err != nil {
			return nil, err
		}
		out := make([]Filter, 0, len(raw))
		for _, f := range raw {
			out = append(out, s.toFilter(f))
		}
		return out, nil
	}
	u := owner
	if u == "me" {
		u = s.Me()
	}
	// /filter/search returns a paged envelope and omits jql /
	// description / favourite / viewUrl unless they're expanded.
	// NOTE: this endpoint was added in newer Jira Server versions
	// and is not available on every Server / DC instance (e.g.
	// Westpac's 9.12.26 returns 404). We translate that into a
	// human-friendly error so callers know to drop the --owner
	// flag and fall back to /filter/favourite.
	q := url.Values{}
	q.Set("owner", u)
	q.Set("expand", "description,owner,jql,favourite,viewUrl")
	var pg struct {
		Values []srvFilter `json:"values"`
	}
	if err := s.client.getJSON("filter/search?"+q.Encode(), &pg); err != nil {
		if strings.HasPrefix(err.Error(), "HTTP 404") {
			return nil, fmt.Errorf("this Jira instance does not expose /rest/api/2/filter/search " +
				"(common on older Server / DC versions); only `jr filter list` (favourites) " +
				"and `jr filter view <id>` are supported here")
		}
		return nil, err
	}
	out := make([]Filter, 0, len(pg.Values))
	for _, f := range pg.Values {
		out = append(out, s.toFilter(f))
	}
	return out, nil
}

// GetFilter fetches a filter by numeric id.
func (s *serverService) GetFilter(id int) (*Filter, error) {
	var raw srvFilter
	if err := s.client.getJSON(fmt.Sprintf("filter/%d", id), &raw); err != nil {
		return nil, err
	}
	f := s.toFilter(raw)
	return &f, nil
}

// CreateFilter POSTs a new filter and returns the freshly fetched
// record (Jira responds with the canonical view, including the
// allocated id and viewUrl).
func (s *serverService) CreateFilter(in FilterInput) (*Filter, error) {
	var raw srvFilter
	if err := s.client.postJSON("filter", in, &raw); err != nil {
		return nil, err
	}
	f := s.toFilter(raw)
	return &f, nil
}

// UpdateFilter PUTs a (full) replacement filter body. Callers must
// merge any user-supplied overrides into the existing filter before
// calling — Jira Server's PUT is not a patch.
func (s *serverService) UpdateFilter(id int, in FilterInput) (*Filter, error) {
	var raw srvFilter
	if err := s.client.putJSON(fmt.Sprintf("filter/%d", id), in, &raw); err != nil {
		return nil, err
	}
	f := s.toFilter(raw)
	return &f, nil
}

// DeleteFilter removes a filter by id (204 No Content on success).
func (s *serverService) DeleteFilter(id int) error {
	return s.client.deleteJSON(fmt.Sprintf("filter/%d", id))
}

// SetFilterFavourite toggles the favourite flag on a filter. It's a
// thin wrapper that fetches the filter, flips Favourite, and PUTs
// the merged body back so unchanged fields survive.
//
// Caveat (Jira Server / DC): some Server versions silently ignore
// the `favourite` field on PUT even though the docs say otherwise —
// the field is only honoured at POST (create) time. We re-fetch the
// filter after PUT and return ErrFavouriteNotToggled when the
// server-side state didn't change, so the CLI can surface a useful
// warning instead of claiming success.
func (s *serverService) SetFilterFavourite(id int, fav bool) (*Filter, error) {
	cur, err := s.GetFilter(id)
	if err != nil {
		return nil, err
	}
	if cur.Favourite == fav {
		return cur, nil
	}
	if _, err := s.UpdateFilter(id, FilterInput{
		Name:        cur.Name,
		Description: cur.Description,
		JQL:         cur.JQL,
		Favourite:   fav,
	}); err != nil {
		return nil, err
	}
	// Verify — some Server instances (e.g. 9.12.x) drop the
	// favourite field on PUT.
	after, err := s.GetFilter(id)
	if err != nil {
		return nil, err
	}
	if after.Favourite != fav {
		return after, ErrFavouriteNotToggled
	}
	return after, nil
}

// ErrFavouriteNotToggled is returned by SetFilterFavourite when the
// PUT succeeded but the filter's favourite flag didn't change on
// the server (a known Jira Server quirk on some versions).
var ErrFavouriteNotToggled = fmt.Errorf("server ignored favourite change " +
	"(this Jira version does not honour the favourite field on PUT; " +
	"toggle it from the Jira web UI instead)")

// FilterShareInput is the body for POST /rest/api/2/filter/{id}/permission.
// Type must be one of: global, authenticated, project, group, projectRole,
// user. Target meaning depends on type — projectId for project, groupname
// for group, etc. — and is ignored for global / authenticated shares.
type FilterShareInput struct {
	Type        string `json:"type"`
	ProjectID   string `json:"projectId,omitempty"`
	GroupName   string `json:"groupname,omitempty"`
	ProjectRole string `json:"projectRoleId,omitempty"`
	User        string `json:"userName,omitempty"`
}

// AddFilterPermission grants the given share type on a filter. Used
// to make a private filter visible to a board ("authenticated"
// shares are the common default).
func (s *serverService) AddFilterPermission(id int, in FilterShareInput) error {
	return s.client.postJSON(fmt.Sprintf("filter/%d/permission", id), in, nil)
}
