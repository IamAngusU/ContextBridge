#!/bin/sh
set -eu

root="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
test_root="$(mktemp -d "${TMPDIR:-/tmp}/contextbridge-installer-test.XXXXXX")"
trap 'rm -rf "$test_root"' EXIT INT TERM

fake_bin="$test_root/fake-bin"
fixture="$test_root/fixture"
home="$test_root/home"
calls="$test_root/contextbridge-calls"
systemctl_calls="$test_root/systemctl-calls"
# Keep host-specific /usr/local commands (notably an already installed `cb`)
# out of the clean-install fixture while retaining standard POSIX utilities.
test_system_path="/usr/bin:/bin:/usr/sbin:/sbin"
mkdir -p "$fake_bin" "$fixture/extension/chromium" "$fixture/extension/firefox" "$home"

cat > "$fixture/contextbridge" <<'EOF'
#!/bin/sh
set -eu
printf '%s\n' "$*" >> "$CONTEXTBRIDGE_TEST_CALLS"
if [ "${1:-}" = "completion" ]; then
  if [ "${CONTEXTBRIDGE_TEST_FAIL_COMPLETION:-0}" = "1" ]; then
    exit 17
  fi
  printf '# fixture completion for %s\n' "${2:-unknown}"
  case "${2:-}" in
    bash) printf '_contextbridge_complete() { :; }\ncomplete -o default -F _contextbridge_complete contextbridge cb\n' ;;
    zsh) printf '#compdef contextbridge cb\n_contextbridge() { :; }\n' ;;
  esac
  exit 0
fi
if [ "${1:-}" = "init" ]; then
  shift
  while [ "$#" -gt 0 ]; do
    if [ "$1" = "--config" ]; then
      shift
      mkdir -p "$(dirname -- "$1")"
      printf 'generated: true\n' > "$1"
      break
    fi
    shift
  done
fi
EOF
chmod +x "$fixture/contextbridge"
printf 'fixture\n' > "$fixture/extension/chromium/manifest.json"
printf 'fixture\n' > "$fixture/extension/firefox/manifest.json"
tar -czf "$test_root/contextbridge_linux_amd64.tar.gz" -C "$fixture" .
# The installer must tolerate checksum manifests assembled by older Windows
# tooling, while current releases themselves are required to use LF.
printf 'fixture-hash  contextbridge_linux_amd64.tar.gz\r\n' > "$test_root/SHA256SUMS"

cat > "$fake_bin/uname" <<'EOF'
#!/bin/sh
case "${1:-}" in
  -s) printf 'Linux\n' ;;
  -m) printf 'x86_64\n' ;;
  *) printf 'Linux\n' ;;
esac
EOF
cat > "$fake_bin/curl" <<'EOF'
#!/bin/sh
set -eu
output=""
url=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    -o) shift; output="$1" ;;
    -*) ;;
    *) url="$1" ;;
  esac
  shift
done
if [ -z "$output" ]; then
  printf '{"assets":[{"browser_download_url":"https://fixture.invalid/contextbridge_linux_amd64.tar.gz"},{"browser_download_url":"https://fixture.invalid/SHA256SUMS"}]}\n'
elif [ "${url##*/}" = "SHA256SUMS" ]; then
  cp "$CONTEXTBRIDGE_TEST_FIXTURE/SHA256SUMS" "$output"
else
  cp "$CONTEXTBRIDGE_TEST_FIXTURE/contextbridge_linux_amd64.tar.gz" "$output"
fi
EOF
cat > "$fake_bin/sha256sum" <<'EOF'
#!/bin/sh
printf 'fixture-hash  %s\n' "$1"
EOF
cat > "$fake_bin/systemctl" <<'EOF'
#!/bin/sh
printf '%s\n' "$*" >> "$CONTEXTBRIDGE_TEST_SYSTEMCTL_CALLS"
exit 0
EOF
cat > "$fake_bin/zsh" <<'EOF'
#!/bin/sh
exit 0
EOF
chmod +x "$fake_bin/uname" "$fake_bin/curl" "$fake_bin/sha256sum" "$fake_bin/systemctl" "$fake_bin/zsh"

