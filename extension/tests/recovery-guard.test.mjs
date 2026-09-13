import assert from 'node:assert/strict';
import fs from 'node:fs';
import vm from 'node:vm';
import { webcrypto } from 'node:crypto';
import { TextEncoder } from 'node:util';

const source = fs.readFileSync(new URL('../src/background.js', import.meta.url), 'utf8');
const listener = { addListener() {} };

async function fixture() {
  let tick = 0;
  let reloads = 0;
  let stopVisible = true;
  let stopDisabled = true;
  let actionReady = true;
  let attachment = false;
  let draft = '';
  let turnText = 'OWNED-TURN';
  let responseText = 'Completed answer';
  let mutateOnDelay = null;
  const url = 'https://chatgpt.com/c/owned-conversation';
  const visible = { offsetWidth: 1, getClientRects: () => [1] };
  const copy = { ...visible, getAttribute(name) { return name === 'data-testid' ? 'copy-turn-action-button' : null; } };
  const stop = {
    ...visible,
    get disabled() { return stopDisabled; },
    getAttribute(name) { return name === 'data-testid' ? 'stop-button' : null; },
    querySelector() { return {}; }
  };
  const input = {
    ...visible,
    get value() { return draft; },
    closest() { return { querySelectorAll: () => [], querySelector: () => attachment ? {} : null }; }
  };
  const content = { get textContent() { return turnText; } };
  const turn = {
    getAttribute(name) { return name === 'data-turn-id' ? 'owned-id' : null; },
    querySelector() { return content; },
    compareDocumentPosition() { return 4; }
  };
  const response = {
    ...visible,
    id: 'response-id',
    get textContent() { return responseText; },
    querySelectorAll(selector) {
      if (selector === 'button') return actionReady ? [copy] : [];
      if (selector.includes('.markdown')) return [{ get textContent() { return responseText; } }];
      return [];
    }
  };
  const document = {
    querySelectorAll(selector) {
      if (selector === '#input') return [input];
      if (selector === '#response') return [response];
      if (selector === 'section[data-turn="user"]') return [turn];
      if (selector === 'button[data-testid="stop-button"]') return stopVisible ? [stop] : [];
      return [];
    }
  };
  const storage = { sessionBindings: {} };
  const chrome = {
    runtime: { onInstalled: listener, onStartup: listener, onMessage: listener },
    tabs: {
      onUpdated: listener, onRemoved: listener,
      get: async () => ({ url }),
      reload: async () => { reloads++; stopVisible = false; }
    },
    scripting: { executeScript: async ({ func, args = [] }) => [{ result: await func(...args) }] },
    storage: { local: {
      get: async (defaults) => ({ ...defaults, ...storage }),
      set: async (update) => Object.assign(storage, update)
    } }
  };
  class FakeDate extends Date { static now() { return tick; } }
  const context = vm.createContext({ chrome, document, crypto: webcrypto, TextEncoder, console, URL,
    Date: FakeDate, Promise, setTimeout, clearTimeout, setInterval, clearInterval });
  vm.runInContext(source, context);
  context.delay = async (ms) => { tick += ms; mutateOnDelay?.(tick); };
  const profile = { name: 'chatgpt', selectors: { input: ['#input'], response: ['#response'] } };
  const ownedTurn = await context.inspectLatestOwnedTurn(turnText, 'chatgpt');
  const work = { job: { id: 'test-job', session_id: 'test-session', prompt: 'NEW-TURN', metadata: {} },
    profile, deadline: new Date(Date.now() + 180000).toISOString() };
  storage.sessionBindings[context.workSessionKey(work)] = { tabId: 7, url, ownedTurn };
  return {
    context, chrome, storage, profile, ownedTurn, work,
    reloads: () => reloads,
    setStopVisible: (value) => { stopVisible = value; },
    setStopDisabled: (value) => { stopDisabled = value; },
    setActionReady: (value) => { actionReady = value; },
    setAttachment: (value) => { attachment = value; },
    setDraft: (value) => { draft = value; },
    setTurnText: (value) => { turnText = value; },
    setResponseText: (value) => { responseText = value; },
    setMutateOnDelay: (value) => { mutateOnDelay = value; }
  };
}

