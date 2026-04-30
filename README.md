# jr — Jira CLI

A fast, comprehensive command-line interface and TUI for Jira Cloud and
Jira Data Center / Server.

> Status: early scaffolding. Expect breaking changes.

## Install

Pick the method for your platform. See [PUBLISHING.md](PUBLISHING.md)
for how releases are built.

### Package managers (recommended)

```sh
# macOS / Linux — Homebrew
brew install hugs7/tap/jira-cli

# Windows — Scoop
scoop bucket add hugs7 https://github.com/hugs7/scoop-bucket
scoop install jr

# Debian / Ubuntu — apt
curl -1sLf 'https://dl.cloudsmith.io/public/hugs7/jira-cli/setup.deb.sh' | sudo -E bash
sudo apt install jira-cli

# Fedora / RHEL — dnf
curl -1sLf 'https://dl.cloudsmith.io/public/hugs7/jira-cli/setup.rpm.sh' | sudo -E bash
sudo dnf install jira-cli

# Alpine — apk
curl -1sLf 'https://dl.cloudsmith.io/public/hugs7/jira-cli/setup.alpine.sh' | sudo -E bash
sudo apk add jira-cli
```

### Install scripts (no package manager required)

Useful for CI, Docker images, exotic distros, or just trying it
quickly:

```sh
# Linux / macOS
curl -fsSL https://raw.githubusercontent.com/hugs7/jira-cli/main/scripts/install.sh | sh

# Windows (PowerShell)
irm https://raw.githubusercontent.com/hugs7/jira-cli/main/scripts/install.ps1 | iex
```

### Manual install (macOS + zsh, no sudo, no Homebrew)

Drops the binary into `~/.local/bin/jr` and the zsh completion into
`~/.zsh/completions/_jr`, and adds the necessary `fpath` snippet to
`~/.zshrc` (idempotent — re-run any time to upgrade in place):

```sh
curl -fsSL https://raw.githubusercontent.com/hugs7/jira-cli/main/scripts/install-mac-zsh.sh | sh
```

Make sure `~/.local/bin` is on your `$PATH`.

### From source

```sh
git clone https://github.com/hugs7/jira-cli
cd jira-cli
go build -o jr ./cmd/jr
./jr --help
```

## Updating

| Installed via | Update with |
|---|---|
| Homebrew | `brew upgrade jira-cli` |
| Scoop | `scoop update jr` |
| apt (Cloudsmith) | `sudo apt update && sudo apt upgrade jira-cli` |
| dnf (Cloudsmith) | `sudo dnf upgrade jira-cli` |
| apk (Cloudsmith) | `sudo apk upgrade jira-cli` |
| `curl \| sh` script / direct binary | `jr upgrade` |

`jr upgrade` checks GitHub Releases and atomically replaces the
running binary on Linux, macOS and Windows. Use `jr upgrade --check`
to peek without installing.

## Configuration

Config lives at `~/.config/jr/config.yml` (override with `--config`).

Per-repo overrides go in `.jr.yml` at the repo root.

## License

MIT
