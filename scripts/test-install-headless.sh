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
mkdir -p "$fake_bin" "$fixture/extension/chromium" "$fixture/extension/firefox" "$home"

cat > "$fixture/contextbridge" <<'EOF'
#!/bin/sh
set -eu
printf '%s\n' "$*" >> "$CONTEXTBRIDGE_TEST_CALLS"
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
printf 'fixture-hash  contextbridge_linux_amd64.tar.gz\n' > "$test_root/SHA256SUMS"

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
chmod +x "$fake_bin/uname" "$fake_bin/curl" "$fake_bin/sha256sum" "$fake_bin/systemctl"

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
HOME="$home" PATH="$fake_bin:$PATH" sh "$root/install.sh" > "$test_root/install.out"

test -x "$CONTEXTBRIDGE_BIN_DIR/contextbridge"
test -f "$CONTEXTBRIDGE_CONFIG"
grep -F -- "init --config $CONTEXTBRIDGE_CONFIG" "$calls" >/dev/null
grep -F -- "cluster configure --config $CONTEXTBRIDGE_CONFIG --mode worker --relay-url https://relay.example.test/contextbridge --name headless-worker" "$calls" >/dev/null
grep -F -- "pair --config $CONTEXTBRIDGE_CONFIG --name headless-worker" "$calls" >/dev/null
grep -F -- '--user enable --now contextbridge.service contextbridge-update.timer' "$systemctl_calls" >/dev/null
grep -F -- "ExecStart=\"$CONTEXTBRIDGE_BIN_DIR/contextbridge\" run --config \"$CONTEXTBRIDGE_CONFIG\"" "$home/.config/systemd/user/contextbridge.service" >/dev/null

missing_relay="$test_root/missing-relay"
mkdir -p "$missing_relay/home"
if HOME="$missing_relay/home" \
  PATH="$fake_bin:$PATH" \
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
  PATH="$fake_bin:$PATH" \
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