export CONTEXTBRIDGE_TEST_CALLS="$calls"
export CONTEXTBRIDGE_TEST_SYSTEMCTL_CALLS="$systemctl_calls"
export CONTEXTBRIDGE_TEST_FIXTURE="$test_root"
export CONTEXTBRIDGE_HOME="$home/share/contextbridge"
export CONTEXTBRIDGE_BIN_DIR="$home/bin"
export CONTEXTBRIDGE_CONFIG="$home/config/contextbridge/config.yml"
export CONTEXTBRIDGE_PROVIDER="later"
export CONTEXTBRIDGE_CLUSTER_MODE="worker"
export CONTEXTBRIDGE_RELAY_URL="https://relay.example.test/contextbridge"
export CONTEXTBRIDGE_WORKER_NAME="headless-worker"
export CONTEXTBRIDGE_NO_DASHBOARD="1"
export CONTEXTBRIDGE_NONINTERACTIVE="1"
HOME="$home" PATH="$fake_bin:$test_system_path" sh "$root/install.sh" > "$test_root/install.out"

test -x "$CONTEXTBRIDGE_BIN_DIR/contextbridge"
test -x "$CONTEXTBRIDGE_BIN_DIR/cb"
test "$(readlink "$CONTEXTBRIDGE_BIN_DIR/cb")" = "$CONTEXTBRIDGE_HOME/contextbridge"
"$CONTEXTBRIDGE_BIN_DIR/cb" version
grep -F -- 'version' "$calls" >/dev/null
grep -F -- '# fixture completion for bash' "$home/.local/share/bash-completion/completions/contextbridge" >/dev/null
grep -F -- '# fixture completion for bash' "$home/.local/share/bash-completion/completions/cb" >/dev/null
grep -F -- 'compdef _contextbridge contextbridge cb;' "$home/.zshrc" >/dev/null
test -f "$CONTEXTBRIDGE_CONFIG"
grep -F -- "init --config $CONTEXTBRIDGE_CONFIG" "$calls" >/dev/null
grep -F -- "cluster configure --config $CONTEXTBRIDGE_CONFIG --mode worker --relay-url https://relay.example.test/contextbridge --name headless-worker" "$calls" >/dev/null
grep -F -- "pair --config $CONTEXTBRIDGE_CONFIG --name headless-worker" "$calls" >/dev/null
grep -F -- '--user enable --now contextbridge.service contextbridge-update.timer' "$systemctl_calls" >/dev/null
grep -F -- "ExecStart=\"$CONTEXTBRIDGE_HOME/contextbridge\" run --config \"$CONTEXTBRIDGE_CONFIG\"" "$home/.config/systemd/user/contextbridge.service" >/dev/null

collision="$test_root/collision"
mkdir -p "$collision/home/.local/share/bash-completion/completions" "$collision/home/.zfunc" "$collision/bin"
printf '#!/bin/sh\nprintf user-owned-cb\n' > "$collision/bin/cb"
chmod +x "$collision/bin/cb"
printf '# user zsh setting\n' > "$collision/home/.zshrc"
# Simulate a prior ContextBridge install whose managed short alias was later
# replaced by another program. Its stale lazy-load files must not keep
# claiming completion ownership for the now foreign `cb` command.
printf '# ContextBridge managed completion\ncomplete -F _contextbridge_complete contextbridge cb\n' > "$collision/home/.local/share/bash-completion/completions/cb"
printf '#compdef contextbridge cb\n# ContextBridge managed completion\n' > "$collision/home/.zfunc/_cb"
chmod 0640 "$collision/home/.zshrc"
collision_mode_before="$(stat -c '%a' "$collision/home/.zshrc" 2>/dev/null || stat -f '%Lp' "$collision/home/.zshrc")"
HOME="$collision/home" \
  PATH="$collision/bin:$fake_bin:$test_system_path" \
  CONTEXTBRIDGE_HOME="$collision/share" \
  CONTEXTBRIDGE_BIN_DIR="$collision/bin" \
  CONTEXTBRIDGE_CONFIG="$collision/config.yml" \
  CONTEXTBRIDGE_PROVIDER="later" \
  CONTEXTBRIDGE_CLUSTER_MODE="local" \
  CONTEXTBRIDGE_NO_DASHBOARD="1" \
  CONTEXTBRIDGE_NONINTERACTIVE="1" \
  sh "$root/install.sh" > "$collision/out"
