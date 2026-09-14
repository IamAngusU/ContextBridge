import assert from 'node:assert/strict';
import fs from 'node:fs';
import vm from 'node:vm';

for (const browser of ['chromium', 'firefox']) {
  const manifest = JSON.parse(fs.readFileSync(new URL(`../manifests/${browser}.json`, import.meta.url), 'utf8'));
  assert.match(manifest.content_security_policy.extension_pages, /connect-src[^;]*https:/);
}

const source = fs.readFileSync(new URL('../src/popup.js', import.meta.url), 'utf8');
const markup = fs.readFileSync(new URL('../src/popup.html', import.meta.url), 'utf8');
for (const [, id] of source.matchAll(/\$\('([^']+)'\)/g)) {
  assert.ok(markup.includes(`id="${id}"`), `Popup is missing #${id}`);
}
const listeners = new Map();
const elements = new Map();
const writes = [];
const stored = {};
const statuses = [];
const runtimeMessages = [];
const permissionRequests = [];
let updatesEnabled = false;
let startError = '';
let pendingStart = null;
let activePageID = 52;
const element = (id) => {
  if (!elements.has(id)) elements.set(id, {
    value: id === 'tab' ? '31' : '',
    classList: { toggle() {} },
    attributes: new Map(),
    setAttribute(name, value) { this.attributes.set(name, value); },
    removeAttribute(name) { this.attributes.delete(name); },
    addEventListener(type, listener) { listeners.set(`${id}:${type}`, listener); }
  });
  return elements.get(id);
};
const context = vm.createContext({
  fetch: async (_url, request = {}) => ({ ok: true, json: async () => {
    if (request.method === 'PUT') updatesEnabled = JSON.parse(request.body).enabled;
    return { enabled: updatesEnabled };
  } }),
  chrome: {
    runtime: { onMessage: { addListener() {} }, sendMessage: async (message) => {
      runtimeMessages.push(message.type);
      if (message.type === 'start' && pendingStart) await pendingStart;
      return message.type === 'start' && startError ? { ok: false, error: startError } : { ok: true };
    } },
    storage: { onChanged: { addListener() {} }, local: {
      get: async (defaults) => ({ ...defaults, ...stored }),
      set: async (value) => { Object.assign(stored, value); writes.push(value); }
    } },
    tabs: {
      get: async (id) => ({ id, url: id === 99 ? 'https://bugcrowd.com/engagements/openai-safety' : id === 52 ? 'https://gemini.google.com/app/existing' : 'https://chatgpt.com/c/existing' }),
      query: async () => [{ id: activePageID, url: activePageID === 52 ? 'https://gemini.google.com/app/existing' : 'https://chatgpt.com/c/existing', title: 'Active AI page' }]
    },
    permissions: { request: async (request) => { permissionRequests.push(request); return true; } }
  },
  document: { addEventListener() {}, getElementById: element },
  URL,
  console,
  setTimeout,
  clearTimeout,
  ContextBridgeProfiles: {
    forURL(url) {
      if (url.startsWith('https://chatgpt.com/')) return { name: 'chatgpt', label: 'ChatGPT (auto-detected)' };
      if (url.startsWith('https://gemini.google.com/')) return { name: 'gemini', label: 'Gemini (auto-detected)' };
      return null;
    }
  }
});
vm.runInContext(source, context);

const oldChats = Array.from({ length: 20 }, (_, index) => ({ id: index + 1, url: `https://chatgpt.com/c/${index}`, active: false, fresh: false }));
const activeChat = { id: 25, url: 'https://chatgpt.com/c/current', active: true, fresh: false };
const gemini = { id: 26, url: 'https://gemini.google.com/app/current', active: true, fresh: false };
assert.deepEqual(Array.from(context.selectStarterTabs([...oldChats, activeChat, gemini])), []);
const freshChatGPT = { id: 27, url: 'https://chatgpt.com/', fresh: true };
const freshGemini = { id: 28, url: 'https://gemini.google.com/app', fresh: true };
const picked = context.selectStarterTabs([...oldChats, activeChat, gemini, freshChatGPT, freshGemini]);
assert.deepEqual(Array.from(picked, (tab) => tab.id), [27, 28]);
assert.equal(context.isFreshChatURL(activeChat.url), false);
assert.equal(context.isFreshChatURL(gemini.url), false);
assert.equal(context.isFreshChatURL(freshChatGPT.url), true);
assert.equal(context.isFreshChatURL(freshGemini.url), true);
assert.deepEqual(Array.from(context.filterTabList([freshChatGPT, freshGemini], 'attached', [27]), (tab) => tab.id), [27]);
assert.deepEqual(Array.from(context.filterTabList([freshChatGPT, freshGemini], 'available', [27]), (tab) => tab.id), [28]);
assert.equal(context.preferredTabID(0, [
  { id: 99, active: true, title: 'Another Opera window' },
  { id: 25, active: true, title: 'Current ChatGPT window' }
], 25, [99]), 25);
assert.equal(context.preferredTabID(99, [{ id: 99 }, { id: 25 }], 25, [25]), 99);
element('tab').value = '99';
assert.equal(await context.scanTargetTabID(), 52, 'a stale non-AI selection must scan the active AI tab');
element('tab').value = '31';
assert.equal(await context.scanTargetTabID(), 31, 'an explicitly selected AI tab must take precedence');
assert.equal(context.tabDisplayState(true, 'working'), 'Working');
assert.equal(context.tabDisplayState(true, 'rate_limited'), 'Cooling down');
assert.equal(context.tabDisplayState(false, 'working'), 'Available');
assert.equal(context.permissionPattern('http://127.0.0.1:32145'), 'http://127.0.0.1/*');
assert.deepEqual(Array.from(context.mediaOriginsFor([{ url: 'https://gemini.google.com/app' }])), ['https://contribution.usercontent.google.com/*']);
context.updateActions(null, true);
assert.equal(element('pair').disabled, false);

