#!/bin/bash
set -euo pipefail

if [[ "$(uname -s)" != "Darwin" || "$(uname -m)" != "arm64" ]]; then
  echo "This proof requires a native macOS arm64 runner." >&2
  exit 1
fi

root="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
test_root="$(mktemp -d "${TMPDIR:-/tmp}/contextbridge-macos-native.XXXXXX")"
test_root="$(CDPATH= cd -- "$test_root" && pwd -P)"
service_pid=""
cleanup() {
  if [[ -n "$service_pid" ]]; then
    kill "$service_pid" >/dev/null 2>&1 || true
    wait "$service_pid" >/dev/null 2>&1 || true
  fi
  rm -rf "$test_root"
}
trap cleanup EXIT INT TERM

fail() {
  echo "Native macOS lifecycle proof failed: $*" >&2
  [[ -f "$test_root/install.out" ]] && { echo "--- install output ---" >&2; cat "$test_root/install.out" >&2; }
  [[ -f "$test_root/launchctl-calls" ]] && { echo "--- launchctl calls ---" >&2; cat "$test_root/launchctl-calls" >&2; }
  [[ -f "$test_root/service.err" ]] && { echo "--- service stderr ---" >&2; cat "$test_root/service.err" >&2; }
  exit 1
}

require_contains() {
  needle="$1"
  file_path="$2"
  label="$3"
  grep -F -- "$needle" "$file_path" >/dev/null || fail "$label"
}

fixture="$test_root/fixture"
fake_bin="$test_root/fake-bin"
home="$test_root/home"
install_dir="$home/Tools & AI/ContextBridge"
bin_dir="$home/.local/bin"
config="$home/.config/Context & Bridge/config.yml"
launchctl_calls="$test_root/launchctl-calls"
mkdir -p "$fixture" "$fake_bin" "$home"

go build -trimpath -o "$fixture/contextbridge" ./cmd/contextbridge
cp "$root/config.example.yml" "$fixture/config.example.yml"
tar -czf "$test_root/contextbridge_darwin_arm64.tar.gz" -C "$fixture" .
digest="$(shasum -a 256 "$test_root/contextbridge_darwin_arm64.tar.gz" | awk '{print $1}')"
printf '%s  %s\n' "$digest" contextbridge_darwin_arm64.tar.gz > "$test_root/SHA256SUMS"

cat > "$fake_bin/curl" <<'EOF'
#!/bin/bash
set -euo pipefail
output=""
url=""
while [[ "$#" -gt 0 ]]; do
  case "$1" in
    -o) shift; output="$1" ;;
    -*) ;;
    *) url="$1" ;;
  esac
  shift
done
if [[ -z "$output" ]]; then
  printf '{"assets":[{"browser_download_url":"https://fixture.invalid/contextbridge_darwin_arm64.tar.gz"},{"browser_download_url":"https://fixture.invalid/SHA256SUMS"}]}\n'
elif [[ "${url##*/}" == "SHA256SUMS" ]]; then
  cp "$CONTEXTBRIDGE_TEST_FIXTURE/SHA256SUMS" "$output"
else
  cp "$CONTEXTBRIDGE_TEST_FIXTURE/contextbridge_darwin_arm64.tar.gz" "$output"
fi
EOF
cat > "$fake_bin/launchctl" <<'EOF'
#!/bin/bash
printf '%s\n' "$*" >> "$CONTEXTBRIDGE_TEST_LAUNCHCTL_CALLS"
exit 0
EOF
chmod +x "$fake_bin/curl" "$fake_bin/launchctl"

export CONTEXTBRIDGE_TEST_FIXTURE="$test_root"
export CONTEXTBRIDGE_TEST_LAUNCHCTL_CALLS="$launchctl_calls"
export CONTEXTBRIDGE_HOME="$install_dir"
export CONTEXTBRIDGE_BIN_DIR="$bin_dir"
export CONTEXTBRIDGE_CONFIG="$config"
export CONTEXTBRIDGE_PROVIDER="later"
export CONTEXTBRIDGE_CLUSTER_MODE="local"
export CONTEXTBRIDGE_NO_DASHBOARD="1"
export CONTEXTBRIDGE_NONINTERACTIVE="1"

HOME="$home" PATH="$fake_bin:$PATH" sh "$root/install.sh" > "$test_root/install.out"

installed="$install_dir/contextbridge"
[[ -x "$installed" ]]
[[ -x "$bin_dir/contextbridge" ]]
[[ -L "$bin_dir/contextbridge" ]]
[[ "$(readlink "$bin_dir/contextbridge")" == "$installed" ]]
[[ -L "$bin_dir/cb" ]]
[[ "$(readlink "$bin_dir/cb")" == "$installed" ]]
[[ -f "$config" ]]
file "$installed" | grep -Eiq 'arm64|aarch64'
"$installed" version

agent="$home/Library/LaunchAgents/de.angusu.contextbridge.plist"
update_agent="$home/Library/LaunchAgents/de.angusu.contextbridge.update.plist"
plutil -lint "$agent" "$update_agent"
require_contains "bootstrap gui/$(id -u) $agent" "$launchctl_calls" "main LaunchAgent was not bootstrapped"
require_contains "bootstrap gui/$(id -u) $update_agent" "$launchctl_calls" "update LaunchAgent was not bootstrapped"
[[ "$(plutil -extract ProgramArguments.0 raw -o - "$agent")" == "$installed" ]] || fail "main LaunchAgent does not decode the installed binary path"
[[ "$(plutil -extract ProgramArguments.2 raw -o - "$agent")" == "$config" ]] || fail "main LaunchAgent does not decode the installed config path"

"$installed" serve --config "$config" > "$test_root/service.out" 2> "$test_root/service.err" &
service_pid="$!"
ready=0
for _ in $(seq 1 80); do
  if "$installed" health --config "$config" >/dev/null 2>&1; then
    ready=1
    break
  fi
  sleep 0.25
done
if [[ "$ready" != "1" ]]; then
  echo "Native macOS loopback service did not become healthy." >&2
  cat "$test_root/service.err" >&2 || true
  exit 1
fi
"$installed" status --config "$config" --json > "$test_root/status.json"
require_contains '"version"' "$test_root/status.json" "native status JSON has no version field"
kill "$service_pid"
wait "$service_pid" || true
service_pid=""

"$installed" uninstall --config "$config" --install-dir "$install_dir" --dry-run --yes > "$test_root/uninstall-plan.out"
require_contains "Config:       preserve $config" "$test_root/uninstall-plan.out" "uninstall dry-run did not preserve the expected config path"
"$installed" uninstall --config "$config" --install-dir "$install_dir" --yes > "$test_root/uninstall.out"

[[ -f "$config" ]]
[[ ! -e "$installed" ]]
[[ ! -e "$bin_dir/contextbridge" ]]
[[ ! -e "$bin_dir/cb" ]]
[[ ! -e "$agent" ]]
[[ ! -e "$update_agent" ]]
require_contains "bootout gui/$(id -u) $agent" "$launchctl_calls" "main LaunchAgent was not booted out"
require_contains "bootout gui/$(id -u) $update_agent" "$launchctl_calls" "update LaunchAgent was not booted out"

echo "Native macOS arm64 install, service, LaunchAgent, and uninstall lifecycle verified"