grep -F -- "Skipped the short 'cb' command" "$collision/out" >/dev/null
grep -F -- 'user-owned-cb' "$collision/bin/cb" >/dev/null
grep -F -- '# fixture completion for bash' "$collision/home/.local/share/bash-completion/completions/contextbridge" >/dev/null
test ! -e "$collision/home/.local/share/bash-completion/completions/cb"
test ! -e "$collision/home/.zfunc/_cb"
grep -Fqx -- 'complete -o default -F _contextbridge_complete contextbridge' "$collision/home/.local/share/bash-completion/completions/contextbridge"
if bash -c 'complete -W foreign cb; . "$1"; complete -p cb' sh "$collision/home/.local/share/bash-completion/completions/contextbridge" | grep -F -- '_contextbridge_complete' >/dev/null; then
  echo "foreign cb command received ContextBridge bash completion" >&2
  exit 1
fi
grep -Fqx -- '#compdef contextbridge' "$collision/home/.zfunc/_contextbridge"
if grep -Fqx -- '#compdef contextbridge cb' "$collision/home/.zfunc/_contextbridge"; then
  echo "foreign cb command received ContextBridge zsh autoload completion" >&2
  exit 1
fi
grep -F -- 'compdef _contextbridge contextbridge;' "$collision/home/.zshrc" >/dev/null
collision_mode_after="$(stat -c '%a' "$collision/home/.zshrc" 2>/dev/null || stat -f '%Lp' "$collision/home/.zshrc")"
test "$collision_mode_after" = "$collision_mode_before"
if grep -F -- 'compdef _contextbridge contextbridge cb;' "$collision/home/.zshrc" >/dev/null; then
  echo "foreign cb command received ContextBridge zsh completion" >&2
  exit 1
fi

# Unmarked completion files are user-owned even when they occupy paths that a
# previous ContextBridge alias used. A later installer run must leave them
# byte-for-byte intact.
printf '# user-owned bash completion\n' > "$collision/home/.local/share/bash-completion/completions/cb"
printf '#compdef cb\n# user-owned zsh completion\n' > "$collision/home/.zfunc/_cb"
collision_bash_completion_before="$(cksum < "$collision/home/.local/share/bash-completion/completions/cb")"
collision_zsh_completion_before="$(cksum < "$collision/home/.zfunc/_cb")"
HOME="$collision/home" \
  PATH="$collision/bin:$fake_bin:$test_system_path" \
  CONTEXTBRIDGE_HOME="$collision/share" \
  CONTEXTBRIDGE_BIN_DIR="$collision/bin" \
  CONTEXTBRIDGE_CONFIG="$collision/config.yml" \
  CONTEXTBRIDGE_PROVIDER="later" \
  CONTEXTBRIDGE_CLUSTER_MODE="local" \
  CONTEXTBRIDGE_NO_DASHBOARD="1" \
  CONTEXTBRIDGE_NONINTERACTIVE="1" \
  sh "$root/install.sh" > "$collision/out-second"
test "$(cksum < "$collision/home/.local/share/bash-completion/completions/cb")" = "$collision_bash_completion_before"
test "$(cksum < "$collision/home/.zfunc/_cb")" = "$collision_zsh_completion_before"

