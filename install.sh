#!/bin/sh
set -eu

REPO="IamAngusU/ContextBridge"
INSTALL_DIR="${CONTEXTBRIDGE_HOME:-$HOME/.local/share/contextbridge}"
BIN_DIR="${CONTEXTBRIDGE_BIN_DIR:-$HOME/.local/bin}"
provider="${CONTEXTBRIDGE_PROVIDER:-ask}"
cluster_mode="${CONTEXTBRIDGE_CLUSTER_MODE:-ask}"
relay_url="${CONTEXTBRIDGE_RELAY_URL:-}"
public_url="${CONTEXTBRIDGE_PUBLIC_URL:-}"
worker_name="${CONTEXTBRIDGE_WORKER_NAME:-auto}"
completion_enabled=1
if [ "${CONTEXTBRIDGE_NO_COMPLETION:-0}" = "1" ]; then completion_enabled=0; fi
interactive=0
if [ "${CONTEXTBRIDGE_NONINTERACTIVE:-0}" != "1" ] && [ -r /dev/tty ]; then
  interactive=1
fi
if [ "$cluster_mode" = "worker" ] && [ "$interactive" = "0" ] && [ -z "$relay_url" ]; then
  echo "A relay URL is required for worker mode. Set CONTEXTBRIDGE_RELAY_URL for an unattended install." >&2
  exit 1
fi
if [ -n "$relay_url" ]; then
  case "$relay_url" in
    https://*|http://127.0.0.1:*|http://localhost:*) ;;
    *) echo "CONTEXTBRIDGE_RELAY_URL must use HTTPS or a localhost URL." >&2; exit 1 ;;
  esac
fi

if [ "$provider" = "ask" ] && [ "$interactive" = "1" ]; then
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
expected="$(awk -v name="$asset" '{sub(/\r$/, "", $2)} $2 == name || $2 ~ ("/" name "$") {print $1}' "$tmp/SHA256SUMS")"
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

# Keep one canonical executable and a path-stable launcher for the short name.
# A copied binary could become stale after an update. Never replace an
# unrelated `cb` command or a user-owned file.
cb_path="$BIN_DIR/cb"
cb_alias_installed=0
if [ -L "$cb_path" ]; then
  cb_target="$(readlink "$cb_path" 2>/dev/null || true)"
  if [ "$cb_target" = "contextbridge" ] || [ "$cb_target" = "$BIN_DIR/contextbridge" ]; then
    ln -sfn contextbridge "$cb_path"
    cb_alias_installed=1
  else
    echo "Skipped the short 'cb' command because $cb_path points elsewhere."
  fi
elif [ -e "$cb_path" ]; then
  if [ "$(sed -n '2p' "$cb_path" 2>/dev/null || true)" = "# ContextBridge managed cb alias" ]; then
    printf '#!/bin/sh\n# ContextBridge managed cb alias\nexec "$(dirname -- "$0")/contextbridge" "$@"\n' > "$tmp/cb"
    install -m 0755 "$tmp/cb" "$cb_path"
    cb_alias_installed=1
  else
    echo "Skipped the short 'cb' command because $cb_path is not managed by ContextBridge."
  fi
else
  existing_cb="$(command -v cb 2>/dev/null || true)"
  if [ -n "$existing_cb" ] && [ "$existing_cb" != "$cb_path" ]; then
    echo "Skipped the short 'cb' command because it already belongs to $existing_cb."
  else
    printf '#!/bin/sh\n# ContextBridge managed cb alias\nexec "$(dirname -- "$0")/contextbridge" "$@"\n' > "$tmp/cb"
    install -m 0755 "$tmp/cb" "$cb_path"
    cb_alias_installed=1
  fi
fi
if [ "$cb_alias_installed" = "1" ]; then
  resolved_cb="$(command -v cb 2>/dev/null || true)"
  if [ -n "$resolved_cb" ] && [ "$resolved_cb" != "$cb_path" ]; then
    # An owned launcher can survive a later PATH reorder. Do not let its mere
    # presence claim completion ownership for the different command that the
    # user's shell would actually execute.
    echo "The managed cb launcher remains at $cb_path, but the active cb command belongs to $resolved_cb."
    cb_alias_installed=0
  fi
fi
if [ "$cb_alias_installed" = "1" ]; then
  echo "Commands ready: contextbridge and cb"
fi