async function exerciseRecoveryAfterSubmit(unsafeDraft) {
  const site = await fixture();
  site.setStopVisible(false);
  site.work.job.output = { mode: 'text' };
  let automations = 0;
  const originalExecute = site.chrome.scripting.executeScript;
  site.chrome.scripting.executeScript = async (request) => {
    if (request.func !== site.context.automate) return originalExecute(request);
    automations++;
    if (automations === 1) {
      site.setTurnText('NEW-TURN');
      if (unsafeDraft) site.setDraft('Typed during generation');
      return [{ result: { ok: false, code: 'stalled_response', error: 'Stale Stop', recoverable: true } }];
    }
    return [{ result: { ok: true, text: 'Recovered answer', artifacts: [] } }];
  };
  site.context.resolveWorkTab = async () => 7;
  site.context.waitForTabSlot = async () => {};
  site.context.sendHeartbeat = async () => true;
  site.context.captureTabProgress = async () => ({ text: 'Completed answer', busy: false });
  site.context.reportProgress = async () => {};
  site.context.completeWork = async () => {};
  await site.context.processWork({ useVisualProfile: false, preserveDrafts: false, pendingCompletions: {} }, site.work, 7);
  return { site, automations };
}

{
  const { site, automations } = await exerciseRecoveryAfterSubmit(true);
  assert.equal(automations, 1);
  assert.equal(site.reloads(), 0);
  assert.equal(site.storage.pendingCompletions['test-job'].error, 'browser_recovery_unsafe');
}

{
  const { site, automations } = await exerciseRecoveryAfterSubmit(false);
  assert.equal(automations, 2);
  assert.equal(site.reloads(), 1);
  assert.equal(site.storage.pendingCompletions['test-job'].text, 'Recovered answer');
}

{
  const site = await fixture();
  site.setStopVisible(false);
  assert.equal(await site.context.recoverPriorStall(site.work, 7, site.profile, site.ownedTurn), false);
  assert.equal(site.reloads(), 0);
}

{
  const site = await fixture();
  site.setStopDisabled(false);
  await assert.rejects(site.context.recoverPriorStall(site.work, 7, site.profile, site.ownedTurn), /Stop is still clickable/);
  assert.equal(site.reloads(), 0);
}

{
  const site = await fixture();
  assert.equal(await site.context.recoverPriorStall(site.work, 7, site.profile, site.ownedTurn), true);
  assert.equal(site.reloads(), 1);
}

for (const [mutate, expected] of [
  [(site) => site.setDraft('A private unsent draft'), /unsent draft/],
  [(site) => site.setAttachment(true), /unsent attachment/],
  [(site) => site.setTurnText('SOMEONE-ELSE'), /not the expected ContextBridge turn/],
  [(site) => site.setActionReady(false), /no visible completion controls/]
]) {
  const site = await fixture();
  mutate(site);
  await assert.rejects(site.context.recoverPriorStall(site.work, 7, site.profile, site.ownedTurn), expected);
  assert.equal(site.reloads(), 0);
}

{
  const site = await fixture();
  site.setMutateOnDelay((tick) => site.setResponseText(`Still changing at ${tick}`));
  await assert.rejects(site.context.recoverPriorStall(site.work, 7, site.profile, site.ownedTurn), /not enough stable idle evidence/);
  assert.equal(site.reloads(), 0);
}

{
  const site = await fixture();
  site.setMutateOnDelay((tick) => { if (tick >= 5000) site.setDraft('Typed while waiting'); });
  await assert.rejects(site.context.recoverPriorStall(site.work, 7, site.profile, site.ownedTurn), /unsent draft/);
  assert.equal(site.reloads(), 0);
}

{
  const site = await fixture();
  site.setTurnText('NEW-TURN');
  await site.context.requireSafeReloadState(7, site.profile, { prompt: 'NEW-TURN' }, false);
  site.setDraft('Typed during generation');
  await assert.rejects(site.context.requireSafeReloadState(7, site.profile, { prompt: 'NEW-TURN' }, false), /unsent draft/);
  site.setDraft('');
  site.setTurnText('ANOTHER-USER-TURN');
  await assert.rejects(site.context.requireSafeReloadState(7, site.profile, { prompt: 'NEW-TURN' }, false), /not the expected ContextBridge turn/);
  assert.equal(site.reloads(), 0);
}

console.log('Guarded reload needs an owned, completed, stable turn and never discards drafts or attachments');
