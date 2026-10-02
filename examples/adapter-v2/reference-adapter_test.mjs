// SPDX-License-Identifier: Apache-2.0

import assert from "node:assert/strict";
import { createServer } from "node:http";
import test from "node:test";
import { ContextBridgeAdapterV2, runReferenceAdapterOnce } from "./reference-adapter.mjs";

const token = "adapter-reference-token-0123456789abcdef";
const endpointCapability = "endpoint-capability-0123456789abcdef";
const leaseCapability = "lease-capability-0123456789abcdef";

async function withCore(handler, run) {
  const server = createServer(handler);
  await new Promise((resolve, reject) => server.listen(0, "127.0.0.1", (error) => error ? reject(error) : resolve()));
  try {
    const address = server.address();
    await run(`http://127.0.0.1:${address.port}`);
  } finally {
    await new Promise((resolve, reject) => server.close((error) => error ? reject(error) : resolve()));
  }
}

function json(response, status, body) {
  const raw = Buffer.from(JSON.stringify(body));
  response.writeHead(status, { "Content-Type": "application/json", "Content-Length": raw.length });
  response.end(raw);
}

test("reference adapter completes the scoped v2 lifecycle without privileged APIs", async () => {
  const seen = [];
  await withCore(async (request, response) => {
    const chunks = [];
    for await (const chunk of request) chunks.push(chunk);
    const body = chunks.length ? JSON.parse(Buffer.concat(chunks).toString("utf8")) : null;
    const path = new URL(request.url, "http://127.0.0.1").pathname;
    assert.equal(request.headers.authorization, `Bearer ${token}`);
    seen.push({ method: request.method, path, body, headers: request.headers });
    if (path === "/v2/adapter/status") return json(response, 200, { ok: true, protocol: "contextbridge.adapter.v2", principal_id: "reference", allowed_profiles: ["reference"] });
    if (path === "/v2/adapter/profiles") return json(response, 200, { reference: { label: "Reference", driver: "reference" } });
    if (path === "/v2/adapter/heartbeat") return json(response, 200, { ok: true, protocol: "contextbridge.adapter.v2", endpoints: [{ profile: "reference", endpoint_id: 1, endpoint_capability: endpointCapability }] });
    if (path === "/v2/adapter/jobs/next") {
      assert.equal(request.headers["x-contextbridge-endpoint-capability"], endpointCapability);
      return json(response, 200, { job: { id: "job-reference", prompt: "test" }, lease_generation: 4, lease_capability: leaseCapability });
    }
    assert.equal(request.headers["x-contextbridge-lease-generation"], "4");
    assert.equal(request.headers["x-contextbridge-lease-capability"], leaseCapability);
    if (path.endsWith("/lease")) return json(response, 200, { ok: true, lease_expires_at: new Date(Date.now() + 30_000).toISOString() });
    if (path.endsWith("/claim")) return json(response, 200, { ok: true, observation_only: true });
    if (path.endsWith("/progress")) return json(response, 200, { ok: true });
    if (path.endsWith("/complete")) return json(response, 200, body);
    return json(response, 404, { error: "not found" });
  }, async (baseURL) => {
    const completed = await runReferenceAdapterOnce({ baseURL, token, profile: "reference", endpointID: 1, waitMS: 1_000 });
    assert.equal(completed.job_id, "job-reference");
    assert.equal(completed.result.text, "REFERENCE-ADAPTER-OK");
  });
  assert.deepEqual(seen.filter((entry) => entry.path.includes("/jobs/job-reference")).map((entry) => entry.path), [
    "/v2/adapter/jobs/job-reference/lease",
    "/v2/adapter/jobs/job-reference/claim",
    "/v2/adapter/jobs/job-reference/progress",
    "/v2/adapter/jobs/job-reference/lease",
    "/v2/adapter/jobs/job-reference/complete",
  ]);
});

test("reference adapter observes cancellation before completion", async () => {
  let leaseChecks = 0;
  let completions = 0;
  await withCore(async (request, response) => {
    for await (const _chunk of request) { /* drain the bounded test request */ }
    const path = new URL(request.url, "http://127.0.0.1").pathname;
    if (path === "/v2/adapter/status") return json(response, 200, { ok: true, protocol: "contextbridge.adapter.v2", principal_id: "reference", allowed_profiles: ["reference"] });
    if (path === "/v2/adapter/profiles") return json(response, 200, { reference: { label: "Reference", driver: "reference" } });
    if (path === "/v2/adapter/heartbeat") return json(response, 200, { ok: true, protocol: "contextbridge.adapter.v2", endpoints: [{ profile: "reference", endpoint_id: 1, endpoint_capability: endpointCapability }] });
    if (path === "/v2/adapter/jobs/next") return json(response, 200, { job: { id: "job-cancelled", prompt: "test" }, lease_generation: 9, lease_capability: leaseCapability });
    if (path.endsWith("/lease")) {
      leaseChecks += 1;
      if (leaseChecks === 2) return json(response, 409, { error: "job was cancelled" });
      return json(response, 200, { ok: true, lease_expires_at: new Date(Date.now() + 30_000).toISOString() });
    }
    if (path.endsWith("/claim")) return json(response, 200, { ok: true, observation_only: true });
    if (path.endsWith("/progress")) return json(response, 200, { ok: true });
    if (path.endsWith("/complete")) {
      completions += 1;
      return json(response, 200, { mode: "text", text: "unexpected" });
    }
    return json(response, 404, { error: "not found" });
  }, async (baseURL) => {
    await assert.rejects(
      runReferenceAdapterOnce({ baseURL, token, profile: "reference", endpointID: 1, waitMS: 1_000 }),
      /HTTP 409.*cancelled/u,
    );
  });
  assert.equal(leaseChecks, 2);
  assert.equal(completions, 0);
});

test("HTTP failures are not retried and unsafe credential destinations are rejected", async () => {
  let calls = 0;
  await withCore((_request, response) => {
    calls += 1;
    json(response, 503, { error: "temporary" });
  }, async (baseURL) => {
    const client = new ContextBridgeAdapterV2({ baseURL, token, profile: "reference" });
    await assert.rejects(client.status(), /HTTP 503/u);
  });
  assert.equal(calls, 1);
  assert.throws(() => new ContextBridgeAdapterV2({ baseURL: "http://example.com", token, profile: "reference" }), /HTTPS or loopback HTTP/u);
  assert.throws(() => new ContextBridgeAdapterV2({ baseURL: "http://127.0.0.1:32145?token=bad", token, profile: "reference" }), /without credentials, path, query, or fragment/u);
});
