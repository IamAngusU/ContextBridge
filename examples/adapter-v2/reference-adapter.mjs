// SPDX-License-Identifier: Apache-2.0

import { pathToFileURL } from "node:url";

const MAX_RESPONSE_BYTES = 20 * 1024 * 1024;
const SAFE_ID = /^[A-Za-z0-9][A-Za-z0-9._-]{0,79}$/;
const SAFE_OPAQUE = /^[\x21-\x7e]{32,256}$/u;

async function readBounded(response) {
  if (!response.body) return new Uint8Array();
  const reader = response.body.getReader();
  const chunks = [];
  let total = 0;
  try {
    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;
      total += value.byteLength;
      if (total > MAX_RESPONSE_BYTES) {
        await reader.cancel("bounded adapter response limit exceeded");
        throw new Error("adapter response exceeds 20 MiB");
      }
      chunks.push(value);
    }
  } finally {
    reader.releaseLock();
  }
  const result = new Uint8Array(total);
  let offset = 0;
  for (const chunk of chunks) {
    result.set(chunk, offset);
    offset += chunk.byteLength;
  }
  return result;
}

export class ContextBridgeAdapterV2 {
  constructor({ baseURL, token, profile, endpointID = 1, fetchImpl = globalThis.fetch, timeoutMS = 30_000 }) {
    const parsed = new URL(baseURL);
    const loopback = parsed.hostname === "localhost" || parsed.hostname === "127.0.0.1" || parsed.hostname === "[::1]" || parsed.hostname === "::1";
    if ((parsed.protocol !== "https:" && !(parsed.protocol === "http:" && loopback)) || parsed.username || parsed.password || (parsed.pathname !== "/" && parsed.pathname !== "") || parsed.search || parsed.hash) {
      throw new Error("adapter base URL must be an HTTPS or loopback HTTP origin without credentials, path, query, or fragment");
    }
    if (typeof token !== "string" || token.trim().length < 32 || /[\r\n\0]/u.test(token)) {
      throw new Error("adapter token must be one non-empty secret of at least 32 characters");
    }
    if (!SAFE_ID.test(profile)) throw new Error("profile must be a safe ContextBridge identifier");
    if (!Number.isSafeInteger(endpointID) || endpointID < 1) throw new Error("endpointID must be a positive safe integer");
    if (typeof fetchImpl !== "function") throw new Error("fetch implementation is required");
    if (!Number.isSafeInteger(timeoutMS) || timeoutMS < 100 || timeoutMS > 300_000) throw new Error("timeoutMS must be between 100 and 300000");
    parsed.pathname = parsed.pathname.replace(/\/+$/u, "");
    this.baseURL = parsed.toString().replace(/\/$/u, "");
    this.token = token.trim();
    this.profile = profile;
    this.endpointID = endpointID;
    this.fetchImpl = fetchImpl;
    this.timeoutMS = timeoutMS;
  }

  async request(path, { method = "GET", body, headers = {} } = {}) {
    const response = await this.fetchImpl(new URL(path, `${this.baseURL}/`), {
      method,
      redirect: "error",
      signal: AbortSignal.timeout(this.timeoutMS),
      headers: {
        Authorization: `Bearer ${this.token}`,
        Accept: "application/json",
        ...(body === undefined ? {} : { "Content-Type": "application/json" }),
        ...headers,
      },
      body: body === undefined ? undefined : JSON.stringify(body),
    });
    const declared = Number(response.headers.get("content-length") ?? 0);
    if (Number.isFinite(declared) && declared > MAX_RESPONSE_BYTES) throw new Error("adapter response exceeds 20 MiB");
    const raw = await readBounded(response);
    let payload = null;
    if (raw.byteLength > 0) {
      try {
        payload = JSON.parse(new TextDecoder("utf-8", { fatal: true }).decode(raw));
      } catch {
        throw new Error(`adapter endpoint returned invalid JSON (HTTP ${response.status})`);
      }
    }
    if (!response.ok) {
      const detail = typeof payload?.error === "string" ? payload.error.slice(0, 240) : response.statusText;
      throw new Error(`adapter endpoint rejected the request (HTTP ${response.status}${detail ? `: ${detail}` : ""})`);
    }
    return { status: response.status, payload };
  }

  async status() {
    const { payload } = await this.request("v2/adapter/status");
    if (payload?.protocol !== "contextbridge.adapter.v2" || !Array.isArray(payload.allowed_profiles) || !payload.allowed_profiles.includes(this.profile)) {
      throw new Error("core did not advertise the requested contextbridge.adapter.v2 profile");
    }
    return payload;
  }

  async profiles() {
    const { payload } = await this.request("v2/adapter/profiles");
    if (!payload || typeof payload !== "object" || Array.isArray(payload) || !(this.profile in payload)) {
      throw new Error("requested adapter profile is missing from the scoped profile response");
    }
    return payload;
  }

