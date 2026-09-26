#!/bin/sh
set -eu

REPO="IamAngusU/ContextBridge"
INSTALL_DIR="${CONTEXTBRIDGE_HOME:-$HOME/.local/share/contextbridge}"
BIN_DIR="${CONTEXTBRIDGE_BIN_DIR:-$HOME/.local/bin}"
provider="${CONTEXTBRIDGE_PROVIDER:-ask}"
cluster_mode="${CONTEXTBRIDGE_CLUSTER_MODE:-ask}"
relay_url="${CONTEXTBRIDGE_RELAY_URL:-}"
lan_bundle="${CONTEXTBRIDGE_LAN_BUNDLE:-}"
public_url="${CONTEXTBRIDGE_PUBLIC_URL:-}"
worker_name="${CONTEXTBRIDGE_WORKER_NAME:-auto}"
command_name="${CONTEXTBRIDGE_COMMAND:-}"
completion_enabled=1
if [ "${CONTEXTBRIDGE_NO_COMPLETION:-0}" = "1" ]; then completion_enabled=0; fi
interactive=0
if [ "${CONTEXTBRIDGE_NONINTERACTIVE:-0}" != "1" ] && [ -r /dev/tty ]; then
  interactive=1
fi

valid_command_name() {
  case "$1" in
    ""|[!A-Za-z]*|*[!A-Za-z0-9_-]*) return 1 ;;
  esac
  [ "${#1}" -le 32 ] && [ "$1" != "contextbridge" ] && [ "$1" != "cb" ]
}

managed_alias_path() {
  candidate="$1"
  [ -L "$candidate" ] && {
    target="$(readlink "$candidate" 2>/dev/null || true)"
    [ "$target" = "contextbridge" ] || [ "$target" = "$BIN_DIR/contextbridge" ] || [ "$target" = "$INSTALL_DIR/contextbridge" ]
    return
  }
  [ -f "$candidate" ] && {
    marker="$(sed -n '2p' "$candidate" 2>/dev/null || true)"
    [ "$marker" = "# ContextBridge managed cb alias" ] || [ "$marker" = "# ContextBridge managed command alias" ]
    return
  }
  return 1
}

canonical_path="$BIN_DIR/contextbridge"
existing_contextbridge="$(command -v contextbridge 2>/dev/null || true)"
existing_cb="$(command -v cb 2>/dev/null || true)"
canonical_name_available=1
if [ -n "$existing_contextbridge" ] && [ "$existing_contextbridge" != "$canonical_path" ]; then
  canonical_name_available=0
elif [ -e "$canonical_path" ] || [ -L "$canonical_path" ]; then
	if ! managed_alias_path "$canonical_path" && ! { [ -f "$INSTALL_DIR/contextbridge" ] && cmp -s "$canonical_path" "$INSTALL_DIR/contextbridge"; }; then
    canonical_name_available=0
  fi
fi
cb_name_available=1
if [ -n "$existing_cb" ] && [ "$existing_cb" != "$BIN_DIR/cb" ]; then
  cb_name_available=0
elif [ -e "$BIN_DIR/cb" ] || [ -L "$BIN_DIR/cb" ]; then
  managed_alias_path "$BIN_DIR/cb" || cb_name_available=0
fi

if [ -n "$command_name" ] && ! valid_command_name "$command_name"; then
  echo "CONTEXTBRIDGE_COMMAND must use 1-32 letters, numbers, underscores, or hyphens, start with a letter, and differ from contextbridge/cb." >&2
  exit 1
fi
if [ -z "$command_name" ] && [ "$canonical_name_available" = "0" ] && [ "$cb_name_available" = "0" ]; then
  if [ "$interactive" = "0" ]; then
    echo "Both 'contextbridge' and 'cb' already belong to other programs. Set CONTEXTBRIDGE_COMMAND to a custom command name." >&2
    exit 1
  fi
  attempts=0
  while [ -z "$command_name" ] && [ "$attempts" -lt 3 ]; do
    attempts=$((attempts + 1))
    printf "Both 'contextbridge' and 'cb' are taken. Choose a command name for ContextBridge: " >/dev/tty
    read -r candidate </dev/tty || candidate=""
    if ! valid_command_name "$candidate"; then
      printf 'Use 1-32 letters, numbers, underscores, or hyphens, starting with a letter.\n' >/dev/tty
      continue
    fi
    existing_candidate="$(command -v "$candidate" 2>/dev/null || true)"
    if [ -n "$existing_candidate" ] && [ "$existing_candidate" != "$BIN_DIR/$candidate" ]; then
      printf "'%s' is already taken. Choose another name.\n" "$candidate" >/dev/tty
      continue
    fi
    if { [ -e "$BIN_DIR/$candidate" ] || [ -L "$BIN_DIR/$candidate" ]; } && ! managed_alias_path "$BIN_DIR/$candidate"; then
      printf "'%s' is already taken. Choose another name.\n" "$candidate" >/dev/tty
      continue
    fi
    command_name="$candidate"
  done
  [ -n "$command_name" ] || { echo "No safe ContextBridge command name was selected." >&2; exit 1; }
