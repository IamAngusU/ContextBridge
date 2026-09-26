// SPDX-License-Identifier: Apache-2.0
// Dependency-free read-only ContextBridge client for Node.js 18+, trusted
// desktop runtimes, or a private web backend. Never bundle an observer token
// into public browser JavaScript.

import { readBoundedText } from "./bounded-response.mjs";

const DEFAULT_MAX_RESPONSE_BYTES = 12 << 20;
const DEFAULT_TIMEOUT_MS = 10_000;

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

  async snapshot({ jobLimit = 50, jobStatus = "" } = {}) {
    const [protocol, overview, nodes, jobs, pipelines] = await Promise.all([
      this.protocol(),
      this.overview(),
      this.nodes(),
      this.jobs({ limit: jobLimit, status: jobStatus }),
      this.pipelines(),
    ]);
    return {
      schema: "contextbridge.ui.snapshot.v1",
      fetched_at: new Date().toISOString(),
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
