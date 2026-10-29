#!/bin/sh
# Install the latest claudesync release into ~/.local/bin (or $CLAUDESYNC_BIN).
set -eu
repo="Hudint/claudesync"
bin="${CLAUDESYNC_BIN:-$HOME/.local/bin}"
os=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$(uname -m)" in
  x86_64|amd64) arch=amd64 ;;
  arm64|aarch64) arch=arm64 ;;
  *) echo "unsupported architecture: $(uname -m)" >&2; exit 1 ;;
esac
url="https://github.com/$repo/releases/latest/download/claudesync_${os}_${arch}.tar.gz"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
echo "downloading $url"
curl -fsSL "$url" | tar -xz -C "$tmp"
mkdir -p "$bin"
install -m 0755 "$tmp/claudesync" "$bin/claudesync"
echo "installed $bin/claudesync"
case ":$PATH:" in *":$bin:"*) ;; *) echo "note: add $bin to your PATH" ;; esac
