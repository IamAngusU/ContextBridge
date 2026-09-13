import assert from 'node:assert/strict';
import fs from 'node:fs';
import vm from 'node:vm';

const source = fs.readFileSync(new URL('../src/background.js', import.meta.url), 'utf8');
const state = { running: false, token: 'test', tabId: 0, tabIds: [], autoAttachFreshTabs: true, autoAttachBlockedTabIds: [] };
const tab = { id: 41, url: 'https://chatgpt.com/' };
const listener = { addListener() {} };
const chrome = {
  runtime: { onInstalled: listener, onStartup: listener, onMessage: listener },
  tabs: { onUpdated: listener, onRemoved: listener, get: async () => ({ ...tab }), query: async () => [{ ...tab }] },
  storage: { local: {
    get: async (defaults) => ({ ...defaults, ...state }),
    set: async (value) => { Object.assign(state, value); }
  } },
  permissions: { contains: async () => true },
  scripting: { executeScript: async () => [{ result: true }] }
};
const context = vm.createContext({ chrome, console, URL, setTimeout, clearTimeout, setInterval, clearInterval, Date, Promise });
vm.runInContext(source, context);
await new Promise((resolve) => setImmediate(resolve));
context.poll = async () => {};
context.sendHeartbeat = async () => {};
state.running = true;

await context.maybeAutoAttachFreshTab(tab.id);
assert.deepEqual(Array.from(state.tabIds), [41]);

state.tabId = 0;
state.tabIds = [];
state.autoAttachBlockedTabIds = [41];
await context.maybeAutoAttachFreshTab(tab.id);
assert.deepEqual(Array.from(state.tabIds), []);

state.autoAttachBlockedTabIds = [];
tab.url = 'https://chatgpt.com/c/existing';
await context.maybeAutoAttachFreshTab(tab.id);
assert.deepEqual(Array.from(state.tabIds), []);
console.log('Automatic attachment requires an empty new chat and honors manual detachment');