fi
if [ -n "$command_name" ]; then
  existing_custom="$(command -v "$command_name" 2>/dev/null || true)"
  if [ -n "$existing_custom" ] && [ "$existing_custom" != "$BIN_DIR/$command_name" ]; then
    echo "The requested command '$command_name' already belongs to another program at $existing_custom." >&2
    exit 1
  fi
  if { [ -e "$BIN_DIR/$command_name" ] || [ -L "$BIN_DIR/$command_name" ]; } && ! managed_alias_path "$BIN_DIR/$command_name"; then
    echo "Refusing to replace the user-owned command file $BIN_DIR/$command_name." >&2
    exit 1
  fi
fi
case "$cluster_mode" in
  ask|local|client|sender|relay|worker|all) ;;
  *) echo "CONTEXTBRIDGE_CLUSTER_MODE must be ask, local, client/sender, relay, worker, or all." >&2; exit 1 ;;
esac
if [ "$cluster_mode" = "sender" ]; then
  cluster_mode="client"
fi
if [ -n "$relay_url" ] && [ -n "$lan_bundle" ]; then
  echo "Choose either CONTEXTBRIDGE_RELAY_URL or CONTEXTBRIDGE_LAN_BUNDLE, not both." >&2
  exit 1
fi
if [ "$cluster_mode" = "ask" ] && { { [ -n "$relay_url" ] || [ -n "$lan_bundle" ]; } && [ -n "$public_url" ]; }; then
  echo "Conflicting worker and public relay hints were supplied. Set CONTEXTBRIDGE_CLUSTER_MODE explicitly." >&2
  exit 1
fi
if [ "$cluster_mode" = "ask" ] && [ -n "$relay_url" ]; then
  cluster_mode="worker"
  echo "Inferred worker mode from CONTEXTBRIDGE_RELAY_URL."
elif [ "$cluster_mode" = "ask" ] && [ -n "$lan_bundle" ]; then
  cluster_mode="worker"
  echo "Inferred worker mode from CONTEXTBRIDGE_LAN_BUNDLE."
elif [ "$cluster_mode" = "ask" ] && [ -n "$public_url" ]; then
  cluster_mode="relay"
  echo "Inferred relay mode from CONTEXTBRIDGE_PUBLIC_URL."
elif [ "$cluster_mode" = "ask" ] && [ "$interactive" = "0" ]; then
  cluster_mode="local"
fi
if [ "$cluster_mode" = "ask" ] && [ "$interactive" = "1" ]; then
  printf '\nWhat do you want to do on this device?\n' >/dev/tty
  printf '  1) Create a new pool\n     This device coordinates the pool. It can also run AI work.\n' >/dev/tty
  printf '  2) Join an existing pool\n     Add this device hardware, models, or APIs to a pool.\n' >/dev/tty
  printf '  3) Use an existing pool\n     Send work without accepting pool jobs on this device.\n' >/dev/tty
  printf '  4) Use ContextBridge only on this device\n     Keep execution local; no other machine is required.\n' >/dev/tty
  printf '  5) Advanced setup\n     Choose relay, worker, and client roles yourself.\n' >/dev/tty
  printf 'You can change this later. Joining another pool requires approval; moving pool authority is a separate protected operation.\n' >/dev/tty
  printf 'Choose 1, 2, 3, 4, or 5 [4]: ' >/dev/tty
  read -r cluster_choice </dev/tty || cluster_choice="4"
  case "${cluster_choice:-4}" in
    1)
      printf 'Should this device also run AI work? [Y/n]: ' >/dev/tty
      read -r run_work </dev/tty || run_work=""
      case "$(printf '%s' "$run_work" | tr '[:upper:]' '[:lower:]')" in n|no) cluster_mode="relay" ;; *) cluster_mode="all" ;; esac
      ;;
    2) cluster_mode="worker" ;;
    3) cluster_mode="client" ;;
    5)
      printf 'Technical roles: local, client, relay, worker, all\nTechnical role [local]: ' >/dev/tty
      read -r cluster_mode </dev/tty || cluster_mode="local"
      cluster_mode="${cluster_mode:-local}"
      [ "$cluster_mode" = "sender" ] && cluster_mode="client"
      case "$cluster_mode" in local|client|relay|worker|all) ;; *) echo "Technical role must be local, client/sender, relay, worker, or all." >&2; exit 1 ;; esac
      ;;
    *) cluster_mode="local" ;;
  esac
