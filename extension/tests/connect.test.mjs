import assert from 'node:assert/strict';
import fs from 'node:fs';
import vm from 'node:vm';

const source = fs.readFileSync(new URL('../src/background.js', import.meta.url), 'utf8');
const listener = { addListener() {} };
const state = {
  bridgeUrl: 'http://127.0.0.1:32145', token: 'test-token', tabId: 7, tabIds: [7],
  running: false, autoReconnect: true, useVisualProfile: true, taughtProfiles: {}, tabCapabilities: {}, tabCapabilityScans: {}
};
let pageInspections = 0;
let serviceChecks = 0;
const heartbeats = [];
const chrome = {
  runtime: { onInstalled: listener, onStartup: listener, onMessage: listener, getManifest: () => ({ version: 'test' }) },
  tabs: { onUpdated: listener, onRemoved: listener,
    get: async (id) => ({ id, title: 'Still loading', url: 'https://chatgpt.com/c/test', status: 'loading' }) },
  scripting: { executeScript: () => { pageInspections += 1; return new Promise(() => {}); } },
  storage: { local: {
    get: async (defaults) => ({ ...defaults, ...state }),
    set: async (changes) => { Object.assign(state, changes); }
  } }
};
const context = vm.createContext({
  chrome, console, URL, navigator: { userAgent: 'Chrome/151.0' }, AbortController,
  setTimeout, clearTimeout, setInterval, clearInterval, Date, Promise,
  ContextBridgeProfiles: { forURL: (url) => url.startsWith('https://chatgpt.com/')
    ? { name: 'chatgpt', label: 'ChatGPT', selectors: {} } : null },
  fetch: async (url, options) => {
    if (url.endsWith('/v1/status')) {
      serviceChecks += 1;
      return { status: 200, ok: true, json: async () => ({ ok: true, version: 'v0.test' }) };
    }
    if (url.endsWith('/v1/browser/heartbeat')) {
      heartbeats.push(JSON.parse(options.body));
      return { status: 200, ok: true };
    }
    throw new Error(`unexpected URL ${url}`);
  }
});
vm.runInContext(source, context);
context.startHeartbeat = () => {};
context.poll = () => {};
context.discoverFreshTabs = async () => {};
context.closeFinishedOwnedTabs = async () => {};
context.scheduleTabDiagnostics = () => {};

const first = context.startPairing();
const duplicate = context.startPairing();
assert.strictEqual(first, duplicate, 'reopening the popup must not start a second connection attempt');
assert.equal((await first).ok, true);
assert.equal(serviceChecks, 1);
assert.equal(pageInspections, 0, 'page DOM or model-picker inspection must not gate Connect');
assert.equal(state.running, true);
assert.equal(state.relayConnected, true);
assert.equal(heartbeats[0].active_tabs, 1);
assert.equal(heartbeats[0].tabs[0].profile, 'chatgpt');
assert.equal(state.connectionProgress, null);

await context.sendHeartbeatOnce('waiting', false);
assert.equal(pageInspections, 0, 'a normal heartbeat must not wait for a suspended AI page');
assert.ok(heartbeats.length >= 2);

chrome.scripting.executeScript = async ({ func }) => {
  if (func.name === 'safeToDiscoverPageCapabilities') return [{ result: false }];
  if (func.name === 'inspectPageCapabilities') return [{ result: { currentModel: 'GPT-5.6 Sol', currentReasoning: 'High' } }];
  if (func.name === 'inspectPageDOM') return [{ result: { prompt_inputs: 1 } }];
  throw new Error(`unexpected inspection ${func.name}`);
};
await context.refreshTabDiagnostics(7);
await context.sendHeartbeatOnce('waiting', false);
const updatedTab = heartbeats.at(-1).tabs[0];
assert.equal(updatedTab.current_model, 'GPT-5.6 Sol');
assert.equal(updatedTab.current_reasoning, 'High');
assert.equal(updatedTab.dom.prompt_inputs, 1);

// A transient outage keeps the user's requested connection but pauses work
// until a heartbeat succeeds. With automatic reconnect disabled, three
// failures end that intent and require a fresh deliberate Connect.
context.fetch = async () => { throw new Error('service offline'); };
await context.sendHeartbeatOnce('waiting');
await context.sendHeartbeatOnce('waiting');
assert.equal(state.running, true);
assert.equal(state.relayConnected, false);
context.fetch = async (url) => url.endsWith('/v1/browser/heartbeat')
  ? { status: 200, ok: true } : { status: 200, ok: true, json: async () => ({ ok: true }) };
await context.sendHeartbeatOnce('waiting');
assert.equal(state.relayConnected, true);
state.autoReconnect = false;
context.fetch = async () => { throw new Error('service offline'); };
await context.sendHeartbeatOnce('waiting');
await context.sendHeartbeatOnce('waiting');
assert.equal(state.running, true);
await context.sendHeartbeatOnce('waiting');
assert.equal(state.running, false);
assert.equal(state.relayConnected, false);
assert.match(state.connectionError, /Automatic reconnect is off/);

state.running = true;
state.relayConnected = true;
await context.resume(true);
assert.equal(state.running, false, 'an extension update must honor the disabled automatic reconnect preference');
assert.match(state.connectionError, /Automatic reconnect is off/);

state.autoReconnect = true;
state.running = true;
state.relayConnected = true;
vm.runInContext('stopRequested = false', context);
context.fetch = async () => ({ status: 401, ok: false });
await context.sendHeartbeatOnce('waiting');
assert.equal(state.running, false, 'a rejected pairing token must halt automatic retries');
assert.equal(state.relayConnected, false);
assert.match(state.connectionError, /pairing token/i);

state.running = true;
vm.runInContext('stopRequested = false', context);
context.fetch = async () => ({ status: 200, ok: true });
await context.stopPairing();
await context.resume(true);
assert.equal(state.running, false, 'a deliberate Disconnect must survive an extension update');

assert.match(fs.readFileSync(new URL('../src/popup.html', import.meta.url), 'utf8'), /id="auto-reconnect"/);
