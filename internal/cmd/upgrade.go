package cmd

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/minio/selfupdate"
	"github.com/spf13/cobra"
)

// GitHub repo to query for releases.
const upgradeRepo = "hugs7/jira-cli"

// newUpgradeCmd implements `jr upgrade`: checks the latest GitHub
// release, downloads the matching archive for the current OS/arch,
// extracts the `jr` binary and atomically replaces the running
// executable.
//
// This is only useful for users who installed via a direct binary
// download or the curl|sh script. Users who installed via Homebrew /
// Scoop / apt should use those package managers instead.
func newUpgradeCmd(info BuildInfo) *cobra.Command {
	var (
		check bool
		force bool
	)
	c := &cobra.Command{
		Use:   "upgrade",
		Short: "Upgrade jr to the latest release",
		Long: `Download and install the latest release of jr from GitHub.

If you installed jr via Homebrew, Scoop, apt or dnf, prefer your
package manager's update command instead:

  brew upgrade jira-cli
  scoop update jr
  sudo apt update && sudo apt upgrade jira-cli
  sudo dnf upgrade jira-cli`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runUpgrade(info, check, force)
		},
	}
	c.Flags().BoolVar(&check, "check", false, "only check for a new version, don't install")
	c.Flags().BoolVar(&force, "force", false, "reinstall even if already on the latest version")
	return c
}

type ghRelease struct {
	TagName string `json:"tag_name"`
	Name    string `json:"name"`
	Assets  []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
		Size               int64  `json:"size"`
	} `json:"assets"`
}

func runUpgrade(info BuildInfo, checkOnly, force bool) error {
	rel, err := latestRelease()
	if err != nil {
		return fmt.Errorf("check latest release: %w", err)
	}
	latest := strings.TrimPrefix(rel.TagName, "v")
	current := strings.TrimPrefix(info.Version, "v")

	fmt.Printf("current: %s\nlatest:  %s\n", current, latest)

	if !force && current == latest {
		fmt.Println("already up to date")
		return nil
	}
	if checkOnly {
		fmt.Println("a newer version is available — run `jr upgrade` to install")
		return nil
	}

	asset, err := pickAsset(rel)
	if err != nil {
		return err
	}
	fmt.Printf("downloading %s (%s)…\n", asset.Name, humanSize(asset.Size))

	bin, cleanup, err := downloadAndExtract(asset.BrowserDownloadURL, asset.Name)
	if err != nil {
		return err
	}
	defer cleanup()

	fmt.Println("installing…")
	if err := selfupdate.Apply(bin, selfupdate.Options{}); err != nil {
		if rerr := selfupdate.RollbackError(err); rerr != nil {
			return fmt.Errorf("install failed and rollback failed: %v (original: %w)", rerr, err)
		}
		return fmt.Errorf("install failed: %w", err)
	}
	fmt.Printf("upgraded to %s ✓\n", latest)
	return nil
}

func latestRelease() (*ghRelease, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", upgradeRepo)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "jr-upgrade")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("github api returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var rel ghRelease
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil, err
	}
	if rel.TagName == "" {
		return nil, fmt.Errorf("no releases found for %s", upgradeRepo)
	}
	return &rel, nil
}

// pickAsset finds the archive matching this binary's OS/arch using
// the same naming convention as .goreleaser.yaml:
//
//	jr_<version>_<os>_<arch>.{tar.gz,zip}
func pickAsset(rel *ghRelease) (*struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Size               int64  `json:"size"`
}, error) {
	osName := runtime.GOOS
	arch := runtime.GOARCH

	osTag := osName
	wantExt := ".tar.gz"
	if osName == "windows" {
		wantExt = ".zip"
	}

	for i := range rel.Assets {
		a := &rel.Assets[i]
		n := strings.ToLower(a.Name)
		if !strings.HasSuffix(n, wantExt) {
			continue
		}
		if !strings.Contains(n, "_"+osTag+"_") {
			continue
		}
		if !strings.Contains(n, "_"+arch) {
			continue
		}
		return a, nil
	}
	return nil, fmt.Errorf("no release asset for %s/%s in %s", osName, arch, rel.TagName)
}

// downloadAndExtract fetches the archive and returns an open reader
// over the `jr` binary inside it, plus a cleanup func to remove the
// temp dir after Apply() finishes.
func downloadAndExtract(url, name string) (io.Reader, func(), error) {
	tmp, err := os.MkdirTemp("", "jr-upgrade-*")
	if err != nil {
		return nil, nil, err
	}
	cleanup := func() { _ = os.RemoveAll(tmp) }

	archivePath := filepath.Join(tmp, name)
	if err := downloadFile(url, archivePath); err != nil {
		cleanup()
		return nil, nil, err
	}

	binName := "jr"
	if runtime.GOOS == "windows" {
		binName = "jr.exe"
	}
	extracted := filepath.Join(tmp, binName)

	switch {
	case strings.HasSuffix(name, ".zip"):
		if err := extractZip(archivePath, binName, extracted); err != nil {
			cleanup()
			return nil, nil, err
		}
	case strings.HasSuffix(name, ".tar.gz"):
		if err := extractTarGz(archivePath, binName, extracted); err != nil {
			cleanup()
			return nil, nil, err
		}
	default:
		cleanup()
		return nil, nil, fmt.Errorf("unsupported archive: %s", name)
	}

	f, err := os.Open(extracted)
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	wrapped := func() { _ = f.Close(); cleanup() }
	return f, wrapped, nil
}

func downloadFile(url, dst string) error {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "jr-upgrade")
	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: %s", url, resp.Status)
	}
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, resp.Body)
	return err
}

func extractZip(src, want, dst string) error {
	r, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer r.Close()
	for _, f := range r.File {
		if filepath.Base(f.Name) != want {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		defer rc.Close()
		out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
		if err != nil {
			return err
		}
		defer out.Close()
		_, err = io.Copy(out, rc)
		return err
	}
	return fmt.Errorf("%s not found in %s", want, src)
}

func extractTarGz(src, want, dst string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if filepath.Base(hdr.Name) != want {
			continue
		}
		out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
		if err != nil {
			return err
		}
		defer out.Close()
		_, err = io.Copy(out, tr)
		return err
	}
	return fmt.Errorf("%s not found in %s", want, src)
}

func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for x := n / unit; x >= unit; x /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
