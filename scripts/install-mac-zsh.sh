#!/usr/bin/env sh
# Manual jr install for macOS + zsh — no sudo, no Homebrew.
#
# Drops the binary into ~/.local/bin/jr and the zsh completion into
# ~/.zsh/completions/_jr. Adds a fpath snippet to ~/.zshrc the first
# time it runs (idempotent — re-runs don't duplicate). Re-run any
# time to upgrade in place; bump V to pin a different release.
#
# Apple Silicon by default; for Intel Macs change ARCH to "amd64".

set -eu

V=0.2.0
ARCH=arm64

T=$(mktemp -d)
trap 'rm -rf "$T"' EXIT

curl -fsSL -o "$T/jr.tar.gz" \
  "https://github.com/hugs7/jira-cli/releases/download/v${V}/jr_${V}_darwin_${ARCH}.tar.gz"
tar xzf "$T/jr.tar.gz" -C "$T"

mkdir -p "$HOME/.local/bin" "$HOME/.zsh/completions"
install -m 755 "$T/jr" "$HOME/.local/bin/jr"
"$HOME/.local/bin/jr" completion zsh > "$HOME/.zsh/completions/_jr"

if ! grep -q 'jr completions' "$HOME/.zshrc" 2>/dev/null; then
  printf '\n# jr completions\nfpath=("$HOME/.zsh/completions" $fpath)\nautoload -Uz compinit && compinit\n' >> "$HOME/.zshrc"
fi

jr version
echo "open a new shell (or 'exec zsh') for completion to load"
