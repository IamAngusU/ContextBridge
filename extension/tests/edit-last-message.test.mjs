import assert from 'node:assert/strict';
import fs from 'node:fs';
import vm from 'node:vm';
import { webcrypto } from 'node:crypto';
import { TextEncoder } from 'node:util';

const source = fs.readFileSync(new URL('../src/background.js', import.meta.url), 'utf8');
const listener = { addListener() {} };

class Textarea {
  constructor(value = '') { this.value = value; this.offsetWidth = 1; }
  focus() {}
  dispatchEvent() {}
  getClientRects() { return [1]; }
}

function fixture(provider) {
  const oldPrompt = `CB-${provider}-OLD`;
  const newPrompt = `CB-${provider}-NEW`;
  const content = {
    id: provider === 'gemini' ? 'user-query-content-6' : '',
    textContent: oldPrompt,
    hasAttachment: false,
    querySelector() { return this.hasAttachment ? {} : null; }
  };
  const editor = new Textarea(oldPrompt);
  const input = { offsetWidth: 1, getClientRects: () => [1], closest: () => null };
  const answer = {
    offsetWidth: 1, innerText: 'OLD-ANSWER', textContent: 'OLD-ANSWER', identity: 'answer-old',
    getClientRects: () => [1], querySelector: () => null, querySelectorAll: () => [],
    getAttribute(name) { return name === 'id' ? this.identity : null; },
    closest: () => null, matches: () => false
  };
  let editOpen = false;
  let stopVisible = false;
  let editClicks = 0;
  let updateClicks = 0;
  const editButton = { offsetWidth: 1, click() { editClicks++; editOpen = true; }, getClientRects: () => [1] };
  const updateButton = {
    offsetWidth: 1, disabled: false, innerText: provider === 'gemini' ? 'Aktualisieren' : 'Senden',
    getClientRects: () => [1], getAttribute: () => null,
    click() {
      updateClicks++;
      content.textContent = editor.value;
      answer.innerText = 'NEW-ANSWER';
      answer.textContent = 'NEW-ANSWER';
      answer.identity = 'answer-new';
    }
  };
  const turn = {
    getAttribute: (name) => provider === 'chatgpt' && name === 'data-turn-id' ? 'owned-turn-1' : null,
    querySelector(selector) {
      if (selector.includes('textarea')) return editOpen ? editor : null;
      if (selector.includes('data-message-author-role') || selector.includes('user-query-content-')) return content;
      if (selector.includes('prompt-edit-button') || selector.includes('Nachricht bearbeiten')) return editButton;
      return null;
    },
    querySelectorAll: (selector) => selector === 'button' ? [updateButton] : []
  };
  const document = {
    querySelectorAll(selector) {
      if (selector === '#input') return [input];
      if (selector === '#response') return [answer];
      if (selector === (provider === 'gemini' ? 'user-query' : 'section[data-turn="user"]')) return [turn];
      if (selector.includes('stop-button') && stopVisible) return [{ offsetWidth: 1, getClientRects: () => [1] }];
      return [];
    },
    querySelector: () => null
  };
  const chrome = {
    runtime: { onInstalled: listener, onStartup: listener, onMessage: listener },
    tabs: { onUpdated: listener, onRemoved: listener },
    storage: { local: { get: async (defaults) => defaults, set: async () => {} } }
  };
  const context = vm.createContext({
    chrome, document, crypto: webcrypto, TextEncoder, HTMLTextAreaElement: Textarea,
    HTMLInputElement: class {}, InputEvent: class {}, Event: class {},
    console, URL, Date, Promise, setTimeout, clearTimeout, setInterval, clearInterval
  });
  vm.runInContext(source, context);
  const profile = { name: provider, selectors: { input: ['#input'], response: ['#response'], submit: [] } };
  const deadline = () => new Date(Date.now() + 20000).toISOString();
  return { context, content, oldPrompt, newPrompt, profile, deadline,
    setStop: (value) => { stopVisible = value; }, editClicks: () => editClicks, updateClicks: () => updateClicks };
}

for (const provider of ['chatgpt', 'gemini']) {
  const site = fixture(provider);
  const owned = await site.context.inspectLatestOwnedTurn(site.oldPrompt, provider);
  assert.ok(owned?.id && owned?.digest);
  assert.equal(await site.context.inspectLatestOwnedTurn('A DIFFERENT USER PROMPT', provider), null);
  const answer = await site.context.automate({ prompt: site.newPrompt, output: { mode: 'text' } }, site.profile, site.deadline(), owned);
  assert.equal(answer.ok, true, `${provider}: ${answer.error || ''}`);
  assert.equal(answer.text, 'NEW-ANSWER');
  assert.equal(site.content.textContent, site.newPrompt);
  assert.equal(site.editClicks(), 1);
  assert.equal(site.updateClicks(), 1);

  const updated = await site.context.inspectLatestOwnedTurn(site.newPrompt, provider);
  site.content.hasAttachment = true;
  const blocked = await site.context.automate({ prompt: 'DO-NOT-SEND', output: { mode: 'text' } }, site.profile, site.deadline(), updated);
  assert.equal(blocked.ok, false);
  assert.equal(blocked.code, 'browser_edit_unavailable');
  assert.equal(site.editClicks(), 1);
  assert.equal(site.updateClicks(), 1);
}

{
  const site = fixture('chatgpt');
  site.setStop(true);
  const blocked = await site.context.automate({ prompt: 'UNSENT', output: { mode: 'text' } }, site.profile, site.deadline());
  assert.equal(blocked.ok, false);
  assert.equal(blocked.code, 'browser_provider_busy');
  assert.equal(site.editClicks(), 0);
  assert.equal(site.updateClicks(), 0);
}

console.log('Owned text turns edit in place; mismatched prompts and attachments fail closed');
