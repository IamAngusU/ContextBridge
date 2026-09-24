# SPDX-License-Identifier: Apache-2.0
"""Minimal server-side ContextBridge producer using only Python's stdlib."""

import json
import os
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid

MAX_RESPONSE_BYTES = 1 << 20
POLL_SECONDS = 0.5
TIMEOUT_SECONDS = 60


def required_env(name: str) -> str:
    value = os.environ.get(name, "").strip()
    if not value:
        raise RuntimeError(f"{name} is required")
    return value


def validate_relay_url(value: str) -> str:
    parsed = urllib.parse.urlparse(value)
    if parsed.scheme == "https" and parsed.hostname:
        return value.rstrip("/")
    if parsed.scheme == "http" and parsed.hostname in {"127.0.0.1", "localhost", "::1"}:
        return value.rstrip("/")
    raise RuntimeError("relay URL must use HTTPS unless it is loopback")


def request_json(method: str, url: str, token: str, body=None, headers=None):
    raw = None if body is None else json.dumps(body, separators=(",", ":")).encode()
    request = urllib.request.Request(url, data=raw, method=method)
    request.add_header("Authorization", f"Bearer {token}")
    if raw is not None:
        request.add_header("Content-Type", "application/json")
    for name, value in (headers or {}).items():
        request.add_header(name, value)
    try:
        with urllib.request.urlopen(request, timeout=10) as response:
            data = response.read(MAX_RESPONSE_BYTES + 1)
    except urllib.error.HTTPError as error:
        detail = error.read(4096).decode("utf-8", "replace")
        raise RuntimeError(f"relay returned HTTP {error.code}: {detail}") from error
    if len(data) > MAX_RESPONSE_BYTES:
        raise RuntimeError("relay response exceeded 1 MiB")
    return json.loads(data)


def main() -> int:
    relay = validate_relay_url(required_env("CONTEXTBRIDGE_RELAY_URL"))
    token = required_env("CONTEXTBRIDGE_PRODUCER_TOKEN")
    operation_id = os.environ.get("CONTEXTBRIDGE_OPERATION_ID", "demo-" + uuid.uuid4().hex)
    job = {
        "contract_version": "contextbridge.job.v1",
        "source": "python-stdlib-example",
        "requirements": {"task": "generation", "provider": "ollama"},
        "payload": {
            "provider": "ollama",
            "prompt": "Reply exactly with PYTHON-CB-OK and nothing else.",
            "output": {"mode": "text", "max_bytes": 4096},
        },
        "max_attempts": 1,
    }
    accepted = request_json(
        "POST",
        relay + "/v1/cluster/jobs?compact=1",
        token,
        job,
        {"Idempotency-Key": operation_id},
    )
    job_id = accepted.get("id")
    if not job_id:
        raise RuntimeError("relay accepted no job ID")
    deadline = time.monotonic() + TIMEOUT_SECONDS
    while time.monotonic() < deadline:
        current = request_json("GET", relay + "/v1/cluster/jobs/" + urllib.parse.quote(job_id) + "?compact=1", token)
        status = current.get("status")
        if status == "completed":
            print(current.get("result", {}).get("output", {}).get("text", ""))
            return 0
        if status in {"failed", "cancelled"}:
            raise RuntimeError(f"job ended as {status}: {current.get('error', '')}")
        time.sleep(POLL_SECONDS)
    raise RuntimeError(f"job {job_id} did not finish within {TIMEOUT_SECONDS}s")


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except Exception as error:  # bounded CLI example: one concise stderr line
        print(f"ContextBridge example failed: {error}", file=sys.stderr)
        raise SystemExit(1)
