import assert from 'node:assert/strict';
import fs from 'node:fs';
import vm from 'node:vm';

const source = fs.readFileSync(new URL('../src/popup.js', import.meta.url), 'utf8');
const listeners = new Map();
const elements = new Map();
const writes = [];
const stored = {};
const element = (id) => {
  if (!elements.has(id)) elements.set(id, {
    value: id === 'tab' ? '31' : '',
    addEventListener(type, listener) { listeners.set(`${id}:${type}`, listener); }
  });
  return elements.get(id);
};
const context = vm.createContext({
  chrome: {
    runtime: { onMessage: { addListener() {} }, sendMessage: async () => ({ ok: true }) },
    storage: { onChanged: { addListener() {} }, local: {
      get: async (defaults) => ({ ...defaults, ...stored }),
      set: async (value) => { Object.assign(stored, value); writes.push(value); }
    } },
    tabs: { get: async (id) => ({ id, url: 'https://chatgpt.com/c/existing' }) },
    permissions: { request: async () => true }
  },
  document: { addEventListener() {}, getElementById: element },
  URL,
  console,
  ContextBridgeProfiles: {
    forURL(url) {
      if (url.startsWith('https://chatgpt.com/')) return { name: 'chatgpt' };
      if (url.startsWith('https://gemini.google.com/')) return { name: 'gemini' };
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
assert.equal(context.tabDisplayState(true, 'working'), 'Working');
assert.equal(context.tabDisplayState(true, 'rate_limited'), 'Cooling down');
assert.equal(context.tabDisplayState(false, 'working'), 'Available');

context.loadTabs = async () => {};
context.refreshState = async () => {};
context.hasTabsPermission = async () => true;
context.setStatus = () => {};
await listeners.get('attach-tab:click')();
assert.deepEqual(Array.from(writes.at(-1).tabIds), [31]);
await listeners.get('detach-tab:click')();
assert.deepEqual(Array.from(writes.at(-1).tabIds), []);
assert.deepEqual(Array.from(stored.autoAttachBlockedTabIds), [31]);
console.log('Only confirmed fresh ChatGPT and Gemini chats are auto-selected');
