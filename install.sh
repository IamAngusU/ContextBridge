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
checksums_url="$(curl -fsSL "$api" | sed -n 's/.*"browser_download_url": *"\([^"]*SHA256SUMS\)".*/\1/p' | head -n 1)"
[ -n "$checksums_url" ] || { echo "Release checksums were not found." >&2; exit 1; }

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT INT TERM
echo "Downloading ContextBridge for $os $arch..."
curl -fsSL "$url" -o "$tmp/$asset"
curl -fsSL "$checksums_url" -o "$tmp/SHA256SUMS"
expected="$(awk -v name="$asset" '$2 == name || $2 ~ ("/" name "$") {print $1}' "$tmp/SHA256SUMS")"
[ -n "$expected" ] || { echo "No checksum was published for $asset." >&2; exit 1; }
if command -v sha256sum >/dev/null 2>&1; then
  actual="$(sha256sum "$tmp/$asset" | awk '{print $1}')"
else
  actual="$(shasum -a 256 "$tmp/$asset" | awk '{print $1}')"
fi
[ "$actual" = "$expected" ] || { echo "ContextBridge download checksum mismatch." >&2; exit 1; }
echo "Download checksum verified."
mkdir -p "$INSTALL_DIR" "$BIN_DIR"
tar -xzf "$tmp/$asset" -C "$INSTALL_DIR"
install -m 0755 "$INSTALL_DIR/contextbridge" "$BIN_DIR/contextbridge"

config="${CONTEXTBRIDGE_CONFIG:-$HOME/.config/contextbridge/config.yml}"
if [ ! -f "$config" ]; then
  "$BIN_DIR/contextbridge" init --config "$config"
fi

if command -v systemctl >/dev/null 2>&1 && systemctl --user show-environment >/dev/null 2>&1; then
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
  if systemctl --user daemon-reload 2>/dev/null && systemctl --user enable --now contextbridge.service 2>/dev/null; then
    echo "ContextBridge user service enabled."
  else
    echo "The user service could not be enabled in this session."
    echo "Start ContextBridge with: $BIN_DIR/contextbridge serve --config $config"
  fi
else
  echo "Start ContextBridge with: $BIN_DIR/contextbridge serve --config $config"
fi

echo "Config: $config"
echo "Browser extension: $INSTALL_DIR/extension"
