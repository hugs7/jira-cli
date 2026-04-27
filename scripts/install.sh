#!/usr/bin/env sh
# Install the latest release of `jr` from GitHub Releases.
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/hugs7/jira-cli/main/scripts/install.sh | sh
#   curl -fsSL .../install.sh | sh -s -- --version v0.2.0 --bin-dir ~/.local/bin

set -eu

REPO="hugs7/jira-cli"
BIN="jr"
VERSION=""
BIN_DIR="${BIN_DIR:-/usr/local/bin}"

while [ $# -gt 0 ]; do
  case "$1" in
    --version) VERSION="$2"; shift 2 ;;
    --bin-dir) BIN_DIR="$2"; shift 2 ;;
    -h|--help)
      sed -n '2,6p' "$0"; exit 0 ;;
    *) echo "unknown flag: $1" >&2; exit 2 ;;
  esac
done

uname_os() {
  os=$(uname -s | tr '[:upper:]' '[:lower:]')
  case "$os" in
    linux)  echo linux ;;
    darwin) echo darwin ;;
    *) echo "unsupported OS: $os" >&2; exit 1 ;;
  esac
}

uname_arch() {
  arch=$(uname -m)
  case "$arch" in
    x86_64|amd64) echo amd64 ;;
    arm64|aarch64) echo arm64 ;;
    *) echo "unsupported arch: $arch" >&2; exit 1 ;;
  esac
}

OS=$(uname_os)
ARCH=$(uname_arch)

if [ -z "$VERSION" ]; then
  VERSION=$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" \
    | grep -m1 '"tag_name"' | cut -d'"' -f4)
fi
if [ -z "$VERSION" ]; then
  echo "failed to detect latest version" >&2; exit 1
fi
NUM="${VERSION#v}"

ASSET="${BIN}_${NUM}_${OS}_${ARCH}.tar.gz"
URL="https://github.com/${REPO}/releases/download/${VERSION}/${ASSET}"

echo "downloading ${ASSET}…"
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT
curl -fsSL "$URL" -o "${TMP}/${ASSET}"
tar -xzf "${TMP}/${ASSET}" -C "${TMP}"

if [ ! -d "$BIN_DIR" ]; then
  mkdir -p "$BIN_DIR" 2>/dev/null || sudo mkdir -p "$BIN_DIR"
fi

if [ -w "$BIN_DIR" ]; then
  install -m 0755 "${TMP}/${BIN}" "${BIN_DIR}/${BIN}"
else
  sudo install -m 0755 "${TMP}/${BIN}" "${BIN_DIR}/${BIN}"
fi

echo "installed ${BIN} ${VERSION} → ${BIN_DIR}/${BIN}"
"${BIN_DIR}/${BIN}" version || true
