// SPDX-License-Identifier: Apache-2.0
// Dependency-free read-only ContextBridge client for Node.js 18+, trusted
// desktop runtimes, or a private web backend. Never bundle an observer token
// into public browser JavaScript.

import { readBoundedText } from "./bounded-response.mjs";

const DEFAULT_MAX_RESPONSE_BYTES = 12 << 20;
const DEFAULT_TIMEOUT_MS = 10_000;
const DEFAULT_STREAM_TIMEOUT_MS = 45_000;
const MAX_EVENT_FRAME_BYTES = 1 << 20;

export class ContextBridgeUIClient {
  constructor({ relayURL, token, fetchImpl = globalThis.fetch, timeoutMS = DEFAULT_TIMEOUT_MS, maxResponseBytes = DEFAULT_MAX_RESPONSE_BYTES }) {
    this.relayURL = validateRelayURL(relayURL);
    this.token = requiredText(token, "observer token");
    if (typeof fetchImpl !== "function") throw new TypeError("a fetch implementation is required");
    this.fetch = fetchImpl;
    this.timeoutMS = boundedInteger(timeoutMS, 100, 120_000, "timeoutMS");
    this.maxResponseBytes = boundedInteger(maxResponseBytes, 1024, 64 << 20, "maxResponseBytes");
  }

  protocol() {
    return this.requestJSON("/v1/cluster/protocol");
  }

  openAPI() {
    return this.requestJSON("/v1/cluster/openapi.json");
  }

  whoami() {
    return this.requestJSON("/v1/cluster/whoami");
  }

  overview() {
    return this.requestJSON("/v1/cluster/overview");
  }

  nodes() {
    return this.requestJSON("/v1/cluster/nodes");
  }

  operationalEvents({ limit = 100 } = {}) {
    return this.requestJSON(`/v1/cluster/events?limit=${boundedInteger(limit, 1, 500, "limit")}`);
  }

  jobs({ limit = 50, status = "" } = {}) {
    const query = new URLSearchParams({ limit: String(boundedInteger(limit, 1, 200, "limit")) });
    if (status) query.set("status", requiredText(status, "status"));
    return this.requestJSON(`/v1/cluster/jobs?${query}`);
  }

  jobPage({ limit = 50, cursor = "", status = "", ownerSubject = "", tenantID = "" } = {}) {
    const query = new URLSearchParams({ page: "1", limit: String(boundedInteger(limit, 1, 200, "limit")) });
    if (cursor) query.set("cursor", requiredText(cursor, "cursor"));
    if (status) query.set("status", requiredText(status, "status"));
    if (ownerSubject) query.set("owner_subject", requiredText(ownerSubject, "ownerSubject"));
    if (tenantID) query.set("tenant_id", requiredText(tenantID, "tenantID"));
    return this.requestJSON(`/v1/cluster/jobs?${query}`);
  }

  job(jobID) {
    return this.requestJSON(`/v1/cluster/jobs/${pathID(jobID)}`);
  }

  jobEvents(jobID, { after = 0, limit = 100 } = {}) {
    const query = new URLSearchParams({
      after: String(boundedInteger(after, 0, Number.MAX_SAFE_INTEGER, "after")),
      limit: String(boundedInteger(limit, 1, 500, "limit")),
    });
    return this.requestJSON(`/v1/cluster/jobs/${pathID(jobID)}/events?${query}`);
  }

  jobEventStream(jobID, options = {}) {
    return this.requestEventStream(`/v1/cluster/jobs/${pathID(jobID)}/events/stream`, options);
  }

  jobEstimate(jobID) {
    return this.requestJSON(`/v1/cluster/jobs/${pathID(jobID)}/estimate`);
  }

  pipelines() {
    return this.requestJSON("/v1/cluster/pipelines");
  }

  pipelineRun(runID) {
    return this.requestJSON(`/v1/cluster/pipeline-runs/${pathID(runID)}`);
  }

  pipelineActivity(runID) {
    return this.requestJSON(`/v1/cluster/pipeline-runs/${pathID(runID)}/activity`);
  }

  pipelineEvents(runID, { after = 0, limit = 100 } = {}) {
    const query = new URLSearchParams({
      after: String(boundedInteger(after, 0, Number.MAX_SAFE_INTEGER, "after")),
      limit: String(boundedInteger(limit, 1, 500, "limit")),
    });
    return this.requestJSON(`/v1/cluster/pipeline-runs/${pathID(runID)}/events?${query}`);
  }

  pipelineEventStream(runID, options = {}) {
    return this.requestEventStream(`/v1/cluster/pipeline-runs/${pathID(runID)}/events/stream`, options);
  }

