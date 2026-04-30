package cmd

import (
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"

	"github.com/hugs7/jira-cli/internal/api"
	"github.com/hugs7/jira-cli/internal/config"
)

// validateHostname rejects URLs / paths that look like a copy-pasted
// browser address. We want a bare hostname (or host:port) so the
// API base templates ("https://%s/rest/api/3") don't end up with
// "https://https://acme.atlassian.net" or similar nonsense.
func validateHostname(s string) error {
	s = strings.TrimSpace(s)
	if s == "" {
		return fmt.Errorf("hostname is required")
	}
	low := strings.ToLower(s)
	if strings.HasPrefix(low, "http://") || strings.HasPrefix(low, "https://") {
		return fmt.Errorf("enter the hostname only — drop the http(s):// scheme (e.g. %q not %q)",
			strings.TrimPrefix(strings.TrimPrefix(low, "https://"), "http://"), s)
	}
	if strings.ContainsAny(s, "/?#") {
		return fmt.Errorf("enter the hostname only — drop the path / query (got %q)", s)
	}
	if strings.ContainsAny(s, " \t") {
		return fmt.Errorf("hostname must not contain whitespace")
	}
	return nil
}

func newAuthCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "auth",
		Short: "Authenticate jr with a Jira host",
	}
	c.AddCommand(newAuthLoginCmd(), newAuthStatusCmd(), newAuthCheckCmd(), newAuthLogoutCmd())
	return c
}

func newAuthLoginCmd() *cobra.Command {
	var host string
	var hostType string
	var username string
	var token string

	c := &cobra.Command{
		Use:   "login",
		Short: "Log in to a Jira host",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Guard the --host flag too so flag-driven invocations can't
			// sneak a scheme/path past the prompt validator.
			if host != "" {
				if err := validateHostname(host); err != nil {
					return err
				}
			}
			// Step 1: ask for the host so we can pick a smart default
			// for the host type ("cloud" for *.atlassian.net,
			// otherwise "server").
			if err := huh.NewForm(
				huh.NewGroup(
					huh.NewInput().
						Title("Jira host").
						Description("Hostname only, e.g. acme.atlassian.net or jira.mycorp.example").
						Value(&host).
						Validate(validateHostname),
				),
			).Run(); err != nil {
				return err
			}
			if hostType == "" {
				if isAtlassianCloud(host) {
					hostType = "cloud"
				} else {
					hostType = "server"
				}
			}

			usernameTitle := "Atlassian email"
			usernameHelp := "The email you sign in to Jira with."
			tokenTitle := "API token"
			tokenHelp := "Create one at https://id.atlassian.com/manage-profile/security/api-tokens"
			if hostType == "server" {
				usernameTitle = "Username (informational)"
				usernameHelp = "Your Jira login. Used for display; PAT carries the auth."
				tokenTitle = "Personal access token (PAT)"
				tokenHelp = "Create one in your Jira profile → Personal Access Tokens."
			}

			if err := huh.NewForm(
				huh.NewGroup(
					huh.NewSelect[string]().
						Title("Host type").
						Options(
							huh.NewOption("Cloud (*.atlassian.net)", "cloud"),
							huh.NewOption("Server / Data Center (self-hosted)", "server"),
						).
						Value(&hostType),
					huh.NewInput().
						Title(usernameTitle).
						Description(usernameHelp).
						Value(&username).
						Validate(func(s string) error {
							if hostType == "cloud" && s == "" {
								return fmt.Errorf("email is required for Jira Cloud auth")
							}
							return nil
						}),
					huh.NewInput().
						Title(tokenTitle).
						Description(tokenHelp).
						EchoMode(huh.EchoModePassword).
						Value(&token).
						Validate(func(s string) error {
							if s == "" {
								return fmt.Errorf("token is required")
							}
							return nil
						}),
				),
			).Run(); err != nil {
				return err
			}

			h := config.Host{Type: hostType, Username: username, Token: token}
			if hostType == "cloud" {
				h.APIBase = fmt.Sprintf("https://%s/rest/api/3", host)
				h.WebBase = fmt.Sprintf("https://%s", host)
			} else {
				h.APIBase = fmt.Sprintf("https://%s/rest/api/2", host)
				h.WebBase = fmt.Sprintf("https://%s", host)
			}
			if err := config.SetHost(host, h); err != nil {
				return err
			}
			fmt.Printf("✓ Logged in to %s as %s\n", host, username)
			return nil
		},
	}

	c.Flags().StringVar(&host, "host", "", "Jira host (e.g. acme.atlassian.net)")
	c.Flags().StringVar(&hostType, "type", "", "host type: cloud or server")
	c.Flags().StringVarP(&username, "username", "u", "", "atlassian email (cloud) / login (server)")
	c.Flags().StringVarP(&token, "token", "t", "", "API token / PAT")
	return c
}

