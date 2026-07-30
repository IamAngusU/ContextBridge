#!/bin/sh
set -eu

REPO="IamAngusU/ContextBridge"
INSTALL_DIR="${CONTEXTBRIDGE_HOME:-$HOME/.local/share/contextbridge}"
BIN_DIR="${CONTEXTBRIDGE_BIN_DIR:-$HOME/.local/bin}"
provider="${CONTEXTBRIDGE_PROVIDER:-ask}"

if [ "$provider" = "ask" ] && [ -r /dev/tty ]; then
  printf '\nChoose the first local target:\n' >/dev/tty
  printf '  1) Existing Ollama, with automatic local model detection (recommended)\n' >/dev/tty
  printf '  2) Managed llama.cpp runtime and a verified GGUF model\n' >/dev/tty
  printf '  3) A visually taught browser tab\n' >/dev/tty
  printf '  4) Configure it later in YAML\n' >/dev/tty
  printf 'Choose 1, 2, 3, or 4 [1]: ' >/dev/tty
  read -r choice </dev/tty || choice="1"
  case "${choice:-1}" in
    2) provider="managed" ;;
    3) provider="browser" ;;
    4) provider="later" ;;
    *) provider="ollama" ;;
  esac
elif [ "$provider" = "ask" ]; then
  provider="ollama"
fi

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

if [ "$provider" = "browser" ]; then
  sed -i.bak \
    -e '/^  default:/,/^  [A-Za-z0-9_-]*:/{s/^    provider: ollama$/    provider: browser/;s/^    fallback: \[browser\]$/    fallback: []/;}' \
    -e '/^  inkwall:/,/^  [A-Za-z0-9_-]*:/{s/^    provider: ollama$/    provider: browser/;s/^    fallback: \[browser\]$/    fallback: []/;}' \
    "$config"
  rm -f "$config.bak"
elif [ "$provider" = "ollama" ]; then
  sed -i.bak \
    -e '/^  default:/,/^  [A-Za-z0-9_-]*:/{s/^    provider: browser$/    provider: ollama/;s/^    fallback: \[\]$/    fallback: [browser]/;}' \
    -e '/^  inkwall:/,/^  [A-Za-z0-9_-]*:/{s/^    provider: browser$/    provider: ollama/;s/^    fallback: \[\]$/    fallback: [browser]/;}' \
    "$config"
  rm -f "$config.bak"
elif [ "$provider" = "managed" ]; then
  managed_model="${CONTEXTBRIDGE_MANAGED_MODEL:-jina}"
  if [ -r /dev/tty ] && [ -z "${CONTEXTBRIDGE_MANAGED_MODEL:-}" ]; then
    printf '\nChoose the first managed workload:\n' >/dev/tty
    printf '  1) Jina v4 retrieval embeddings\n' >/dev/tty
    printf '  2) NuExtract3 structured extraction\n' >/dev/tty
    printf '  3) Both models\n' >/dev/tty
    printf 'Choose 1, 2, or 3 [1]: ' >/dev/tty
    read -r model_choice </dev/tty || model_choice="1"
    case "${model_choice:-1}" in 2) managed_model="nuextract" ;; 3) managed_model="both" ;; *) managed_model="jina" ;; esac
  fi
  echo "Installing the verified llama.cpp runtime..."
  "$BIN_DIR/contextbridge" runtime install --config "$config" llama.cpp
  if [ "$managed_model" = "jina" ] || [ "$managed_model" = "both" ]; then
    sed -i.bak '/^  jina:/,/^  [A-Za-z0-9_-]*:/{s/^    auto_start: false$/    auto_start: true/;}' "$config"
    rm -f "$config.bak"
    "$BIN_DIR/contextbridge" pull --config "$config" jina-v4-retrieval
  fi
  if [ "$managed_model" = "nuextract" ] || [ "$managed_model" = "both" ]; then
    sed -i.bak '/^  nuextract:/,/^  [A-Za-z0-9_-]*:/{s/^    auto_start: false$/    auto_start: true/;}' "$config"
    rm -f "$config.bak"
    "$BIN_DIR/contextbridge" pull --config "$config" nuextract3
  fi
fi

if [ "$os" = "linux" ] && command -v systemctl >/dev/null 2>&1 && systemctl --user show-environment >/dev/null 2>&1; then
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
elif [ "$os" = "darwin" ]; then
  agent_dir="$HOME/Library/LaunchAgents"
  agent="$agent_dir/de.angusu.contextbridge.plist"
  mkdir -p "$agent_dir"
  cat > "$agent" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>de.angusu.contextbridge</string>
  <key>ProgramArguments</key><array><string>$BIN_DIR/contextbridge</string><string>serve</string><string>--config</string><string>$config</string></array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict>
  <key>StandardOutPath</key><string>$INSTALL_DIR/contextbridge.log</string>
  <key>StandardErrorPath</key><string>$INSTALL_DIR/contextbridge-error.log</string>
</dict></plist>
EOF
  launchctl bootout "gui/$(id -u)" "$agent" >/dev/null 2>&1 || true
  if launchctl bootstrap "gui/$(id -u)" "$agent" >/dev/null 2>&1; then
    echo "ContextBridge launch agent enabled."
  else
    echo "Start ContextBridge with: $BIN_DIR/contextbridge serve --config $config"
  fi
else
  echo "Start ContextBridge with: $BIN_DIR/contextbridge serve --config $config"
fi

echo "Config: $config"
echo "Chromium extension: $INSTALL_DIR/extension/chromium"
echo "Firefox extension: $INSTALL_DIR/extension/firefox"

if [ "${CONTEXTBRIDGE_NO_DASHBOARD:-0}" != "1" ]; then
  if [ "$os" = "darwin" ] || [ -n "${DISPLAY:-}${WAYLAND_DISPLAY:-}" ]; then
    sleep 1
    "$BIN_DIR/contextbridge" dashboard --config "$config" || true
  else
    echo "Dashboard: http://127.0.0.1:32145"
  fi
fi