fi
if [ "$cluster_mode" = "worker" ] && [ -z "$relay_url" ] && [ -z "$lan_bundle" ] && [ "$interactive" = "1" ]; then
  printf '\nHow will this device reach the pool?\n' >/dev/tty
  printf '  1) Private network with a trusted join bundle\n     LAN, VLAN, VPN, or another routed private network.\n' >/dev/tty
  printf '  2) HTTPS relay URL\n     Connect to the address provided by the pool owner.\n' >/dev/tty
  printf 'Choose 1 or 2 [1]: ' >/dev/tty
  read -r connection_choice </dev/tty || connection_choice="1"
  if [ "${connection_choice:-1}" = "2" ]; then
    printf 'HTTPS relay URL: ' >/dev/tty
    read -r relay_url </dev/tty
  else
    printf 'Trusted LAN join-bundle path: ' >/dev/tty
    read -r lan_bundle </dev/tty
  fi
fi
if [ -n "$lan_bundle" ] && [ "$cluster_mode" != "worker" ]; then
  echo "CONTEXTBRIDGE_LAN_BUNDLE is only valid when this device joins an existing pool as a worker." >&2
  exit 1
fi
if { [ "$cluster_mode" = "worker" ] || [ "$cluster_mode" = "client" ]; } && [ "$interactive" = "0" ] && [ -z "$relay_url" ] && [ -z "$lan_bundle" ]; then
  echo "A relay URL is required for $cluster_mode mode. Workers may instead set CONTEXTBRIDGE_LAN_BUNDLE." >&2
  exit 1
fi
if [ -n "$relay_url" ]; then
  case "$relay_url" in
    https://*|http://127.0.0.1:*|http://localhost:*) ;;
    *) echo "CONTEXTBRIDGE_RELAY_URL must use HTTPS or a localhost URL." >&2; exit 1 ;;
  esac
fi

if [ "$cluster_mode" = "client" ] && [ "$provider" = "ask" ]; then
  provider="later"
  echo "Sender-only mode does not need a local model provider."
elif [ "$cluster_mode" = "relay" ] && [ "$provider" = "ask" ]; then
  provider="later"
  echo "Coordination-only mode does not need a local model provider."
elif [ "$provider" = "ask" ] && [ "$interactive" = "1" ]; then
  printf '\nAdd execution resources to this device?\n' >/dev/tty
  printf '  1) Use existing Ollama\n     Detect local Ollama models. Nothing is downloaded.\n' >/dev/tty
  printf '  2) Install a managed local runtime\n     Install llama.cpp and a verified model.\n' >/dev/tty
  printf '  3) Skip for now\n     Finish setup without adding a model. You can add one later.\n' >/dev/tty
  printf 'Choose 1, 2, or 3 [1]: ' >/dev/tty
  read -r choice </dev/tty || choice="1"
  case "${choice:-1}" in
    2) provider="managed" ;;
    3) provider="later" ;;
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
chmod 0755 "$INSTALL_DIR/contextbridge"
manifest_stage="$(mktemp "$INSTALL_DIR/.contextbridge-install.XXXXXX")"
if ! cat > "$manifest_stage" <<'EOF'
{"schema_version":1,"product":"ContextBridge","paths":["CHANGELOG.md","LICENSE","LICENSES","LICENSING.md","NOTICE","README.md","SBOM.cdx.json","SOURCE.md","THIRD_PARTY_NOTICES.txt","TRADEMARKS.md","config.example.yml","contextbridge","deploy","docs","examples","install.ps1","install.sh"]}
EOF
then
  rm -f "$manifest_stage"
  exit 1