func isAtlassianCloud(host string) bool {
	if host == "" {
		return true
	}
	if len(host) > len(".atlassian.net") &&
		host[len(host)-len(".atlassian.net"):] == ".atlassian.net" {
		return true
	}
	return false
}

var (
	styleHost    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	styleDefault = lipgloss.NewStyle().Foreground(lipgloss.Color("10")).Bold(true)
	styleLabel   = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	styleOK      = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	styleErr     = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	styleMuted   = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
)

func maskToken(t string) string {
	if len(t) <= 4 {
		return "****"
	}
	return "••••••••" + t[len(t)-4:]
}

func newAuthStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show authentication status",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := config.Get()
			if len(cfg.Hosts) == 0 {
				fmt.Println(styleMuted.Render("Not logged in to any host. Try `jr auth login`."))
				return nil
			}

			names := make([]string, 0, len(cfg.Hosts))
			for n := range cfg.Hosts {
				names = append(names, n)
			}
			sort.Strings(names)

			for i, name := range names {
				h := cfg.Hosts[name]
				header := styleHost.Render(name)
				if name == cfg.DefaultHost {
					header += "  " + styleDefault.Render("(default)")
				}
				fmt.Println(header)
				fmt.Printf("  %s %s\n", styleLabel.Render("Type:    "), h.Type)
				fmt.Printf("  %s %s\n", styleLabel.Render("User:    "), h.Username)
				fmt.Printf("  %s %s\n", styleLabel.Render("Token:   "), maskToken(h.Token))
				if h.APIBase != "" {
					fmt.Printf("  %s %s\n", styleLabel.Render("API base:"), h.APIBase)
				}
				if i < len(names)-1 {
					fmt.Println()
				}
			}
			return nil
		},
	}
}

// newAuthCheckCmd verifies stored credentials by hitting /myself.
func newAuthCheckCmd() *cobra.Command {
	var hostFlag string
	c := &cobra.Command{
		Use:   "check",
		Short: "Verify stored credentials by calling the Jira API",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := config.Get()
			if len(cfg.Hosts) == 0 {
				return fmt.Errorf("not logged in to any host — run `jr auth login`")
			}

			targets := []string{}
			if hostFlag != "" {
				if _, ok := cfg.Hosts[hostFlag]; !ok {
					return fmt.Errorf("no auth for host %q", hostFlag)
				}
				targets = append(targets, hostFlag)
			} else {
				for n := range cfg.Hosts {
					targets = append(targets, n)
				}
				sort.Strings(targets)
			}

			anyFail := false
			for _, name := range targets {
				h := cfg.Hosts[name]
				if err := checkHost(h); err != nil {
					anyFail = true
					fmt.Printf("%s  %s — %s\n", styleErr.Render("✗"), styleHost.Render(name), err)
				} else {
					fmt.Printf("%s  %s — %s\n", styleOK.Render("✓"), styleHost.Render(name),
						styleMuted.Render("authenticated"))
				}
			}
			if anyFail {
				return fmt.Errorf("one or more hosts failed authentication")
			}
			return nil
		},
	}
	c.Flags().StringVar(&hostFlag, "host", "", "only check this host (default: all configured hosts)")
	return c
}

func checkHost(h config.Host) error {
	client := api.New("", h)
	req, err := client.NewRequest("GET", "myself", nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return fmt.Errorf("HTTP %d — token rejected", resp.StatusCode)
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

func newAuthLogoutCmd() *cobra.Command {
	var host string
	c := &cobra.Command{
		Use:   "logout",
		Short: "Remove credentials for a host",
		RunE: func(cmd *cobra.Command, args []string) error {
			if host == "" {
				host = config.Get().DefaultHost
			}
			if host == "" {
				return fmt.Errorf("no host configured")
			}

			var confirm bool
			if err := huh.NewConfirm().
				Title(fmt.Sprintf("Remove credentials for %s?", host)).
				Value(&confirm).
				Run(); err != nil {
				return err
			}
			if !confirm {
				fmt.Println("Cancelled.")
				return nil
			}

			if err := config.RemoveHost(host); err != nil {
				return err
			}
			fmt.Printf("✓ Logged out of %s\n", host)
			return nil
		},
	}
	c.Flags().StringVar(&host, "host", "", "host to remove (default: current default host)")
	return c
}
