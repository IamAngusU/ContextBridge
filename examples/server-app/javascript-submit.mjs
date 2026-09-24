// SPDX-License-Identifier: Apache-2.0
// Minimal server-side ContextBridge producer for Node.js 18+.

import crypto from "node:crypto";

const relay = requiredEnv("CONTEXTBRIDGE_RELAY_URL").replace(/\/$/, "");
const token = requiredEnv("CONTEXTBRIDGE_PRODUCER_TOKEN");
const operationId = process.env.CONTEXTBRIDGE_OPERATION_ID || `demo-${crypto.randomUUID()}`;
validateRelayURL(relay);

const job = {
  contract_version: "contextbridge.job.v1",
  source: "node-stdlib-example",
  requirements: { task: "generation", provider: "ollama" },
  payload: {
    provider: "ollama",
    prompt: "Reply exactly with NODE-CB-OK and nothing else.",
    output: { mode: "text", max_bytes: 4096 },
  },
  max_attempts: 1,
};

try {
  const accepted = await requestJSON("POST", `${relay}/v1/cluster/jobs?compact=1`, job, {
    "Idempotency-Key": operationId,
  });
  if (!accepted.id) throw new Error("relay accepted no job ID");
  const deadline = Date.now() + 60_000;
  while (Date.now() < deadline) {
    const current = await requestJSON("GET", `${relay}/v1/cluster/jobs/${encodeURIComponent(accepted.id)}?compact=1`);
    if (current.status === "completed") {
      console.log(current.result?.output?.text ?? "");
      process.exit(0);
    }
    if (["failed", "cancelled"].includes(current.status)) {
      throw new Error(`job ended as ${current.status}: ${current.error ?? ""}`);
    }
    await new Promise((resolve) => setTimeout(resolve, 500));
  }
  throw new Error(`job ${accepted.id} did not finish within 60s`);
} catch (error) {
  console.error(`ContextBridge example failed: ${error.message}`);
  process.exit(1);
}

function requiredEnv(name) {
  const value = (process.env[name] || "").trim();
  if (!value) throw new Error(`${name} is required`);
  return value;
}

function validateRelayURL(value) {
  const parsed = new URL(value);
  const loopback = ["127.0.0.1", "localhost", "[::1]"].includes(parsed.hostname);
  if (parsed.protocol !== "https:" && !(parsed.protocol === "http:" && loopback)) {
    throw new Error("relay URL must use HTTPS unless it is loopback");
  }
}

async function requestJSON(method, url, body, extraHeaders = {}) {
  const response = await fetch(url, {
    method,
    headers: {
      Authorization: `Bearer ${token}`,
      ...(body === undefined ? {} : { "Content-Type": "application/json" }),
      ...extraHeaders,
    },
    body: body === undefined ? undefined : JSON.stringify(body),
    signal: AbortSignal.timeout(10_000),
  });
  const raw = await response.text();
  if (raw.length > 1 << 20) throw new Error("relay response exceeded 1 MiB");
  if (!response.ok) throw new Error(`relay returned HTTP ${response.status}: ${raw.slice(0, 4096)}`);
  return JSON.parse(raw);
}
