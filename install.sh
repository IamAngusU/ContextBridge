#!/bin/sh
set -eu

REPO="IamAngusU/ContextBridge"
INSTALL_DIR="${CONTEXTBRIDGE_HOME:-$HOME/.local/share/contextbridge}"
BIN_DIR="${CONTEXTBRIDGE_BIN_DIR:-$HOME/.local/bin}"

case "$(uname -s)" in
  Linux) os="linux" ;;
  Darwin) os="darwin" ;;
  *) echo "Unsupported operating system" >&2; exit 1 ;;
esac

case "$(uname -m)" in
  x86_64|amd64) arch="amd64" ;;
  arm64|aarch64) arch="arm64" ;;
  *) echo "Unsupported architecture" >&2; exit 1 ;;
esac

asset="contextbridge_${os}_${arch}.tar.gz"
api="https://api.github.com/repos/$REPO/releases/latest"
url="$(curl -fsSL "$api" | sed -n "s/.*\"browser_download_url\": *\"\([^\"]*${asset}\)\".*/\1/p" | head -n 1)"
[ -n "$url" ] || { echo "Release asset $asset was not found." >&2; exit 1; }

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT INT TERM
echo "Downloading ContextBridge for $os $arch..."
curl -fsSL "$url" -o "$tmp/$asset"
mkdir -p "$INSTALL_DIR" "$BIN_DIR"
tar -xzf "$tmp/$asset" -C "$INSTALL_DIR"
install -m 0755 "$INSTALL_DIR/contextbridge" "$BIN_DIR/contextbridge"

config="${CONTEXTBRIDGE_CONFIG:-$HOME/.config/contextbridge/config.yml}"
if [ ! -f "$config" ]; then
  "$BIN_DIR/contextbridge" init --config "$config"
fi

if command -v systemctl >/dev/null 2>&1; then
  unit_dir="$HOME/.config/systemd/user"
  mkdir -p "$unit_dir"
  cat > "$unit_dir/contextbridge.service" <<EOF
[Unit]
Description=ContextBridge local model and browser bridge
After=network-online.target

[Service]
ExecStart="$BIN_DIR/contextbridge" serve --config "$config"
Restart=on-failure
RestartSec=3

[Install]
WantedBy=default.target
EOF
  systemctl --user daemon-reload
  systemctl --user enable --now contextbridge.service
  echo "ContextBridge user service enabled."
else
  echo "Start ContextBridge with: $BIN_DIR/contextbridge serve --config $config"
fi

echo "Config: $config"
echo "Browser extension: $INSTALL_DIR/extension"