fi
chmod 0600 "$manifest_stage"
mv -f "$manifest_stage" "$INSTALL_DIR/.contextbridge-install.json"
canonical_command_installed=0
if [ "$canonical_name_available" = "1" ]; then
	if [ -e "$canonical_path" ] || [ -L "$canonical_path" ]; then rm -f "$canonical_path"; fi
	ln -s "$INSTALL_DIR/contextbridge" "$canonical_path"
  canonical_command_installed=1
else
  echo "Preserved the existing 'contextbridge' command; ContextBridge's binary remains at $INSTALL_DIR/contextbridge."
fi

# Keep one canonical executable and a path-stable launcher for the short name.
# A copied binary could become stale after an update. Never replace an
# unrelated `cb` command or a user-owned file.
cb_path="$BIN_DIR/cb"
cb_alias_installed=0
if [ "$cb_name_available" = "1" ]; then
  if [ -e "$cb_path" ] || [ -L "$cb_path" ]; then rm -f "$cb_path"; fi
  ln -s "$INSTALL_DIR/contextbridge" "$cb_path"
  cb_alias_installed=1
else
  echo "Skipped the short 'cb' command because it already belongs to ${existing_cb:-$cb_path}."
fi

custom_command_installed=0
if [ -n "$command_name" ]; then
  custom_path="$BIN_DIR/$command_name"
  if [ -e "$custom_path" ] || [ -L "$custom_path" ]; then rm -f "$custom_path"; fi
  ln -s "$INSTALL_DIR/contextbridge" "$custom_path"
  custom_command_installed=1
fi

completion_commands=""
if [ "$canonical_command_installed" = "1" ]; then completion_commands="contextbridge"; fi
if [ "$cb_alias_installed" = "1" ]; then completion_commands="${completion_commands:+$completion_commands }cb"; fi
if [ "$custom_command_installed" = "1" ]; then completion_commands="${completion_commands:+$completion_commands }$command_name"; fi
preferred_command="${command_name:-contextbridge}"
if [ -z "$command_name" ] && [ "$canonical_command_installed" != "1" ] && [ "$cb_alias_installed" = "1" ]; then preferred_command="cb"; fi
if [ -n "$completion_commands" ]; then
  echo "Commands ready: $completion_commands"
fi

install_shell_completion() {
  # bash-completion lazy-loads files from this per-user XDG directory without
  # a shell profile rewrite on standard installations.
  bash_completion_dir="${XDG_DATA_HOME:-$HOME/.local/share}/bash-completion/completions"
  mkdir -p "$bash_completion_dir" || return 1
  zsh_completion_dir="$HOME/.zfunc"
  # Ownership cleanup is independent from generating a new script. A command
  # no longer owned by ContextBridge must not retain our completion handler.
  for old_command in contextbridge cb; do
    case " $completion_commands " in *" $old_command "*) continue ;; esac
    if [ -f "$bash_completion_dir/$old_command" ] && grep -Fqx '# ContextBridge managed completion' "$bash_completion_dir/$old_command"; then
      rm -f "$bash_completion_dir/$old_command" || return 1
    fi
    if [ -f "$zsh_completion_dir/_$old_command" ] && grep -Fqx '# ContextBridge managed completion' "$zsh_completion_dir/_$old_command"; then
      rm -f "$zsh_completion_dir/_$old_command" || return 1
    fi
  done
  "$INSTALL_DIR/contextbridge" completion bash > "$tmp/contextbridge-completion.raw.bash" || return 1
  sed "s/^complete -o default -F _contextbridge_complete contextbridge cb$/complete -o default -F _contextbridge_complete $completion_commands/" \
    "$tmp/contextbridge-completion.raw.bash" > "$tmp/contextbridge-completion.body.bash" || return 1
  grep -Fqx "complete -o default -F _contextbridge_complete $completion_commands" "$tmp/contextbridge-completion.body.bash" || return 1
  {
    printf '# ContextBridge managed completion\n'
    cat "$tmp/contextbridge-completion.body.bash"
  } > "$tmp/contextbridge-completion.bash" || return 1
  for completion_command in $completion_commands; do
    install -m 0644 "$tmp/contextbridge-completion.bash" "$bash_completion_dir/$completion_command" || return 1
  done

  if command -v zsh >/dev/null 2>&1; then
    mkdir -p "$zsh_completion_dir" || return 1
    "$INSTALL_DIR/contextbridge" completion zsh > "$tmp/_contextbridge.raw" || return 1
    sed "s/^#compdef contextbridge cb$/#compdef $completion_commands/" "$tmp/_contextbridge.raw" > "$tmp/_contextbridge.body" || return 1
    grep -Fqx "#compdef $completion_commands" "$tmp/_contextbridge.body" || return 1
    {
      IFS= read -r first_line || return 1
      printf '%s\n# ContextBridge managed completion\n' "$first_line"
      cat
    } < "$tmp/_contextbridge.body" > "$tmp/_contextbridge" || return 1
    for completion_command in $completion_commands; do
      install -m 0644 "$tmp/_contextbridge" "$zsh_completion_dir/_$completion_command" || return 1
    done
    zsh_completion_commands="$completion_commands"
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
  "$INSTALL_DIR/contextbridge" init --config "$config"
