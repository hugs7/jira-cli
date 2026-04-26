package cmd

import (
	"fmt"
	"os/exec"
	"runtime"

	"github.com/hugs7/jira-cli/internal/api"
	"github.com/hugs7/jira-cli/internal/config"
)

// defaultService returns a Service for the configured default host
// (or the host given via --host on a sub-command).
func defaultService(hostFlag string) (api.Service, error) {
	cfg := config.Get()
	host := hostFlag
	if host == "" {
		host = cfg.DefaultHost
	}
	if host == "" {
		return nil, fmt.Errorf("no host configured — run `jr auth login`")
	}
	hcfg, ok := cfg.Hosts[host]
	if !ok {
		return nil, fmt.Errorf("no auth for host %q — run `jr auth login --host %s`", host, host)
	}
	return api.NewService(host, hcfg)
}

// openInBrowser opens a URL in the user's default browser.
func openInBrowser(url string) error {
	var cmd string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		cmd = "open"
	case "windows":
		cmd = "rundll32"
		args = []string{"url.dll,FileProtocolHandler"}
	default:
		cmd = "xdg-open"
	}
	args = append(args, url)
	return exec.Command(cmd, args...).Start()
}