  async heartbeat(previousCapability = "", state = "idle") {
    const endpoint = { id: this.endpointID, profile: this.profile, state };
    if (previousCapability) endpoint.endpoint_capability = previousCapability;
    const { payload } = await this.request("v2/adapter/heartbeat", {
      method: "POST",
      body: {
        connected: true,
        ready: state === "idle",
        state: state === "idle" ? "waiting" : "busy",
        adapter: "reference-adapter",
        adapter_version: "1",
        active_endpoints: 1,
        busy_endpoints: state === "idle" ? 0 : 1,
        endpoints: [endpoint],
      },
    });
    const capability = payload?.endpoints?.find((item) => item.profile === this.profile && item.endpoint_id === this.endpointID)?.endpoint_capability;
    if (typeof capability !== "string" || !SAFE_OPAQUE.test(capability)) throw new Error("heartbeat did not return a bounded endpoint capability");
    return capability;
  }

  async next(endpointCapability, wait = true) {
    if (typeof endpointCapability !== "string" || !SAFE_OPAQUE.test(endpointCapability)) throw new Error("endpoint capability is required before polling");
    const query = new URLSearchParams({ profile: this.profile, endpoint_id: String(this.endpointID) });
    if (!wait) query.set("wait", "0");
    const { status, payload } = await this.request(`v2/adapter/jobs/next?${query}`, {
      headers: { "X-ContextBridge-Endpoint-Capability": endpointCapability },
    });
    if (status === 204) return null;
    if (!payload?.job?.id || !Number.isSafeInteger(payload.lease_generation) || payload.lease_generation < 1 || typeof payload.lease_capability !== "string" || !SAFE_OPAQUE.test(payload.lease_capability)) {
      throw new Error("core returned a malformed adapter lease");
    }
    return payload;
  }

  leaseHeaders(work) {
    if (!work?.job?.id || !Number.isSafeInteger(work.lease_generation) || work.lease_generation < 1 || typeof work.lease_capability !== "string" || !SAFE_OPAQUE.test(work.lease_capability)) {
      throw new Error("valid adapter work and lease credentials are required");
    }
    return {
      "X-ContextBridge-Lease-Generation": String(work.lease_generation),
      "X-ContextBridge-Lease-Capability": work.lease_capability,
    };
  }

  async claim(work, action = "prepare") {
    if (!["prepare", "mutate", "commit"].includes(action)) throw new Error("claim action must be prepare, mutate, or commit");
    return (await this.request(`v2/adapter/jobs/${encodeURIComponent(work.job.id)}/claim`, {
      method: "POST", headers: this.leaseHeaders(work), body: { action },
    })).payload;
  }

  async lease(work, { renew = false } = {}) {
    const { payload } = await this.request(`v2/adapter/jobs/${encodeURIComponent(work.job.id)}/lease`, {
      method: renew ? "POST" : "GET", headers: this.leaseHeaders(work),
    });
    if (payload?.ok !== true || typeof payload.lease_expires_at !== "string" || !Number.isFinite(Date.parse(payload.lease_expires_at))) {
      throw new Error("core returned malformed adapter lease status");
    }
    return payload;
  }

  async progress(work, { sequence, text = "", phase = "generating", percent = 0, busy = true } = {}) {
    if (!Number.isSafeInteger(sequence) || sequence < 1) throw new Error("progress sequence must be a positive safe integer");
    return (await this.request(`v2/adapter/jobs/${encodeURIComponent(work.job.id)}/progress`, {
      method: "POST", headers: this.leaseHeaders(work), body: { sequence, text, phase, percent, busy },
    })).payload;
  }

  async complete(work, output) {
    return (await this.request(`v2/adapter/jobs/${encodeURIComponent(work.job.id)}/complete`, {
      method: "POST", headers: this.leaseHeaders(work), body: output,
    })).payload;
  }
}

export async function runReferenceAdapterOnce(options) {
  const client = new ContextBridgeAdapterV2(options);
  await client.status();
  await client.profiles();
  let endpointCapability = await client.heartbeat();
  const deadline = Date.now() + (options.waitMS ?? 30_000);
  while (Date.now() < deadline) {
    const work = await client.next(endpointCapability, false);
    if (!work) {
      await new Promise((resolve) => setTimeout(resolve, 200));
      continue;
    }
    await client.lease(work);
    await client.claim(work, "prepare");
    await client.progress(work, { sequence: 1, text: "reference adapter accepted the bounded lease", percent: 50, busy: true });
    endpointCapability = await client.heartbeat(endpointCapability, "busy");
    await client.lease(work);
    const result = await client.complete(work, { mode: "text", text: options.reply ?? "REFERENCE-ADAPTER-OK", model: "reference-adapter" });
    await client.heartbeat(endpointCapability, "idle");
    return { job_id: work.job.id, result };
  }
  throw new Error("no compatible adapter job arrived before the bounded wait expired");
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  runReferenceAdapterOnce({
    baseURL: process.env.CONTEXTBRIDGE_URL ?? "http://127.0.0.1:32145",
    token: process.env.CONTEXTBRIDGE_ADAPTER_TOKEN ?? "",
    profile: process.env.CONTEXTBRIDGE_ADAPTER_PROFILE ?? "reference",
    endpointID: Number(process.env.CONTEXTBRIDGE_ADAPTER_ENDPOINT_ID ?? 1),
    reply: process.env.CONTEXTBRIDGE_REFERENCE_REPLY ?? "REFERENCE-ADAPTER-OK",
  }).then((result) => {
    process.stdout.write(`${JSON.stringify(result)}\n`);
  }).catch((error) => {
    process.stderr.write(`reference adapter: ${error.message}\n`);
    process.exitCode = 1;
  });
}
