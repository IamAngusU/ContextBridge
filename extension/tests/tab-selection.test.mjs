import assert from 'node:assert/strict';
import fs from 'node:fs';
import vm from 'node:vm';

const source = fs.readFileSync(new URL('../src/popup.js', import.meta.url), 'utf8');
const element = () => ({ addEventListener() {} });
const context = vm.createContext({
  chrome: { runtime: { onMessage: { addListener() {} } } },
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

const oldChats = Array.from({ length: 20 }, (_, index) => ({ id: index + 1, url: `https://chatgpt.com/c/${index}`, active: false }));
const activeChat = { id: 25, url: 'https://chatgpt.com/c/current', active: true };
const gemini = { id: 26, url: 'https://gemini.google.com/app/current', active: true };
const picked = context.selectStarterTabs([...oldChats, activeChat, gemini]);
assert.deepEqual(Array.from(picked, (tab) => tab.id), [25, 26]);
console.log('Starter tab selection keeps both ChatGPT and Gemini even with many old chats');
