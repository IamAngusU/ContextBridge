import assert from 'node:assert/strict';
import fs from 'node:fs';
import vm from 'node:vm';
import { TextEncoder } from 'node:util';

const source = fs.readFileSync(new URL('../src/background.js', import.meta.url), 'utf8');
const state = { running: false, token: 'test', tabId: 0, tabIds: [], autoAttachFreshTabs: true, autoAttachBlockedTabIds: [] };
const tab = { id: 41, url: 'https://chatgpt.com/' };
const listener = { addListener() {} };
const chrome = {
  runtime: { onInstalled: listener, onStartup: listener, onMessage: listener, getManifest: () => ({ version: 'test' }) },
  tabs: { onUpdated: listener, onRemoved: listener, get: async () => ({ ...tab }), query: async () => [{ ...tab }] },
  storage: { local: {
    get: async (defaults) => ({ ...defaults, ...state }),
    set: async (value) => { Object.assign(state, value); }
  } },
  permissions: { contains: async () => true },
  scripting: { executeScript: async () => [{ result: true }] }
};
const context = vm.createContext({
  chrome, console, URL, TextEncoder, AbortController, setTimeout, clearTimeout, setInterval, clearInterval, Date, Promise,
  navigator: { userAgent: 'Chrome/151.0' },
  ContextBridgeProfiles: { forURL: (url) => url.startsWith('https://chatgpt.com/') ? { name: 'chatgpt' } : null }
});
vm.runInContext(source, context);
await new Promise((resolve) => setImmediate(resolve));
context.poll = async () => {};
context.sendHeartbeat = async () => true;
state.running = true;

await context.maybeAutoAttachFreshTab(tab.id);
assert.deepEqual(Array.from(state.tabIds), [41]);
await context.maybeAutoAttachFreshTab(tab.id);
assert.deepEqual(Array.from(state.tabIds), [41], 'reloading the same tab must not attach it twice');

state.tabId = 0;
state.tabIds = [];
state.autoAttachBlockedTabIds = [41];
await context.maybeAutoAttachFreshTab(tab.id);
assert.deepEqual(Array.from(state.tabIds), []);

state.autoAttachBlockedTabIds = [];
tab.url = 'https://chatgpt.com/c/existing';
await context.maybeAutoAttachFreshTab(tab.id);
assert.deepEqual(Array.from(state.tabIds), []);
state.tabIds = [41];
state.tabId = 41;
state.running = false;
context.startHeartbeat = () => {};
context.discoverFreshTabs = async () => {};
context.fetch = async (url, options) => {
  assert.equal(url, 'http://127.0.0.1:32145/v1/status');
  assert.equal(options.headers.Authorization, 'Bearer test');
  return { status: 200, ok: true, json: async () => ({ ok: true, version: 'v0.5.15' }) };
};
assert.equal((await context.testBridge()).version, 'v0.5.15');
await context.startPairing();
assert.equal(state.running, true);
state.running = false;
context.sendHeartbeat = async () => false;
await assert.rejects(context.startPairing(), /did not accept the browser connection/);
assert.equal(state.running, false);
context.sendHeartbeat = async () => true;
state.token = '';
await assert.rejects(context.startPairing(), /pairing token once/);
assert.equal(state.running, false);
state.token = 'test';
context.fetch = async () => ({ status: 401, ok: false });
await assert.rejects(context.startPairing(), /Pairing token is invalid/);
assert.equal(state.running, false);
state.token = 'test';
state.tabId = 41;
state.tabIds = [41, 42];
state.sessionBindings = { stale: { tabId: 41, autoCreated: true, perJob: true }, kept: { tabId: 42 } };
chrome.tabs.get = async (id) => {
  if (id === 41) throw new Error('No tab with id: 41.');
  return { id, url: 'https://gemini.google.com/app/live', title: 'Live Gemini' };
};
const reconciled = await context.reconcileConfiguredTabs();
assert.deepEqual(Array.from(reconciled.removed), [41]);
assert.deepEqual(Array.from(reconciled.tabs, (item) => item.id), [42]);
assert.deepEqual(Array.from(state.tabIds), [42]);
assert.equal(state.sessionBindings.stale, undefined);
assert.equal(state.sessionBindings.kept.tabId, 42);
state.running = true;
state.relayConnected = false;
state.tabId = 51;
state.tabIds = [51, 52];
vm.runInContext('stopRequested = false', context);
chrome.tabs.get = async (id) => {
  if (id === 51) throw new Error('No tab with id: 51.');
  return { id, url: 'https://chatgpt.com/c/live', title: 'Live ChatGPT' };
};
let heartbeat;
context.fetch = async (url, options) => {
  assert.match(url, /\/v1\/browser\/heartbeat$/);
  heartbeat = JSON.parse(options.body);
  return { status: 200, ok: true };
};
assert.equal(await context.sendHeartbeatOnce('waiting', true), true,
  'a tab closing between reconciliation and heartbeat must not reject healthy tabs');
assert.deepEqual(Array.from(state.tabIds), [52]);
assert.equal(heartbeat.active_tabs, 1);
assert.equal(heartbeat.tabs[0].id, 52);
state.tabId = 41;
state.tabIds = [41];
state.running = false;
chrome.tabs.get = async () => { throw new Error('No tab with id: 41.'); };
context.fetch = async () => ({ status: 200, ok: true, json: async () => ({ ok: true, version: 'test' }) });
await assert.rejects(context.startPairing(), /All attached AI tabs were closed/);
assert.deepEqual(Array.from(state.tabIds), []);
context.delay = async () => {};
chrome.tabs.get = async () => ({ ...tab, status: 'loading' });
chrome.scripting.executeScript = async () => [{ result: { input: 1 } }];
await context.waitForTabReady(tab.id, { input: ['[contenteditable="true"]'] }, 1000);
chrome.tabs.get = async () => { throw new Error('No tab with id'); };
await assert.rejects(context.waitForTabReady(tab.id, {}, 1000), /tab was closed/);
console.log('Automatic attachment requires an empty new chat and honors manual detachment');