# If both public names belong to other programs, unattended installation must
# stop before downloading unless the operator supplies a safe custom name.
both_taken="$test_root/both-names-taken"
mkdir -p "$both_taken/home" "$both_taken/bin"
printf '#!/bin/sh\nprintf foreign-contextbridge\n' > "$both_taken/bin/contextbridge"
printf '#!/bin/sh\nprintf foreign-cb\n' > "$both_taken/bin/cb"
chmod +x "$both_taken/bin/contextbridge" "$both_taken/bin/cb"
contextbridge_before="$(cksum < "$both_taken/bin/contextbridge")"
cb_before="$(cksum < "$both_taken/bin/cb")"
if HOME="$both_taken/home" \
  PATH="$both_taken/bin:$fake_bin:$test_system_path" \
  CONTEXTBRIDGE_HOME="$both_taken/share" \
  CONTEXTBRIDGE_BIN_DIR="$both_taken/bin" \
  CONTEXTBRIDGE_CONFIG="$both_taken/config.yml" \
  CONTEXTBRIDGE_PROVIDER="later" \
  CONTEXTBRIDGE_CLUSTER_MODE="local" \
  CONTEXTBRIDGE_NO_DASHBOARD="1" \
  CONTEXTBRIDGE_NONINTERACTIVE="1" \
  sh "$root/install.sh" > "$both_taken/no-name.out" 2> "$both_taken/no-name.err"; then
  echo "installer accepted two foreign command names without a custom name" >&2
  exit 1
fi
grep -F -- 'Set CONTEXTBRIDGE_COMMAND to a custom command name.' "$both_taken/no-name.err" >/dev/null
test ! -e "$both_taken/share/contextbridge"

HOME="$both_taken/home" \
  PATH="$both_taken/bin:$fake_bin:$test_system_path" \
  CONTEXTBRIDGE_HOME="$both_taken/share" \
  CONTEXTBRIDGE_BIN_DIR="$both_taken/bin" \
  CONTEXTBRIDGE_CONFIG="$both_taken/config.yml" \
  CONTEXTBRIDGE_PROVIDER="later" \
  CONTEXTBRIDGE_CLUSTER_MODE="local" \
  CONTEXTBRIDGE_NO_DASHBOARD="1" \
  CONTEXTBRIDGE_NONINTERACTIVE="1" \
  CONTEXTBRIDGE_COMMAND="bridge-ai" \
  sh "$root/install.sh" > "$both_taken/custom.out"
test "$contextbridge_before" = "$(cksum < "$both_taken/bin/contextbridge")"
test "$cb_before" = "$(cksum < "$both_taken/bin/cb")"
test "$(readlink "$both_taken/bin/bridge-ai")" = "$both_taken/share/contextbridge"
"$both_taken/bin/bridge-ai" version
grep -Fqx -- 'complete -o default -F _contextbridge_complete bridge-ai' "$both_taken/home/.local/share/bash-completion/completions/bridge-ai"
test ! -e "$both_taken/home/.local/share/bash-completion/completions/contextbridge"
test ! -e "$both_taken/home/.local/share/bash-completion/completions/cb"
grep -Fqx -- '#compdef bridge-ai' "$both_taken/home/.zfunc/_bridge-ai"
grep -F -- 'compdef _contextbridge bridge-ai;' "$both_taken/home/.zshrc" >/dev/null

shadowed="$test_root/shadowed-managed-alias"
mkdir -p "$shadowed/home" "$shadowed/bin" "$shadowed/foreign"
printf '#!/bin/sh\n# ContextBridge managed cb alias\nexec "$(dirname -- "$0")/contextbridge" "$@"\n' > "$shadowed/bin/cb"
printf '#!/bin/sh\nprintf foreign-cb\n' > "$shadowed/foreign/cb"
chmod +x "$shadowed/bin/cb" "$shadowed/foreign/cb"
HOME="$shadowed/home" \
  PATH="$shadowed/foreign:$shadowed/bin:$fake_bin:$test_system_path" \
  CONTEXTBRIDGE_HOME="$shadowed/share" \
  CONTEXTBRIDGE_BIN_DIR="$shadowed/bin" \
  CONTEXTBRIDGE_CONFIG="$shadowed/config.yml" \
  CONTEXTBRIDGE_PROVIDER="later" \
  CONTEXTBRIDGE_CLUSTER_MODE="local" \
  CONTEXTBRIDGE_NO_DASHBOARD="1" \
  CONTEXTBRIDGE_NONINTERACTIVE="1" \
  sh "$root/install.sh" > "$shadowed/out"
