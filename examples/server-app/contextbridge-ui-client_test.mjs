// SPDX-License-Identifier: Apache-2.0

import assert from "node:assert/strict";
import { readBoundedText } from "./bounded-response.mjs";
import { ContextBridgeUIClient } from "./contextbridge-ui-client.mjs";

const requests = [];
const client = new ContextBridgeUIClient({
  relayURL: "https://relay.example.test",
  token: "observer-token",
  fetchImpl: async (url, options) => {
    requests.push({ url, options });
    return new Response('{"ok":true}', { status: 200, headers: { "content-type": "application/json" } });
  },
});

await client.job("job.");
assert.match(requests[0].url, /\/v1\/cluster\/jobs\/job\.$/u);
assert.equal(requests[0].options.redirect, "error");
for (const id of [".", "..", "_job", "-job", "job..child", "job/child", "job\\child", "job child", "a".repeat(129)]) {
  assert.throws(() => client.job(id), /identifier contract/u);
}
assert.throws(() => client.jobs({ limit: 201 }), /between 1 and 200/u);

const oversized = new ContextBridgeUIClient({
  relayURL: "https://relay.example.test",
  token: "observer-token",
  maxResponseBytes: 1024,
  fetchImpl: async () => new Response(new ReadableStream({
    start(controller) {
      controller.enqueue(new Uint8Array(700));
      controller.enqueue(new Uint8Array(325));
      controller.close();
    },
  }), { status: 200 }),
});
await assert.rejects(() => oversized.overview(), /exceeds 1024 bytes/u);

const exact = new TextEncoder().encode("é".repeat(512));
assert.equal(exact.byteLength, 1024);
assert.equal(await readBoundedText(new Response(exact), 1024), "é".repeat(512));
await assert.rejects(
  () => readBoundedText(new Response("{}", { headers: { "content-length": "1025" } }), 1024),
  /exceeds 1024 bytes/u,
);
await assert.rejects(
  () => readBoundedText(new Response(new Uint8Array([0xc3, 0x28])), 1024),
  /encoded data|encoding/u,
);

let cancelled = false;
const chunkedUnicode = new Response(new ReadableStream({
  start(controller) {
    controller.enqueue(new TextEncoder().encode("é".repeat(400)));
    controller.enqueue(new TextEncoder().encode("é".repeat(200)));
  },
  cancel() {
    cancelled = true;
  },
}));
await assert.rejects(() => readBoundedText(chunkedUnicode, 1024, "relay response"), /exceeds 1024 bytes/u);
assert.equal(cancelled, true);

console.log("ContextBridge JavaScript reference client tests passed.");
