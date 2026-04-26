// Package api is a thin HTTP client for the Jira REST API.
//
// Cloud (REST v3, https://<site>.atlassian.net/rest/api/3) uses HTTP
// Basic with email + API token. Server / DC (REST v2, https://<host>/
// rest/api/2) uses a personal access token sent as a Bearer header.
package api

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/hugs7/jira-cli/internal/config"
)

type Client struct {
	host string
	cfg  config.Host
	http *http.Client
}

func New(host string, cfg config.Host) *Client {
	return &Client{
		host: host,
		cfg:  cfg,
		http: &http.Client{Timeout: 30 * time.Second},
	}
}

// baseURL returns the API base for this host. The user may have stored
// a fully-qualified APIBase already; if not we build a sensible default.
func (c *Client) baseURL() string {
	if c.cfg.APIBase != "" {
		return strings.TrimRight(c.cfg.APIBase, "/")
	}
	if c.cfg.Type == "cloud" {
		return "https://" + c.host + "/rest/api/3"
	}
	// Server / DC default.
	return "https://" + c.host + "/rest/api/2"
}

// HostRoot returns the scheme+host portion of the configured API base
// (everything before "/rest/"). Useful for building browse URLs and
// hitting sibling REST namespaces (e.g. /rest/agile/1.0/).
func (c *Client) HostRoot() string {
	if c.cfg.WebBase != "" {
		return strings.TrimRight(c.cfg.WebBase, "/")
	}
	base := c.baseURL()
	if i := strings.Index(base, "/rest/"); i >= 0 {
		return base[:i]
	}
	return base
}

// NewRequest builds an authenticated request. endpoint may be a
// relative path (joined with baseURL) or a full URL.
func (c *Client) NewRequest(method, endpoint string, body io.Reader) (*http.Request, error) {
	url := endpoint
	if !strings.HasPrefix(endpoint, "http://") && !strings.HasPrefix(endpoint, "https://") {
		url = c.baseURL() + "/" + strings.TrimLeft(endpoint, "/")
	}
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		return nil, err
	}
	if c.cfg.Token != "" {
		switch c.cfg.Type {
		case "server":
			// Server / DC PAT.
			req.Header.Set("Authorization", "Bearer "+c.cfg.Token)
		default:
			// Cloud: Basic with email + API token.
			if c.cfg.Username != "" {
				req.SetBasicAuth(c.cfg.Username, c.cfg.Token)
			} else {
				req.Header.Set("Authorization", "Bearer "+c.cfg.Token)
			}
		}
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "jr-cli")
	return req, nil
}

func (c *Client) Do(req *http.Request) (*http.Response, error) {
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	return resp, nil
}
