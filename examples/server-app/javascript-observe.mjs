// SPDX-License-Identifier: Apache-2.0
// Print one bounded, read-only ContextBridge UI snapshot.

import { ContextBridgeUIClient } from "./contextbridge-ui-client.mjs";

const relayURL = requiredEnv("CONTEXTBRIDGE_RELAY_URL");
const token = requiredEnv("CONTEXTBRIDGE_OBSERVER_TOKEN");
const client = new ContextBridgeUIClient({ relayURL, token });

try {
  const snapshot = await client.snapshot({ jobLimit: 25 });
  process.stdout.write(JSON.stringify(snapshot, null, 2) + "\n");
} catch (error) {
  console.error(`ContextBridge observation failed: ${error.message}`);
  process.exitCode = 1;
}

function requiredEnv(name) {
  const value = String(process.env[name] || "").trim();
  if (!value) throw new Error(`${name} is required`);
  return value;
}