context.loadTabs = async () => { await context.updateCurrentPageAction(); };
context.refreshState = async () => {};
context.hasTabsPermission = async () => true;
context.setStatus = (state, message) => { statuses.push({ state, message }); };
context.renderRunning = () => {};
await listeners.get('toggle-selected-tab:click')();
assert.deepEqual(Array.from(writes.at(-1).tabIds), [31]);
await listeners.get('toggle-selected-tab:click')();
assert.deepEqual(Array.from(writes.at(-1).tabIds), []);
assert.deepEqual(Array.from(stored.autoAttachBlockedTabIds), [31]);
await context.updateCurrentPageAction();
assert.equal(elements.get('current-provider').textContent, 'Gemini detected');
assert.equal(elements.get('toggle-current-tab').textContent, 'Attach this page');
await listeners.get('toggle-current-tab:click')();
assert.deepEqual(Array.from(stored.tabIds), [52]);
assert.equal(elements.get('toggle-current-tab').textContent, 'Detach this page');
assert.equal(statuses.at(-1).state, 'idle');
await listeners.get('toggle-current-tab:click')();
assert.deepEqual(Array.from(stored.tabIds), []);
assert.deepEqual(Array.from(stored.autoAttachBlockedTabIds), [31, 52]);
element('url').value = 'http://127.0.0.1:32145';
element('token').value = 'test-token';
const requestsBeforeConnect = permissionRequests.length;
await listeners.get('pair:click')();
assert.deepEqual(Array.from(stored.tabIds), [52]);
assert.equal(stored.token, 'test-token');
assert.equal(permissionRequests.length - requestsBeforeConnect, 1);
assert.deepEqual(Array.from(permissionRequests.at(-1).origins), ['https://gemini.google.com/*', 'https://contribution.usercontent.google.com/*', 'http://127.0.0.1/*']);
assert.ok(runtimeMessages.includes('start'));
assert.ok(!runtimeMessages.includes('test'));
let releaseStart;
pendingStart = new Promise((resolve) => { releaseStart = resolve; });
const startsBeforePending = runtimeMessages.filter((name) => name === 'start').length;
const pendingConnect = listeners.get('pair:click')();
assert.equal(element('pair').disabled, true);
assert.equal(element('pair').attributes.get('aria-busy'), 'true');
context.showConnectionProgress({ phase: 'Inspecting AI tabs', done: 2, total: 4, etaSeconds: 7 });
assert.equal(element('pair').textContent, 'Connecting 2/4…');
assert.deepEqual(statuses.at(-1), { state: 'connecting', message: 'Inspecting AI tabs · 2/4 · about 7s left' });
await listeners.get('pair:click')();
releaseStart();
await pendingConnect;
pendingStart = null;
assert.equal(runtimeMessages.filter((name) => name === 'start').length, startsBeforePending + 1, 'duplicate Connect click started a second attempt');
assert.equal(element('pair').attributes.has('aria-busy'), false);
assert.equal(element('edit-last-message-row').hidden, false);
element('edit-last-message').checked = true;
await listeners.get('edit-last-message:change')();
assert.equal(runtimeMessages.at(-1), 'set-tab-edit-mode');
await context.refreshUpdatePreference();
assert.equal(element('auto-update').disabled, false);
assert.equal(element('auto-update').checked, false);
element('auto-update').checked = true;
await listeners.get('auto-update:change')();
assert.equal(updatesEnabled, true);
assert.equal(element('auto-update').checked, true);
element('session-mode').value = 'new_chat';
await listeners.get('session-mode:change')();
assert.equal(stored.sessionMode, 'new_chat');
element('session-mode').value = 'new_chat_per_job';
await listeners.get('session-mode:change')();
assert.equal(stored.sessionMode, 'new_chat_per_job');
element('auto-close-finished').checked = true;
await listeners.get('auto-close-finished:change')();
assert.equal(stored.autoCloseFinishedChats, true);
await listeners.get('release-session-tab:click')();
assert.equal(runtimeMessages.at(-1), 'release-session-tab');
stored.lastError = 'The browser tab did not finish reloading';
startError = 'Local ContextBridge did not accept the browser connection';
await listeners.get('pair:click')();
assert.equal(stored.lastError, '');
assert.equal(stored.connectionError, startError);
assert.deepEqual(statuses.at(-1), { state: 'error', message: startError });
console.log('Current AI page auto-detected; one Connect click attaches and starts it');