fi

if { [ "$cluster_mode" = "worker" ] || [ "$cluster_mode" = "client" ]; } && [ -z "$relay_url" ] && [ -z "$lan_bundle" ] && [ "$interactive" = "1" ]; then
  printf 'Public HTTPS relay URL: ' >/dev/tty
  read -r relay_url </dev/tty
fi
if { [ "$cluster_mode" = "relay" ] || [ "$cluster_mode" = "all" ]; } && [ -z "$public_url" ] && [ "$interactive" = "1" ]; then
  printf 'Public HTTPS relay URL, or leave empty while configuring the reverse proxy: ' >/dev/tty
  read -r public_url </dev/tty || public_url=""
fi
if { [ "$cluster_mode" = "worker" ] || [ "$cluster_mode" = "client" ]; } && [ -z "$relay_url" ] && [ -z "$lan_bundle" ]; then
  echo "A relay URL is required for $cluster_mode mode. Workers may instead set CONTEXTBRIDGE_LAN_BUNDLE." >&2
  exit 1
fi
if [ "$cluster_mode" = "worker" ] && [ -n "$lan_bundle" ]; then
  "$INSTALL_DIR/contextbridge" cluster lan join --config "$config" --bundle "$lan_bundle" --name "$worker_name"
elif [ -n "$relay_url" ] && { [ "$cluster_mode" = "worker" ] || [ "$cluster_mode" = "all" ]; }; then
  "$INSTALL_DIR/contextbridge" cluster configure --config "$config" --mode "$cluster_mode" --relay-url "$relay_url" --name "$worker_name"
elif [ -n "$relay_url" ]; then
  "$INSTALL_DIR/contextbridge" cluster configure --config "$config" --mode "$cluster_mode" --relay-url "$relay_url"
elif [ -n "$public_url" ]; then
  "$INSTALL_DIR/contextbridge" cluster configure --config "$config" --mode "$cluster_mode" --listen auto --public-url "$public_url" --name "$worker_name"
elif [ "$cluster_mode" = "relay" ] || [ "$cluster_mode" = "all" ]; then
  "$INSTALL_DIR/contextbridge" cluster configure --config "$config" --mode "$cluster_mode" --listen auto --name "$worker_name"
else
  "$INSTALL_DIR/contextbridge" cluster configure --config "$config" --mode "$cluster_mode" --name "$worker_name"
fi
if { [ "$cluster_mode" = "worker" ] && [ -z "$lan_bundle" ]; } || [ "$cluster_mode" = "all" ]; then
  "$INSTALL_DIR/contextbridge" pair --config "$config" --name "$worker_name"
fi

if [ "$provider" = "managed" ]; then
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
  "$INSTALL_DIR/contextbridge" runtime install --config "$config" llama.cpp
  if [ "$managed_model" = "jina" ] || [ "$managed_model" = "both" ]; then
    sed -i.bak '/^  jina:/,/^  [A-Za-z0-9_-]*:/{s/^    auto_start: false$/    auto_start: true/;}' "$config"
    rm -f "$config.bak"
    "$INSTALL_DIR/contextbridge" pull --config "$config" jina-v4-retrieval
  fi
  if [ "$managed_model" = "nuextract" ] || [ "$managed_model" = "both" ]; then
    sed -i.bak '/^  nuextract:/,/^  [A-Za-z0-9_-]*:/{s/^    auto_start: false$/    auto_start: true/;}' "$config"
    rm -f "$config.bak"
    "$INSTALL_DIR/contextbridge" pull --config "$config" nuextract3
  fi