install_shell_completion() {
  # bash-completion lazy-loads files from this per-user XDG directory without
  # a shell profile rewrite on standard installations.
  bash_completion_dir="${XDG_DATA_HOME:-$HOME/.local/share}/bash-completion/completions"
  mkdir -p "$bash_completion_dir" || return 1
  zsh_completion_dir="$HOME/.zfunc"
  if [ "$cb_alias_installed" != "1" ]; then
    # Ownership cleanup is independent from generating a new script. Do it
    # first so a missing/older generator cannot leave our stale registration
    # attached to a foreign command.
    if [ -f "$bash_completion_dir/cb" ] && grep -Fqx '# ContextBridge managed completion' "$bash_completion_dir/cb"; then
      rm -f "$bash_completion_dir/cb" || return 1
    fi
    if [ -f "$zsh_completion_dir/_cb" ] && grep -Fqx '# ContextBridge managed completion' "$zsh_completion_dir/_cb"; then
      rm -f "$zsh_completion_dir/_cb" || return 1
    fi
  fi
  "$BIN_DIR/contextbridge" completion bash > "$tmp/contextbridge-completion.bash" || return 1
  if [ "$cb_alias_installed" != "1" ]; then
    sed 's/^complete -o default -F _contextbridge_complete contextbridge cb$/complete -o default -F _contextbridge_complete contextbridge/' \
      "$tmp/contextbridge-completion.bash" > "$tmp/contextbridge-completion-scoped.bash" || return 1
    grep -Fqx 'complete -o default -F _contextbridge_complete contextbridge' "$tmp/contextbridge-completion-scoped.bash" || return 1
    if grep -Fqx 'complete -o default -F _contextbridge_complete contextbridge cb' "$tmp/contextbridge-completion-scoped.bash"; then return 1; fi
    mv "$tmp/contextbridge-completion-scoped.bash" "$tmp/contextbridge-completion.bash" || return 1
  fi
  install -m 0644 "$tmp/contextbridge-completion.bash" "$bash_completion_dir/contextbridge" || return 1
  if [ "$cb_alias_installed" = "1" ]; then
    install -m 0644 "$tmp/contextbridge-completion.bash" "$bash_completion_dir/cb" || return 1
  fi

  if command -v zsh >/dev/null 2>&1; then
    mkdir -p "$zsh_completion_dir" || return 1
    "$BIN_DIR/contextbridge" completion zsh > "$tmp/_contextbridge" || return 1
    if [ "$cb_alias_installed" != "1" ]; then
      sed 's/^#compdef contextbridge cb$/#compdef contextbridge/' "$tmp/_contextbridge" > "$tmp/_contextbridge-scoped" || return 1
      grep -Fqx '#compdef contextbridge' "$tmp/_contextbridge-scoped" || return 1
      if grep -Fqx '#compdef contextbridge cb' "$tmp/_contextbridge-scoped"; then return 1; fi
      mv "$tmp/_contextbridge-scoped" "$tmp/_contextbridge" || return 1
    fi
    install -m 0644 "$tmp/_contextbridge" "$zsh_completion_dir/_contextbridge" || return 1
    zsh_completion_commands="contextbridge"
    if [ "$cb_alias_installed" = "1" ]; then
      install -m 0644 "$tmp/_contextbridge" "$zsh_completion_dir/_cb" || return 1
      zsh_completion_commands="contextbridge cb"
    fi
    zsh_rc="${ZDOTDIR:-$HOME}/.zshrc"
    zsh_rc_target="$zsh_rc"
    zsh_link_depth=0
    while [ -L "$zsh_rc_target" ]; do
      zsh_link_depth=$((zsh_link_depth + 1))
      [ "$zsh_link_depth" -le 16 ] || return 1
      zsh_link_value="$(readlink "$zsh_rc_target")" || return 1
      case "$zsh_link_value" in
        /*) zsh_rc_target="$zsh_link_value" ;;
        *) zsh_rc_target="$(dirname -- "$zsh_rc_target")/$zsh_link_value" ;;
      esac
    done
    zsh_rc_dir="$(dirname -- "$zsh_rc_target")"
    [ -d "$zsh_rc_dir" ] || return 1
    zsh_rc_stage="$(mktemp "$zsh_rc_dir/.contextbridge-zshrc.XXXXXX")" || return 1
    zsh_rc_content="$tmp/contextbridge-zshrc-content"
    zsh_marker_state="none"
    if [ -f "$zsh_rc_target" ]; then
      if awk '
        BEGIN { state = 0; starts = 0; ends = 0; invalid = 0 }
        $0 == "# >>> ContextBridge completion >>>" { starts++; if (state != 0) invalid = 1; state = 1; next }
        $0 == "# <<< ContextBridge completion <<<" { ends++; if (state != 1) invalid = 1; state = 0; next }
        END {
          if (state != 0) invalid = 1
          if (starts == 0 && ends == 0) exit 2
          if (starts != 1 || ends != 1 || invalid) exit 3
        }
      ' "$zsh_rc_target"; then
        zsh_marker_state="valid"
      else
        zsh_marker_exit=$?
        if [ "$zsh_marker_exit" -ne 2 ]; then
          # Ambiguous ownership markers are never authority to rewrite a shell
          # profile. Leave it byte-for-byte intact and skip optional completion.
          rm -f "$zsh_rc_stage"
          return 1
        fi
      fi
    fi
    if [ "$zsh_marker_state" = "valid" ]; then
      # Replace only ContextBridge's exact managed block. This also removes an
      # older `cb` compdef when that command now belongs to another program.
      awk '
        $0 == "# >>> ContextBridge completion >>>" { managed = 1; next }
        $0 == "# <<< ContextBridge completion <<<" { managed = 0; next }
        !managed { print }
      ' "$zsh_rc_target" > "$zsh_rc_content" || { rm -f "$zsh_rc_stage"; return 1; }
    elif [ -f "$zsh_rc_target" ]; then
      cp "$zsh_rc_target" "$zsh_rc_content" || { rm -f "$zsh_rc_stage"; return 1; }
    else
      : > "$zsh_rc_content" || { rm -f "$zsh_rc_stage"; return 1; }
    fi
    if [ -f "$zsh_rc_target" ]; then
      # Copy metadata first, then replace only the staging file's contents. The
      # eventual atomic rename keeps an existing regular file's mode, and the
      # symlink resolution above keeps dotfile-manager links intact.
      cp -p "$zsh_rc_target" "$zsh_rc_stage" || { rm -f "$zsh_rc_stage"; return 1; }
    fi
    cp "$zsh_rc_content" "$zsh_rc_stage" || { rm -f "$zsh_rc_stage"; return 1; }
    {
      printf '\n# >>> ContextBridge completion >>>\n'
      printf 'fpath=("$HOME/.zfunc" $fpath)\n'
      printf 'autoload -Uz _contextbridge\n'
      printf 'if (( $+functions[compdef] )); then compdef _contextbridge %s; else autoload -Uz compinit && compinit; fi\n' "$zsh_completion_commands"
      printf '# <<< ContextBridge completion <<<\n'
    } >> "$zsh_rc_stage" || { rm -f "$zsh_rc_stage"; return 1; }
    mv -f "$zsh_rc_stage" "$zsh_rc_target" || { rm -f "$zsh_rc_stage"; return 1; }
  fi
  return 0
}

if [ "$completion_enabled" = "1" ]; then
  if install_shell_completion; then
    echo "Shell completion installed (open a new shell)."
  else
    echo "Shell completion could not be installed; ContextBridge itself is ready. Run: contextbridge completion bash|zsh" >&2
  fi
fi

config="${CONTEXTBRIDGE_CONFIG:-$HOME/.config/contextbridge/config.yml}"
if [ ! -f "$config" ]; then
  "$BIN_DIR/contextbridge" init --config "$config"
fi

if [ "$cluster_mode" = "ask" ] && [ "$interactive" = "1" ]; then
  printf '\nChoose how this device participates:\n' >/dev/tty
  printf '  1) Local bridge only (recommended for a first install)\n' >/dev/tty
  printf '  2) Relay for other devices\n' >/dev/tty
  printf '  3) Worker for an existing relay\n' >/dev/tty
  printf '  4) Relay and worker on this device\n' >/dev/tty
  printf 'Choose 1, 2, 3, or 4 [1]: ' >/dev/tty
  read -r cluster_choice </dev/tty || cluster_choice="1"
  case "${cluster_choice:-1}" in 2) cluster_mode="relay" ;; 3) cluster_mode="worker" ;; 4) cluster_mode="all" ;; *) cluster_mode="local" ;; esac
elif [ "$cluster_mode" = "ask" ]; then
  cluster_mode="local"
fi

if [ "$cluster_mode" = "worker" ] && [ -z "$relay_url" ] && [ "$interactive" = "1" ]; then
  printf 'Public HTTPS relay URL: ' >/dev/tty
  read -r relay_url </dev/tty
fi
if { [ "$cluster_mode" = "relay" ] || [ "$cluster_mode" = "all" ]; } && [ -z "$public_url" ] && [ "$interactive" = "1" ]; then
  printf 'Public HTTPS relay URL, or leave empty while configuring the reverse proxy: ' >/dev/tty
  read -r public_url </dev/tty || public_url=""
fi
if [ "$cluster_mode" = "worker" ] && [ -z "$relay_url" ]; then
  echo "A relay URL is required for worker mode. Set CONTEXTBRIDGE_RELAY_URL for an unattended install." >&2
  exit 1
fi
if [ -n "$relay_url" ]; then
  "$BIN_DIR/contextbridge" cluster configure --config "$config" --mode "$cluster_mode" --relay-url "$relay_url" --name "$worker_name"
elif [ -n "$public_url" ]; then
  "$BIN_DIR/contextbridge" cluster configure --config "$config" --mode "$cluster_mode" --listen auto --public-url "$public_url" --name "$worker_name"
elif [ "$cluster_mode" = "relay" ] || [ "$cluster_mode" = "all" ]; then
  "$BIN_DIR/contextbridge" cluster configure --config "$config" --mode "$cluster_mode" --listen auto --name "$worker_name"
else
  "$BIN_DIR/contextbridge" cluster configure --config "$config" --mode "$cluster_mode" --name "$worker_name"
fi
if [ "$cluster_mode" = "worker" ] || [ "$cluster_mode" = "all" ]; then
  "$BIN_DIR/contextbridge" pair --config "$config" --name "$worker_name"
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
  if [ "$interactive" = "1" ] && [ -z "${CONTEXTBRIDGE_MANAGED_MODEL:-}" ]; then
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
ExecStart="$BIN_DIR/contextbridge" run --config "$config"
Restart=on-failure
RestartSec=3

[Install]
WantedBy=default.target
EOF
  cat > "$unit_dir/contextbridge-update.service" <<EOF
[Unit]
Description=ContextBridge verified automatic update
After=network-online.target

[Service]
Type=oneshot
ExecStart="$BIN_DIR/contextbridge" update auto --config "$config"
EOF
  cat > "$unit_dir/contextbridge-update.timer" <<EOF
[Unit]
Description=Check for ContextBridge updates daily

[Timer]
OnBootSec=15m
OnUnitActiveSec=24h
RandomizedDelaySec=2h
Persistent=true

[Install]
WantedBy=timers.target
EOF
  if systemctl --user daemon-reload 2>/dev/null && systemctl --user enable --now contextbridge.service contextbridge-update.timer 2>/dev/null; then
    echo "ContextBridge user service enabled."
  else
    echo "The user service could not be enabled in this session."
    echo "Start ContextBridge with: $BIN_DIR/contextbridge run --config $config"
  fi
elif [ "$os" = "darwin" ]; then
  agent_dir="$HOME/Library/LaunchAgents"
  agent="$agent_dir/de.angusu.contextbridge.plist"
  update_agent="$agent_dir/de.angusu.contextbridge.update.plist"
  mkdir -p "$agent_dir"
  cat > "$agent" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>de.angusu.contextbridge</string>
  <key>ProgramArguments</key><array><string>$BIN_DIR/contextbridge</string><string>run</string><string>--config</string><string>$config</string></array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict>
  <key>StandardOutPath</key><string>$INSTALL_DIR/contextbridge.log</string>
  <key>StandardErrorPath</key><string>$INSTALL_DIR/contextbridge-error.log</string>
</dict></plist>
EOF
  cat > "$update_agent" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>de.angusu.contextbridge.update</string>
  <key>ProgramArguments</key><array><string>$BIN_DIR/contextbridge</string><string>update</string><string>auto</string><string>--config</string><string>$config</string></array>
  <key>StartInterval</key><integer>86400</integer>
  <key>ProcessType</key><string>Background</string>
  <key>StandardOutPath</key><string>$INSTALL_DIR/contextbridge-update.log</string>
  <key>StandardErrorPath</key><string>$INSTALL_DIR/contextbridge-update-error.log</string>
</dict></plist>
EOF
  launchctl bootout "gui/$(id -u)" "$agent" >/dev/null 2>&1 || true
  launchctl bootout "gui/$(id -u)" "$update_agent" >/dev/null 2>&1 || true
  if launchctl bootstrap "gui/$(id -u)" "$agent" >/dev/null 2>&1; then
    launchctl bootstrap "gui/$(id -u)" "$update_agent" >/dev/null 2>&1 || true
    echo "ContextBridge launch agent enabled."
  else
    echo "Start ContextBridge with: $BIN_DIR/contextbridge run --config $config"
  fi
else
  echo "Start ContextBridge with: $BIN_DIR/contextbridge run --config $config"
fi

echo "Config: $config"
echo "Chromium extension: $INSTALL_DIR/extension/chromium"
echo "Firefox extension: $INSTALL_DIR/extension/firefox"
case ":$PATH:" in
  *":$BIN_DIR:"*) ;;
  *) echo "Add $BIN_DIR to PATH to run contextbridge without its full path." ;;
esac

if [ "${CONTEXTBRIDGE_NO_DASHBOARD:-0}" != "1" ]; then
  if [ "$os" = "darwin" ] || [ -n "${DISPLAY:-}${WAYLAND_DISPLAY:-}" ]; then
    sleep 1
    "$BIN_DIR/contextbridge" dashboard --config "$config" || true
  else
    echo "Dashboard: http://127.0.0.1:32145"
  fi
fi
