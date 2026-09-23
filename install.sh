#!/usr/bin/env bash
# install.sh — copy-paste one-liner install of asdf-tui into /usr/local/bin
# from the latest GitHub Release. One dependency (curl), zero config:
#
#   bash <(curl -sfL https://raw.githubusercontent.com/BrunIF/asdf-tui/main/install.sh)
#
# It detects the OS/arch, downloads the matching prebuilt binary from the
# release, installs it (sudo only when /usr/local/bin is not writable), runs
# `asdf-tui version` to confirm, then tells you the next command.

set -euo pipefail

REPO="BrunIF/asdf-tui"

pretty() { printf "\033[1;32m==>\033[0m %s\n" "$*" >&2; }
step()   { printf "\033[1;34m-->\033[0m %s\n" "$*" >&2; }
warn()   { printf "\033[1;33m==>\033[0m %s\n" "$*" >&2; }
die()    { printf "\033[1;31m==>\033[0m %s\n" "$*" >&2; exit 1; }

os="$(uname -s | tr '[:upper:]' '[:lower:]')"
case "$(uname -m)" in
  x86_64|amd64)  arch="amd64" ;;
  aarch64|arm64) arch="arm64" ;;
  *) die "unsupported architecture: $(uname -m) (only amd64/arm64)" ;;
esac

case "$os" in
  linux|darwin) : ;;
  *) die "unsupported OS: $os (only linux/darwin)" ;;
esac

pretty "Installing asdf-tui (${os}/${arch}) into /usr/local/bin"

step "Querying the latest release from $REPO"
api="$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest")"
tag="$(printf '%s' "$api" | sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p')"
ver="${tag#v}"
[ -n "${ver:-}" ] || die "could not read the latest release tag; is there a release yet?"
step "Latest release: $tag"

asset="asdf-tui-${ver}-${os}-${arch}"
url="https://github.com/$REPO/releases/download/${tag}/${asset}"
step "Downloading $asset"
tmp="$(mktemp)"
curl -fsSL "$url" -o "$tmp"
chmod +x "$tmp"

dest="/usr/local/bin/asdf-tui"
if [ -w /usr/local/bin ] || [ "$(id -u)" -eq 0 ]; then
  install -m 0755 "$tmp" "$dest"
else
  step "/usr/local/bin not writable, using sudo"
  sudo install -m 0755 "$tmp" "$dest"
fi
rm -f "$tmp"

pretty "Installed: $dest"
"$dest" version
step "Run it:"
printf "\n  asdf-tui\n\n" >&2