grep -F -- 'active cb command belongs to' "$shadowed/out" >/dev/null
grep -Fqx -- 'complete -o default -F _contextbridge_complete contextbridge' "$shadowed/home/.local/share/bash-completion/completions/contextbridge"
test ! -e "$shadowed/home/.local/share/bash-completion/completions/cb"
grep -Fqx -- '#compdef contextbridge' "$shadowed/home/.zfunc/_contextbridge"
test ! -e "$shadowed/home/.zfunc/_cb"

symlink_case="$test_root/zshrc-symlink"
mkdir -p "$symlink_case/home/dotfiles"
printf '# linked user zsh setting\n' > "$symlink_case/home/dotfiles/zshrc"
if ln -s dotfiles/zshrc "$symlink_case/home/.zshrc" 2>/dev/null && test -L "$symlink_case/home/.zshrc"; then
  HOME="$symlink_case/home" \
    PATH="$fake_bin:$test_system_path" \
    CONTEXTBRIDGE_HOME="$symlink_case/share" \
    CONTEXTBRIDGE_BIN_DIR="$symlink_case/bin" \
    CONTEXTBRIDGE_CONFIG="$symlink_case/config.yml" \
    CONTEXTBRIDGE_PROVIDER="later" \
    CONTEXTBRIDGE_CLUSTER_MODE="local" \
    CONTEXTBRIDGE_NO_DASHBOARD="1" \
    CONTEXTBRIDGE_NONINTERACTIVE="1" \
    sh "$root/install.sh" > "$symlink_case/out"
  test -L "$symlink_case/home/.zshrc"
  test "$(readlink "$symlink_case/home/.zshrc")" = 'dotfiles/zshrc'
  grep -F -- '# linked user zsh setting' "$symlink_case/home/dotfiles/zshrc" >/dev/null
  grep -F -- 'compdef _contextbridge contextbridge cb;' "$symlink_case/home/dotfiles/zshrc" >/dev/null
fi

for malformed_kind in reversed duplicate unclosed; do
  malformed="$test_root/zshrc-malformed-$malformed_kind"
  mkdir -p "$malformed/home"
  case "$malformed_kind" in
    reversed) printf '# <<< ContextBridge completion <<<\nuser-before=true\n# >>> ContextBridge completion >>>\nuser-after=true\n' > "$malformed/home/.zshrc" ;;
    duplicate) printf '# >>> ContextBridge completion >>>\nmanaged=true\n# <<< ContextBridge completion <<<\nuser-middle=true\n# >>> ContextBridge completion >>>\nuser-after=true\n' > "$malformed/home/.zshrc" ;;
    unclosed) printf 'user-before=true\n# >>> ContextBridge completion >>>\nuser-after=true\n' > "$malformed/home/.zshrc" ;;
  esac
  malformed_checksum="$(cksum < "$malformed/home/.zshrc")"
  HOME="$malformed/home" \
    PATH="$fake_bin:$test_system_path" \
    CONTEXTBRIDGE_HOME="$malformed/share" \
    CONTEXTBRIDGE_BIN_DIR="$malformed/bin" \
    CONTEXTBRIDGE_CONFIG="$malformed/config.yml" \
    CONTEXTBRIDGE_PROVIDER="later" \
    CONTEXTBRIDGE_CLUSTER_MODE="local" \
    CONTEXTBRIDGE_NO_DASHBOARD="1" \
    CONTEXTBRIDGE_NONINTERACTIVE="1" \
    sh "$root/install.sh" > "$malformed/out" 2> "$malformed/err"
  test "$(cksum < "$malformed/home/.zshrc")" = "$malformed_checksum"
  test -x "$malformed/bin/contextbridge"
  test -f "$malformed/config.yml"
  grep -F -- 'Shell completion could not be installed; ContextBridge itself is ready.' "$malformed/err" >/dev/null
done

