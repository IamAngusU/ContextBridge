#!/bin/sh
set -eu

root="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
work="$(mktemp -d)"
service_pid=""
adapter_pid=""

cleanup() {
    if [ -n "$adapter_pid" ]; then kill "$adapter_pid" 2>/dev/null || true; fi
    if [ -n "$service_pid" ]; then kill "$service_pid" 2>/dev/null || true; fi
    case "$work" in
        /tmp/*|/var/tmp/*) rm -rf -- "$work" ;;
        *) printf '%s\n' "Refusing to remove unexpected temporary path: $work" >&2 ;;
    esac
}
trap cleanup EXIT INT TERM

port="$(python3 - <<'PY'
import socket
s = socket.socket()
s.bind(("127.0.0.1", 0))
print(s.getsockname()[1])
s.close()
PY
)"
operator_token="operator-reference-token-0123456789abcdef"
adapter_token="adapter-reference-token-0123456789abcdef"

go build -trimpath -o "$work/contextbridge" "$root/cmd/contextbridge"

"$work/contextbridge" adapter conformance \
    --adapter "$(command -v node)" \
    --arg "$root/examples/adapter-v2/reference-adapter.mjs" \
    --profile reference \
    --working-directory "$root" \
    --timeout-seconds 10 \
    --json >"$work/conformance.json"

python3 - "$work/conformance.json" <<'PY'
import json, sys
with open(sys.argv[1], encoding="utf-8") as f:
    report = json.load(f)
assert report["schema"] == "contextbridge.adapter-conformance.v1", report
assert report["protocol"] == "contextbridge.adapter.v2", report
assert report["passed"] is True, report
expected = {
    "unauthorized_is_terminal",
    "http_failure_not_retried",
    "expired_lease_is_terminal",
    "scoped_lifecycle",
    "endpoint_capability_renewal",
    "claim_before_completion",
    "ambiguous_completion_not_retried",
}
assert expected <= {item["id"] for item in report["checks"]}, report
assert all(item["passed"] for item in report["checks"]), report
PY

cat >"$work/config.yml" <<EOF
version: 1
server:
  listen: 127.0.0.1:$port
  token: $operator_token
storage:
  directory: $work/data
  inbox: $work/inbox
  models: $work/models
routes:
  default:
    provider: adapter
    adapter_profile: reference
    task: generation
    timeout_seconds: 30
providers:
  adapter:
    auth_mode: scoped
    lease_seconds: 30
    principals:
      reference:
        token: $adapter_token
        allowed_profiles: [reference]
adapter_profiles:
  reference:
    label: Reference adapter
    driver: reference
EOF

cat >"$work/job.json" <<'EOF'
{"route":"default","prompt":"Return the reference proof string.","output":{"mode":"text","max_bytes":1024}}
EOF

"$work/contextbridge" serve --config "$work/config.yml" >"$work/service.log" 2>&1 &
service_pid=$!

ready=false
for _ in $(seq 1 100); do
    if curl -fsS "http://127.0.0.1:$port/health" >/dev/null 2>&1; then
        ready=true
        break
    fi
    sleep 0.05
done
if [ "$ready" != true ]; then
    printf '%s\n' "ContextBridge reference service did not become healthy" >&2
    cat "$work/service.log" >&2
    exit 1
fi

CONTEXTBRIDGE_URL="http://127.0.0.1:$port" \
CONTEXTBRIDGE_ADAPTER_TOKEN="$adapter_token" \
CONTEXTBRIDGE_ADAPTER_PROFILE=reference \
node "$root/examples/adapter-v2/reference-adapter.mjs" >"$work/adapter.json" 2>"$work/adapter.log" &
adapter_pid=$!

"$work/contextbridge" submit --config "$work/config.yml" --file "$work/job.json" >"$work/result.json"
wait "$adapter_pid"
adapter_pid=""

python3 - "$work/result.json" "$work/adapter.json" <<'PY'
import json, sys
with open(sys.argv[1], encoding="utf-8") as f:
    result = json.load(f)
with open(sys.argv[2], encoding="utf-8") as f:
    adapter = json.load(f)
assert result["mode"] == "text", result
assert result["text"] == "REFERENCE-ADAPTER-OK", result
assert adapter["result"]["text"] == "REFERENCE-ADAPTER-OK", adapter
assert adapter["job_id"], adapter
PY

printf '%s\n' "ContextBridge adapter v2 reference E2E and conformance passed."