  async snapshot({ jobLimit = 50, jobStatus = "" } = {}) {
    const [identity, protocol, overview, nodes, jobs, pipelines] = await Promise.all([
      this.whoami(),
      this.protocol(),
      this.overview(),
      this.nodes(),
      this.jobs({ limit: jobLimit, status: jobStatus }),
      this.pipelines(),
    ]);
    return {
      schema: "contextbridge.ui.snapshot.v1",
      fetched_at: new Date().toISOString(),
      identity,
      protocol,
      overview,
      nodes,
      jobs,
      pipelines,
    };
  }

  async requestJSON(path) {
    if (typeof path !== "string" || !path.startsWith("/v1/cluster/") || path.includes("\0")) {
      throw new TypeError("only ContextBridge cluster API paths are allowed");
    }
    const response = await this.fetch(this.relayURL + path, {
      method: "GET",
      headers: { Authorization: `Bearer ${this.token}`, Accept: "application/json" },
      signal: AbortSignal.timeout(this.timeoutMS),
      redirect: "error",
    });
    const raw = await readBoundedText(response, this.maxResponseBytes);
    if (!response.ok) {
      throw new Error(`ContextBridge returned HTTP ${response.status}: ${raw.slice(0, 4096)}`);
    }
    try {
      return JSON.parse(raw);
    } catch (error) {
      throw new Error(`ContextBridge returned invalid JSON: ${error.message}`);
    }
  }

  async *requestEventStream(path, { after = "0", reconnect = true, signal = undefined } = {}) {
    if (typeof path !== "string" || !path.startsWith("/v1/cluster/") || !path.endsWith("/events/stream") || path.includes("\0")) {
      throw new TypeError("only ContextBridge execution event stream paths are allowed");
    }
    let cursor = eventCursor(after);
    for (;;) {
      const requestSignal = signal ?? AbortSignal.timeout(DEFAULT_STREAM_TIMEOUT_MS);
      const query = new URLSearchParams({ after: cursor });
      const response = await this.fetch(`${this.relayURL}${path}?${query}`, {
        method: "GET",
        headers: {
          Authorization: `Bearer ${this.token}`,
          Accept: "text/event-stream",
          ...(cursor !== "0" ? { "Last-Event-ID": cursor } : {}),
        },
        signal: requestSignal,
        redirect: "error",
      });
      if (!response.ok) {
        const raw = await readBoundedText(response, Math.min(this.maxResponseBytes, 64 << 10), "ContextBridge stream error");
        throw new Error(`ContextBridge returned HTTP ${response.status}: ${raw.slice(0, 4096)}`);
      }
      if (!String(response.headers.get("content-type") ?? "").toLowerCase().startsWith("text/event-stream")) {
        await response.body?.cancel?.("unexpected event stream content type");
        throw new Error("ContextBridge returned a non-SSE execution event stream");
      }
      if (response.headers.get("x-contextbridge-event-stream") !== "authoritative-events-v1") {
        await response.body?.cancel?.("unsupported event stream contract");
        throw new Error("ContextBridge returned an unsupported execution event stream contract");
      }
      let retryMS = 1000;
      let terminal = false;
      for await (const frame of parseEventStream(response, MAX_EVENT_FRAME_BYTES)) {
        if (frame.retry !== undefined) {
          retryMS = frame.retry;
        }
        if (frame.data === undefined) continue;
        if (frame.id !== "") cursor = eventCursor(frame.id);
        const item = {
          schema: "contextbridge.ui-event.v1",
          event: frame.event,
          id: frame.id,
          data: frame.data,
        };
        yield item;
        if (terminalEvent(frame.event)) terminal = true;
      }
      if (terminal || !reconnect) return;
      await abortableDelay(retryMS, signal);
    }
  }
}

async function* parseEventStream(response, maximumFrameBytes) {
  if (!response.body || typeof response.body.getReader !== "function") {
    throw new Error("runtime cannot read the execution event stream");
  }
  const reader = response.body.getReader();
  const decoder = new TextDecoder("utf-8", { fatal: true });
  let buffer = "";
  try {
    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;
      buffer += decoder.decode(value, { stream: true });
      if (new TextEncoder().encode(buffer).byteLength > maximumFrameBytes && eventBoundary(buffer) === null) {
        await reader.cancel("execution event frame byte limit exceeded");
        throw new Error(`ContextBridge execution event frame exceeds ${maximumFrameBytes} bytes`);
      }
      for (;;) {
        const boundary = eventBoundary(buffer);
        if (boundary === null) break;
        const raw = buffer.slice(0, boundary.index);
        buffer = buffer.slice(boundary.index + boundary.length);
        const parsed = parseEventFrame(raw, maximumFrameBytes);
        if (parsed !== null) yield parsed;
      }
    }
    buffer += decoder.decode();
    if (buffer.trim() !== "") {
      const parsed = parseEventFrame(buffer, maximumFrameBytes);
      if (parsed !== null) yield parsed;
    }
  } finally {
    reader.releaseLock();
  }
}

