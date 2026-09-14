import assert from 'node:assert/strict';
import fs from 'node:fs';
import vm from 'node:vm';

const source = fs.readFileSync(new URL('../src/background.js', import.meta.url), 'utf8');
const listener = { addListener() {} };
const state = { tabId: 1, tabIds: [1, 2], sessionMode: 'manual', sessionBindingsMigrated: true };
let createForeground = false;
const tabs = new Map([
  [1, { id: 1, url: 'https://chatgpt.com/c/first' }],
  [2, { id: 2, url: 'https://chatgpt.com/c/second' }]
]);
const chrome = {
  runtime: { onInstalled: listener, onStartup: listener, onMessage: listener },
  tabs: {
    onUpdated: listener, onRemoved: listener,
    get: async (id) => { if (!tabs.has(id)) throw new Error('closed tab'); return { ...tabs.get(id) }; },
    create: async ({ url, active }) => {
      assert.equal(active, Boolean(createForeground));
      const tab = { id: 3, url };
      tabs.set(3, tab);
      return { ...tab };
    },
    remove: async (id) => { tabs.delete(id); }
  },
  scripting: { executeScript: async () => [{ result: { empty: true, provider_busy: false, has_attachments: false, focused: false } }] },
  storage: { local: {
    get: async (defaults) => ({ ...defaults, ...state }),
    set: async (value) => { Object.assign(state, value); }
  } },
  permissions: { contains: async () => true }
};
const context = vm.createContext({
  chrome, console, URL, setTimeout, clearTimeout, setInterval, clearInterval, Date, Promise,
  ContextBridgeProfiles: { forURL: (url) => url.startsWith('https://chatgpt.com/') ? { name: 'chatgpt', selectors: { input: ['#prompt-textarea'] } } : null }
});
vm.runInContext(source, context);
context.poll = async () => {};
context.sendHeartbeat = async () => true;
context.waitForTabReady = async () => {};
context.checkFreshTab = async (id) => id === 3 || tabs.get(id)?.url === 'https://chatgpt.com/';
const work = (key, metadata = {}) => ({ job: { contextbridge_session_key: key, metadata }, profile: { name: 'chatgpt' } });
const bindingKey = (key) => context.workSessionKey(work(key));
assert.notEqual(bindingKey('shared'), context.workSessionKey({ job: { contextbridge_session_key: 'shared' }, profile: { name: 'gemini' } }));
assert.notEqual(context.workSessionKey({ job: { id: 'one', contextbridge_session_key: 'shared', metadata: { contextbridge_new_chat_per_job: true } }, profile: { name: 'chatgpt' } }),
  context.workSessionKey({ job: { id: 'two', contextbridge_session_key: 'shared', metadata: { contextbridge_new_chat_per_job: true } }, profile: { name: 'chatgpt' } }));
assert.equal(context.safeToCloseOwnedTab({ empty: true, provider_busy: false, has_attachments: false, focused: false }), true);
assert.equal(context.safeToCloseOwnedTab({ empty: false }), false);
assert.equal(context.safeToCloseOwnedTab({ empty: true, has_attachments: true }), false);
await context.setTabEditMode(1, true);
assert.equal(state.tabEditModes[1], true);
await assert.rejects(context.setTabEditMode(99, true), /Attach this AI tab/);

const [first, second] = await Promise.all([
  context.resolveWorkTab({}, work('producer-a/session-1'), 1),
  context.resolveWorkTab({}, work('producer-b/session-1'), 1)
]);
assert.equal(first, 1);
assert.equal(second, 2);
assert.equal(await context.resolveWorkTab({}, work('producer-a/session-1'), 2), 1);
await assert.rejects(context.resolveWorkTab({}, work('session-3'), 1), /No unassigned AI tab/);
tabs.get(1).url = 'https://chatgpt.com/c/other';
await assert.rejects(context.resolveWorkTab({}, work('producer-a/session-1'), 1), /moved or closed/);
tabs.get(1).url = 'https://chatgpt.com/';
await context.releaseSessionTab(1);
assert.equal(await context.resolveWorkTab({}, work('session-3'), 1), 1);
tabs.get(1).url = 'https://chatgpt.com/c/third';
await context.rememberSessionURL(bindingKey('session-3'), 1);
tabs.get(1).url = 'https://chatgpt.com/c/first';
assert.equal(await context.resolveWorkTab({}, work('producer-a/session-1'), 1), 1);
assert.equal(state.sessionBindings[bindingKey('session-3')].tabId, 0);
tabs.get(1).url = 'https://chatgpt.com/c/third';
assert.equal(await context.resolveWorkTab({}, work('session-3'), 1), 1);

state.sessionMode = 'new_chat';
assert.equal(await context.resolveWorkTab({}, work('session-4'), 1), 3);
assert.deepEqual(Array.from(state.tabIds), [1, 2, 3]);
assert.equal(state.sessionBindings[bindingKey('session-4')].autoCreated, true);
const savedBindings = state.sessionBindings;
state.sessionBindings = {};
state.tabIds = [1, 2];
createForeground = true;
assert.equal(await context.resolveWorkTab({}, work('foreground', { contextbridge_foreground_new_chat: true }), 1), 3);
createForeground = false;
state.sessionBindings = savedBindings;
state.sessionMode = 'manual';
await assert.rejects(context.resolveWorkTab({}, work('session-5'), 1), /No unassigned AI tab/);
state.sessionBindings = {};
state.sessionBindingsMigrated = false;
state.sessionTabs = { old: 1 };
state.tabIds = [1];
await assert.rejects(context.resolveWorkTab({}, work('new'), 1), /No unassigned AI tab/);
assert.equal(state.sessionBindings['legacy-tab:1'].legacy, true);
tabs.get(1).url = 'https://chatgpt.com/';
assert.equal(await context.resolveWorkTab({}, work('new'), 1), 1);
assert.equal(state.sessionBindings['legacy-tab:1'], undefined);
tabs.set(3, { id: 3, url: 'https://chatgpt.com/c/owned', active: false });
state.running = true;
state.autoCloseFinishedChats = true;
state.tabIds = [1, 3];
const cleanupWork = { job: { id: 'cleanup-job', contextbridge_session_key: 'cleanup', metadata: { contextbridge_new_chat_per_job: true } },
  profile: { name: 'chatgpt' } };
const cleanupKey = context.workSessionKey(cleanupWork);
state.sessionBindings = { [cleanupKey]: { tabId: 3, url: 'https://chatgpt.com/c/owned', autoCreated: true,
  ownedTurn: { id: 'owned', digest: 'proof' } } };
await context.markFinishedTabForClose(cleanupWork, 3, { artifacts: [] });
assert.ok(state.sessionBindings[cleanupKey].closeEligibleAt > Date.now());
state.sessionBindings[cleanupKey].closeEligibleAt = Date.now() - 1;
await context.closeFinishedOwnedTabs();
assert.equal(tabs.has(3), false, 'only an inactive completed owned tab may auto-close');
tabs.set(3, { id: 3, url: 'https://chatgpt.com/c/manual', active: false });
state.sessionBindings = { manual: { tabId: 3, url: 'https://chatgpt.com/c/manual', autoCreated: false,
  ownedTurn: { id: 'owned', digest: 'proof' }, closeEligibleAt: Date.now() - 1 } };
await context.closeFinishedOwnedTabs();
assert.equal(tabs.has(3), true, 'a manually attached tab must not auto-close');
console.log('Browser sessions are exclusive, producer-scoped, recoverable, and fail closed');