fi

if [ "$os" = "linux" ] && command -v systemctl >/dev/null 2>&1 && systemctl --user show-environment >/dev/null 2>&1; then
  unit_dir="$HOME/.config/systemd/user"
  mkdir -p "$unit_dir"
  cat > "$unit_dir/contextbridge.service" <<EOF
[Unit]
Description=ContextBridge local-first execution service
After=network-online.target

[Service]
ExecStart="$INSTALL_DIR/contextbridge" run --config "$config"
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
ExecStart="$INSTALL_DIR/contextbridge" update auto --config "$config"
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
    echo "Start ContextBridge with: $preferred_command run --config $config"
  fi
elif [ "$os" = "darwin" ]; then
  agent_dir="$HOME/Library/LaunchAgents"
  agent="$agent_dir/de.angusu.contextbridge.plist"
  update_agent="$agent_dir/de.angusu.contextbridge.update.plist"
  mkdir -p "$agent_dir"
	# plist string values are XML text, not shell syntax. Escape every dynamic
	# path so valid names containing &, <, >, quotes, spaces, or Unicode remain
	# valid launchd property lists.
	xml_escape() {
	  printf '%s' "$1" | sed -e 's/&/\&amp;/g' -e 's/</\&lt;/g' -e 's/>/\&gt;/g' -e 's/"/\&quot;/g' -e "s/'/\&apos;/g"
	}
	plist_binary="$(xml_escape "$INSTALL_DIR/contextbridge")"
	plist_config="$(xml_escape "$config")"
	plist_log="$(xml_escape "$INSTALL_DIR/contextbridge.log")"
	plist_error_log="$(xml_escape "$INSTALL_DIR/contextbridge-error.log")"
	plist_update_log="$(xml_escape "$INSTALL_DIR/contextbridge-update.log")"
	plist_update_error_log="$(xml_escape "$INSTALL_DIR/contextbridge-update-error.log")"
  cat > "$agent" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>de.angusu.contextbridge</string>
  <key>ProgramArguments</key><array><string>$plist_binary</string><string>run</string><string>--config</string><string>$plist_config</string></array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict>
  <key>StandardOutPath</key><string>$plist_log</string>
  <key>StandardErrorPath</key><string>$plist_error_log</string>
</dict></plist>
EOF
  cat > "$update_agent" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>de.angusu.contextbridge.update</string>
  <key>ProgramArguments</key><array><string>$plist_binary</string><string>update</string><string>auto</string><string>--config</string><string>$plist_config</string></array>
  <key>StartInterval</key><integer>86400</integer>
  <key>ProcessType</key><string>Background</string>
  <key>StandardOutPath</key><string>$plist_update_log</string>
  <key>StandardErrorPath</key><string>$plist_update_error_log</string>
</dict></plist>
EOF
  launchctl bootout "gui/$(id -u)" "$agent" >/dev/null 2>&1 || true
  launchctl bootout "gui/$(id -u)" "$update_agent" >/dev/null 2>&1 || true
  if launchctl bootstrap "gui/$(id -u)" "$agent" >/dev/null 2>&1; then
    launchctl bootstrap "gui/$(id -u)" "$update_agent" >/dev/null 2>&1 || true
    echo "ContextBridge launch agent enabled."
  else
    echo "Start ContextBridge with: $preferred_command run --config $config"
  fi
else
  echo "Start ContextBridge with: $preferred_command run --config $config"
fi

echo "Config: $config"
case ":$PATH:" in
  *":$BIN_DIR:"*) ;;
  *) echo "Add $BIN_DIR to PATH to run contextbridge without its full path." ;;
esac

if [ "${CONTEXTBRIDGE_NO_DASHBOARD:-0}" != "1" ]; then
  if [ "$os" = "darwin" ] || [ -n "${DISPLAY:-}${WAYLAND_DISPLAY:-}" ]; then
    sleep 1
    "$INSTALL_DIR/contextbridge" dashboard --config "$config" || true
  else
    echo "Dashboard: http://127.0.0.1:32145"
  fi
fi