function parseEventFrame(raw, maximumFrameBytes) {
  if (new TextEncoder().encode(raw).byteLength > maximumFrameBytes) {
    throw new Error(`ContextBridge execution event frame exceeds ${maximumFrameBytes} bytes`);
  }
  let id = "";
  let event = "message";
  let retry;
  const data = [];
  for (const line of raw.split(/\r\n|\r|\n/u)) {
    if (line === "" || line.startsWith(":")) continue;
    const separator = line.indexOf(":");
    const field = separator < 0 ? line : line.slice(0, separator);
    const value = separator < 0 ? "" : line.slice(separator + 1).replace(/^ /u, "");
    if (field === "id" && !value.includes("\0")) id = value;
    else if (field === "event") event = requiredText(value, "event type");
    else if (field === "data") data.push(value);
    else if (field === "retry" && /^\d{1,6}$/u.test(value)) retry = Math.min(30_000, Math.max(100, Number(value)));
  }
  if (data.length === 0) return retry === undefined ? null : { retry };
  const serialized = data.join("\n");
  let payload;
  try {
    payload = JSON.parse(serialized);
  } catch (error) {
    throw new Error(`ContextBridge returned invalid event JSON: ${error.message}`);
  }
  return { id, event, data: payload, retry };
}

function eventBoundary(value) {
  const candidates = [
    { index: value.indexOf("\r\n\r\n"), length: 4 },
    { index: value.indexOf("\n\n"), length: 2 },
    { index: value.indexOf("\r\r"), length: 2 },
  ].filter((candidate) => candidate.index >= 0).sort((left, right) => left.index - right.index);
  return candidates[0] ?? null;
}

function eventCursor(value) {
  const cursor = String(value ?? "").trim();
  if (!/^\d{1,20}$/u.test(cursor) || BigInt(cursor) > 18_446_744_073_709_551_615n) {
    throw new RangeError("event cursor must be an unsigned 64-bit integer");
  }
  return String(BigInt(cursor));
}

function terminalEvent(event) {
  return ["job.completed", "job.failed", "job.cancelled", "job.ambiguous", "pipeline.completed", "pipeline.failed", "pipeline.cancelled"].includes(event);
}

function abortableDelay(milliseconds, signal) {
  if (signal?.aborted) return Promise.reject(signal.reason ?? new Error("stream aborted"));
  return new Promise((resolve, reject) => {
    const onAbort = () => {
      clearTimeout(timer);
      reject(signal.reason ?? new Error("stream aborted"));
    };
    const timer = setTimeout(() => {
      signal?.removeEventListener("abort", onAbort);
      resolve();
    }, milliseconds);
    signal?.addEventListener("abort", onAbort, { once: true });
  });
}

function validateRelayURL(value) {
  const parsed = new URL(requiredText(value, "relayURL"));
  const loopback = ["127.0.0.1", "localhost", "[::1]", "::1"].includes(parsed.hostname);
  if (parsed.username || parsed.password) {
    throw new Error("relay URL must not contain credentials");
  }
  if (parsed.protocol !== "https:" && !(parsed.protocol === "http:" && loopback)) {
    throw new Error("relay URL must use HTTPS unless it is loopback");
  }
  parsed.pathname = parsed.pathname.replace(/\/$/, "");
  parsed.search = "";
  parsed.hash = "";
  return parsed.toString().replace(/\/$/, "");
}

function requiredText(value, label) {
  const normalized = String(value ?? "").trim();
  if (!normalized || /[\r\n\0]/u.test(normalized)) throw new TypeError(`${label} is required and must be one line`);
  return normalized;
}

function boundedInteger(value, minimum, maximum, label) {
  if (!Number.isSafeInteger(value) || value < minimum || value > maximum) {
    throw new RangeError(`${label} must be an integer between ${minimum} and ${maximum}`);
  }
  return value;
}

function pathID(value) {
  const id = requiredText(value, "ID");
  if (!/^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$/u.test(id) || id.includes("..")) {
    throw new TypeError("ID does not match the ContextBridge identifier contract");
  }
  return encodeURIComponent(id);
}