completion_failure="$test_root/completion-failure"
mkdir -p "$completion_failure/home/.local/share/bash-completion/completions" "$completion_failure/home/.zfunc" "$completion_failure/bin"
printf '#!/bin/sh\nprintf foreign-cb\n' > "$completion_failure/bin/cb"
chmod +x "$completion_failure/bin/cb"
printf '# ContextBridge managed completion\ncomplete -F _contextbridge_complete contextbridge cb\n' > "$completion_failure/home/.local/share/bash-completion/completions/cb"
printf '#compdef contextbridge cb\n# ContextBridge managed completion\n' > "$completion_failure/home/.zfunc/_cb"
HOME="$completion_failure/home" \
  PATH="$completion_failure/bin:$fake_bin:$test_system_path" \
  CONTEXTBRIDGE_HOME="$completion_failure/share" \
  CONTEXTBRIDGE_BIN_DIR="$completion_failure/bin" \
  CONTEXTBRIDGE_CONFIG="$completion_failure/config.yml" \
  CONTEXTBRIDGE_PROVIDER="later" \
  CONTEXTBRIDGE_CLUSTER_MODE="local" \
  CONTEXTBRIDGE_NO_DASHBOARD="1" \
  CONTEXTBRIDGE_NONINTERACTIVE="1" \
  CONTEXTBRIDGE_TEST_FAIL_COMPLETION="1" \
  sh "$root/install.sh" > "$completion_failure/out" 2> "$completion_failure/err"
test -x "$completion_failure/bin/contextbridge"
test -f "$completion_failure/config.yml"
test ! -e "$completion_failure/home/.local/share/bash-completion/completions/cb"
test ! -e "$completion_failure/home/.zfunc/_cb"
grep -F -- 'Shell completion could not be installed; ContextBridge itself is ready.' "$completion_failure/err" >/dev/null

missing_relay="$test_root/missing-relay"
mkdir -p "$missing_relay/home"
if HOME="$missing_relay/home" \
  PATH="$fake_bin:$test_system_path" \
  CONTEXTBRIDGE_HOME="$missing_relay/share" \
  CONTEXTBRIDGE_BIN_DIR="$missing_relay/bin" \
  CONTEXTBRIDGE_CONFIG="$missing_relay/config.yml" \
  CONTEXTBRIDGE_PROVIDER="later" \
  CONTEXTBRIDGE_CLUSTER_MODE="worker" \
  CONTEXTBRIDGE_NO_DASHBOARD="1" \
  CONTEXTBRIDGE_NONINTERACTIVE="1" \
  CONTEXTBRIDGE_RELAY_URL="" \
  sh "$root/install.sh" </dev/null > "$missing_relay/out" 2> "$missing_relay/err"; then
  echo "headless worker install unexpectedly accepted an empty relay URL" >&2
  exit 1
fi
grep -F -- 'Set CONTEXTBRIDGE_RELAY_URL for an unattended install.' "$missing_relay/err" >/dev/null
test ! -e "$missing_relay/bin/contextbridge"

unsafe_relay="$test_root/unsafe-relay"
mkdir -p "$unsafe_relay/home"
if HOME="$unsafe_relay/home" \
  PATH="$fake_bin:$test_system_path" \
  CONTEXTBRIDGE_HOME="$unsafe_relay/share" \
  CONTEXTBRIDGE_BIN_DIR="$unsafe_relay/bin" \
  CONTEXTBRIDGE_CONFIG="$unsafe_relay/config.yml" \
  CONTEXTBRIDGE_PROVIDER="later" \
  CONTEXTBRIDGE_CLUSTER_MODE="worker" \
  CONTEXTBRIDGE_NO_DASHBOARD="1" \
  CONTEXTBRIDGE_NONINTERACTIVE="1" \
  CONTEXTBRIDGE_RELAY_URL="http://relay.example.test" \
  sh "$root/install.sh" </dev/null > "$unsafe_relay/out" 2> "$unsafe_relay/err"; then
  echo "headless worker install unexpectedly accepted a non-TLS remote relay" >&2
  exit 1
fi
grep -F -- 'CONTEXTBRIDGE_RELAY_URL must use HTTPS or a localhost URL.' "$unsafe_relay/err" >/dev/null
test ! -e "$unsafe_relay/bin/contextbridge"

echo "Headless worker installer verified"
