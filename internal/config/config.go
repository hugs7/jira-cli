// Package config handles persisted jr configuration.
//
// Config (including tokens) lives at $XDG_CONFIG_HOME/jr/config.yml — by
// default ~/.config/jr/config.yml. The file is written with mode 0600 so
// only the current user can read it.
//
// Override the location with $JR_CONFIG. Override a single token at runtime
// with $JR_TOKEN (applies to the default host).
package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Host describes a single Jira host the user is logged into.
//
// For Bitbucket we tracked username + app password. Jira's auth rules
// differ between Cloud and Server, so we store the moving parts:
//   - Cloud (type=cloud): Username = atlassian account email,
//     Token = API token. We send HTTP Basic.
//   - Server / DC (type=server): Token = personal access token,
//     sent as a Bearer header. Username is informational only and
//     used to populate "me"-style queries (assignee=currentUser()
//     works without it but display strings still benefit).
type Host struct {
	Type     string `yaml:"type"`               // "cloud" or "server"
	Username string `yaml:"username,omitempty"` // email (cloud) / login (server)
	APIBase  string `yaml:"api_base,omitempty"` // e.g. https://acme.atlassian.net/rest/api/3
	WebBase  string `yaml:"web_base,omitempty"` // base URL for browser links (no /rest/...)
	Token    string `yaml:"token,omitempty"`    // API token / PAT
}

// JQLPreset is a saved JQL query the user can fire from the dashboard.
type JQLPreset struct {
	Name string `yaml:"name"`
	JQL  string `yaml:"jql"`
}

// RecentIssue is a key + summary the user opened from jr; used to
// power the "recently viewed" dashboard section.
type RecentIssue struct {
	Host    string `yaml:"host"`
	Key     string `yaml:"key"`
	Summary string `yaml:"summary,omitempty"`
}

type Config struct {
	DefaultHost string          `yaml:"default_host"`
	Editor      string          `yaml:"editor,omitempty"` // command to launch text editor
	Hosts       map[string]Host `yaml:"hosts"`

	// Theme is the named TUI colour theme. See internal/tui/theme.go
	// for the list of built-ins (default, dracula, solarized-dark,
	// nord, 3270). Empty falls back to "default".
	Theme string `yaml:"theme,omitempty"`

	// Persisted dashboard state.
	JQLPresets []JQLPreset   `yaml:"jql_presets,omitempty"`
	Recent     []RecentIssue `yaml:"recent,omitempty"`
}

// EditorCmd returns the user's preferred editor command.
// Resolution order: config.editor → $VISUAL → $EDITOR → "nano".
func (c Config) EditorCmd() string {
	if c.Editor != "" {
		return c.Editor
	}
	if v := os.Getenv("VISUAL"); v != "" {
		return v
	}
	if v := os.Getenv("EDITOR"); v != "" {
		return v
	}
	return "nano"
}

var (
	loaded Config
	path   string
)

// Load reads the config from disk. A missing file is not an error.
func Load(overridePath string) error {
	p := overridePath
	if p == "" {
		if env := os.Getenv("JR_CONFIG"); env != "" {
			p = env
		} else {
			dir, err := os.UserConfigDir()
			if err != nil {
				return err
			}
			p = filepath.Join(dir, "jr", "config.yml")
		}
	}
	path = p

	loaded = Config{Hosts: map[string]Host{}}
	data, err := os.ReadFile(p)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read config %s: %w", p, err)
	}
	if len(data) > 0 {
		if err := yaml.Unmarshal(data, &loaded); err != nil {
			return fmt.Errorf("parse config: %w", err)
		}
	}
	if loaded.Hosts == nil {
		loaded.Hosts = map[string]Host{}
	}

	if envTok := os.Getenv("JR_TOKEN"); envTok != "" && loaded.DefaultHost != "" {
		h := loaded.Hosts[loaded.DefaultHost]
		h.Token = envTok
		loaded.Hosts[loaded.DefaultHost] = h
	}
	return nil
}

func Get() Config { return loaded }

// SetHost adds or updates a host and persists the config.
func SetHost(name string, h Host) error {
	if loaded.Hosts == nil {
		loaded.Hosts = map[string]Host{}
	}
	loaded.Hosts[name] = h
	if loaded.DefaultHost == "" {
		loaded.DefaultHost = name
	}
	return save()
}

func RemoveHost(name string) error {
	delete(loaded.Hosts, name)
	if loaded.DefaultHost == name {
		loaded.DefaultHost = ""
		for n := range loaded.Hosts {
			loaded.DefaultHost = n
			break
		}
	}
	return save()
}

// AddRecent records that the user opened an issue. Most recent first,
// capped at 25 to keep the dashboard tidy.
func AddRecent(host, key, summary string) error {
	out := []RecentIssue{{Host: host, Key: key, Summary: summary}}
	for _, r := range loaded.Recent {
		if r.Host == host && r.Key == key {
			continue
		}
		out = append(out, r)
		if len(out) >= 25 {
			break
		}
	}
	loaded.Recent = out
	return save()
}

func SetJQLPresets(p []JQLPreset) error {
	loaded.JQLPresets = p
	return save()
}

// SetTheme persists the chosen TUI theme name. Best-effort.
func SetTheme(name string) error {
	loaded.Theme = name
	return save()
}

func save() error {
	if path == "" {
		return fmt.Errorf("config path not set")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := yaml.Marshal(&loaded)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}
