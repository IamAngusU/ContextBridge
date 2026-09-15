import assert from 'node:assert/strict';
import fs from 'node:fs';
import vm from 'node:vm';
import { webcrypto } from 'node:crypto';
import { TextEncoder } from 'node:util';

const source = fs.readFileSync(new URL('../src/background.js', import.meta.url), 'utf8');
assert.equal(source.includes('await response.blob()'), false, 'page artifact collection must enforce its byte limit while streaming');
assert.equal(source.includes("fetch(candidate.url, { credentials: 'include'"), false,
  'provider DOM URLs must not be fetched credentialed before origin policy checks');
assert.match(source, /jobs\/next\?wait=25[\s\S]{0,300}fetchWithTimeout|fetchWithTimeout[\s\S]{0,300}jobs\/next\?wait=25/,
  'the relay long poll must have an abortable deadline');
assert.equal(source.includes("add(href, cleanFileName(anchor.download"), true,
  'download links stay available as references');
assert.match(source, /add\(href,[\s\S]{0,300}'generic_link', false\)/,
  'generic assistant links must not trigger an extension-side transfer');
const listeners = { addListener() {} };
let alarmListener;
const alarmCalls = [];
let activeHeartbeatAlarm;
const chrome = {
  runtime: { onInstalled: listeners, onStartup: listeners, onMessage: listeners, getManifest: () => ({ version: 'test' }) },
  storage: { local: { get: async (defaults) => defaults, set: async () => {} } },
  tabs: {}, scripting: {}, i18n: { getMessage: () => '' },
  alarms: {
    onAlarm: { addListener(fn) { alarmListener = fn; } },
    get: async (name) => name === activeHeartbeatAlarm?.name ? activeHeartbeatAlarm : undefined,
    create: async (name, schedule) => {
      activeHeartbeatAlarm = { name, ...schedule };
      alarmCalls.push(['create', name, schedule.periodInMinutes, schedule.delayInMinutes]);
    },
    clear: async (name) => {
      if (name === activeHeartbeatAlarm?.name) activeHeartbeatAlarm = undefined;
      alarmCalls.push(['clear', name]);
      return true;
    }
  }
};
const context = vm.createContext({ chrome, console, URL, TextEncoder, AbortController, setTimeout, clearTimeout, setInterval, clearInterval, Date, Promise });
vm.runInContext(source, context);
context.crypto = webcrypto;
{
  const previousResolve = context.resolveWorkTab;
  const previousRenew = context.renewLease;
  const previousComplete = context.completeWork;
  const previousHeartbeat = context.sendHeartbeat;
  const activeLeases = vm.runInContext('activeBrowserLeases', context);
  let renewals = 0;
  context.renewLease = async () => { renewals += 1; return true; };
  context.resolveWorkTab = async () => {
    assert.ok(activeLeases.has('lease-before-tab'), 'a claimed job must be tracked before slow fresh-tab setup');
    assert.equal(renewals, 1, 'the claimed job must renew before slow fresh-tab setup');
    throw new Error('ordering check complete');
  };
  context.completeWork = async () => true;
  context.sendHeartbeat = async () => true;
  try {
    await context.processWork({ pendingCompletions: {} }, {
      job: { id: 'lease-before-tab', prompt: 'bounded test', metadata: {}, output: { mode: 'text' } },
      profile: { name: 'chatgpt' }, lease_generation: 3,
      lease_expires_at: new Date(Date.now() + 90000).toISOString(), deadline: new Date(Date.now() + 180000).toISOString()
    }, 7);
    assert.equal(activeLeases.has('lease-before-tab'), false, 'failed setup must release in-memory lease tracking');
  } finally {
    context.resolveWorkTab = previousResolve;
    context.renewLease = previousRenew;
    context.completeWork = previousComplete;
    context.sendHeartbeat = previousHeartbeat;
  }
}
{
  const previousFetch = context.fetch;
  const previousDelay = context.delay;
  const cfg = { bridgeUrl: 'http://127.0.0.1:32145', token: 'token' };
  let calls = 0;
  context.delay = async () => {};
  try {
    context.fetch = async () => {
      calls += 1;
      if (calls === 1) throw new TypeError('temporary network failure');
      return { ok: true, status: 200 };
    };
    assert.equal(await context.renewLease(cfg, 'retry-renew', 4), true);
    assert.equal(calls, 2, 'an idempotent renewal should retry one transient network failure');

    calls = 0;
    context.fetch = async (_url, options) => {
      calls += 1;
      assert.equal(JSON.parse(options.body).action, 'send');
      return calls === 1 ? { ok: false, status: 503 } : { ok: true, status: 200 };
    };
    assert.equal(await context.claimBrowserAction(cfg, 'retry-claim', 5, 'send'), true);
    assert.equal(calls, 2, 'the idempotent pre-send claim should retry a transient server failure');

    calls = 0;
    context.fetch = async () => { calls += 1; return { ok: false, status: 409 }; };
    assert.equal(await context.renewLease(cfg, 'lost-renew', 6), false);
    assert.equal(calls, 1, 'an authoritative lease conflict must not be retried');

    calls = 0;
    context.fetch = async () => { calls += 1; return { ok: false, status: 401 }; };
    await assert.rejects(context.renewLease(cfg, 'bad-auth', 7), (error) => error?.code === 'browser_bridge_unavailable');
    assert.equal(calls, 1, 'an authentication failure needs an operator fix, not repeated requests');
  } finally {
    context.fetch = previousFetch;
    context.delay = previousDelay;
  }
}
{
  const small = { state: 'waiting', active_tabs: 1, tabs: [{ id: 1, profile: 'chatgpt', state: 'waiting' }] };
  assert.equal(context.browserHeartbeatBody(small), JSON.stringify(small),
    'a normal heartbeat must retain its complete diagnostic payload');
  const long = 'x'.repeat(120);
  const controls = Array.from({ length: 32 }, (_, index) => ({
    tag: 'button', id: `${index}-${long}`, test_id: long, role: 'button', aria_label: long,
    text: long, type: 'button', accept: long, has_popup: 'menu', expanded: 'false', visible: true
  }));
  const tabs = Array.from({ length: 16 }, (_, index) => ({
    id: index + 1, origin: 'https://chatgpt.com', title: `tab-${index}-${long}`, profile: 'chatgpt', state: 'waiting',
    current_model: 'GPT-5.6 Sol', current_reasoning: 'Sehr hoch',
    models: Array.from({ length: 50 }, (_, model) => `model-${model}-${long}`),
    reasoning_levels: Array.from({ length: 20 }, (_, level) => `level-${level}-${long}`),
    dom_status: 'ready', dom: { page_visibility: 'hidden', was_discarded: false, tools: controls, model_controls: controls }
  }));
  const body = context.browserHeartbeatBody({
    state: 'waiting', tab_title: '😀'.repeat(100000), active_tabs: tabs.length, busy_tabs: 0, tabs
  });
  assert.ok(new TextEncoder().encode(body).byteLength <= 112 * 1024,
    'a maximal multi-tab diagnostic heartbeat must stay below the service limit with safety margin');
  const compact = JSON.parse(body);
  assert.equal(compact.tabs.length, 16, 'payload budgeting must not hide attached tabs from routing');
  assert.ok(compact.tab_title.length <= 300, 'page-controlled top-level metadata must also be bounded during compaction');
  assert.equal(compact.tabs[15].current_model, 'GPT-5.6 Sol', 'routing-critical current model metadata must survive compaction');
}
{
  assert.equal(alarmCalls.length, 0,
    'loading a worker must wait for its startup/install/alarm event instead of racing lifecycle policy');
  assert.equal(typeof alarmListener, 'function', 'a suspended worker must have an alarm wake listener');
  await context.startHeartbeat();
  assert.ok(alarmCalls.some(([action, name, minutes, delay]) => action === 'create'
    && name === 'contextbridge-heartbeat' && minutes === 0.5 && delay === 0.5));
  await context.startHeartbeat();
  assert.equal(alarmCalls.filter(([action]) => action === 'create').length, 1, 'restarting an active worker must not postpone the alarm');
  await context.stopHeartbeat();
  assert.ok(alarmCalls.some(([action, name]) => action === 'clear' && name === 'contextbridge-heartbeat'));
  const originalResume = context.resume;
  let wakes = 0;
  context.resume = () => { wakes += 1; };
  alarmListener({ name: 'unrelated' });
  await alarmListener({ name: 'contextbridge-heartbeat' });
  assert.equal(wakes, 1);
  context.resume = originalResume;
}
chrome.tabs.get = async (id) => ({ id, url: 'https://bugcrowd.com/engagements/openai-safety' });
await assert.rejects(context.scanPageCapabilities(99), /only on a ChatGPT or Gemini tab/);
assert.equal(context.capabilityScanInterval('chatgpt', { currentModel: '', scanDiagnostic: { model: 'no trigger (0 composer menus)', noTriggerAttempts: 1 } }), 15000);
assert.equal(context.capabilityScanInterval('chatgpt', { currentModel: '', scanDiagnostic: { model: 'no trigger (0 composer menus)', noTriggerAttempts: 3 } }), 300000);
assert.equal(context.capabilityScanInterval('chatgpt', { currentModel: 'GPT-5.6 Sol' }), 1800000);
{
  const health = context.summarizePageHealth({ discarded: true }, {
    status: 'ready', latencyMs: 999999, slowScans: 99,
    dom: { assistant_turns: 999999, gemini_user_turns: -5, page_visibility: 'private-value', was_discarded: false }
  });
  assert.equal(health.discarded, true);
  assert.equal(health.assistantTurns, 10000);
  assert.equal(health.userTurns, 0);
  assert.equal(health.diagnosticMs, 10000);
  assert.equal(health.slowScans, 10);
  assert.equal(health.visibility, 'unknown');
  assert.equal(health.domStatus, 'ready');
}
{
  const pill = { innerText: '5.6 Hoch', offsetWidth: 1, getAttribute: () => null };
  const composer = { querySelectorAll: () => [pill] };
  const input = { closest: () => composer };
  context.document = {
    querySelector: (selector) => selector.startsWith('#prompt-textarea') ? input : null,
    querySelectorAll: () => []
  };
  const visibleSelection = context.inspectPageCapabilities();
  assert.equal(visibleSelection.currentReasoning, 'Hoch');
  assert.equal(visibleSelection.currentModelVersion, '5.6');
  assert.equal(visibleSelection.currentModel, '', 'a compact version label must not invent a model variant');
}
assert.equal(context.classifyFailureReason('Prompt editor did not retain the submitted text'), 'prompt_not_retained');
assert.equal(context.classifyFailureReason('Send button stayed disabled after filling the prompt'), 'send_disabled');
assert.equal(context.classifyFailureReason('No compatible image upload input appeared; the prompt was not sent'), 'upload_input_missing');
assert.equal(context.classifyFailureReason('An unsent attachment is already present; the prompt was not sent'), 'attachment_busy');
assert.equal(context.classifyFailureReason('Gemini did not show the image-job prompt in a new user turn; no assistant answer was accepted'), 'submitted_prompt_unverified');
assert.equal(context.classifyFailureReason('ChatGPT still shows Stop; the draft was left untouched'), 'provider_busy');
assert.equal(context.classifyFailureReason('The model selector is not visible in this ChatGPT composer'), 'model_selector_missing');
assert.equal(context.classifyFailureReason('Requested model "GPT-5.5" is not available in this chat (0 candidates, 0 model choices, pill trigger)'), 'model_candidates_empty_pill');
assert.equal(context.classifyFailureReason('Requested model "GPT-5.5" is not available in this chat (0 candidates, 0 model choices, form trigger)'), 'model_candidates_empty_form');
assert.equal(context.classifyFailureReason('Requested model "GPT-5.5" is not available in this chat (0 candidates, 0 model choices)'), 'model_candidates_empty');
assert.equal(context.classifyFailureReason('Requested model "GPT-5.5" is not available in this chat (7 candidates, 0 model choices)'), 'model_choices_empty');
assert.equal(context.classifyFailureReason('Requested model "GPT-5.5" is disabled in this chat'), 'model_choice_disabled');
assert.equal(context.classifyFailureReason('Requested model "GPT-5.5" is not available in this chat'), 'model_choice_missing');
assert.equal(context.classifyFailureReason('The submitted ContextBridge turn could not be verified; no reload was performed'), 'recovery_turn_unverified');
assert.equal(context.classifyFailureReason('Automatic reload skipped: the latest user message is not the expected ContextBridge turn; the tab was left untouched'), 'recovery_turn_mismatch');
assert.equal(context.classifyFailureReason('Automatic reload skipped: an unsent draft is present; the tab was left untouched'), 'recovery_draft');
assert.equal(context.classifyFailureReason('A provider error containing private text'), 'other');
assert.equal(context.shouldForegroundStalledTab({ autoCreated: true }, { name: 'chatgpt' }, { ok: false, recoverable: true, code: 'stalled_response' }), true);
assert.equal(context.foregroundFreshChatForJob({ image_base64: 'iVBORw0KGgo=' }), true, 'fresh image-upload chats should wake their provider UI');
assert.equal(context.foregroundFreshChatForJob({ prompt: 'text only' }), false, 'text-only chats should stay in the background');
assert.equal(context.foregroundFreshChatForJob({ metadata: { contextbridge_foreground_new_chat: true } }), true, 'explicit foreground request should be honored');
assert.equal(context.shouldForegroundStalledTab({ autoCreated: false }, { name: 'chatgpt' }, { ok: false, recoverable: true, code: 'stalled_response' }), false);
assert.equal(context.shouldForegroundStalledTab({ autoCreated: true }, { name: 'gemini' }, { ok: false, recoverable: true, code: 'stalled_response' }), false);
assert.equal(context.shouldForegroundStalledTab({ autoCreated: true }, { name: 'chatgpt' }, { ok: false, recoverable: false, code: 'browser_timeout' }), false);
assert.equal(context.isNewAssistantTurn({ response_count: 2 }, { response_count: 2, text: 'Old music player clock changed', active_generation: true }), false);
assert.equal(context.isNewAssistantTurn({ response_count: 2 }, { response_count: 3, text: 'Fresh answer', active_generation: true }), true);
assert.equal(context.isNewAssistantTurn({ response_count: 2, response_identity: 'old' }, { response_count: 2, response_identity: 'new' }), true);
assert.equal(context.isNewAssistantTurn({ response_count: 9, response_identity: 'latest' }, { response_count: 8, response_identity: 'older' }), false);
assert.equal(context.isEditAssistantTurn({ response_count: 9 }, { response_count: 8, active_generation: true }, 'Older answer', 'Prior answer'), false);
assert.equal(context.isEditAssistantTurn({ response_count: 9 }, { response_count: 9, active_generation: true }, 'Fresh edited answer', 'Prior answer'), true);

{
  const exact = context.parseOutput('ä'.repeat(128), { mode: 'text', max_bytes: 256 }, 'test');
  assert.equal(exact.truncated, undefined);
  assert.equal(new TextEncoder().encode(exact.text).length, 256);
  const over = context.parseOutput('ä'.repeat(129), { mode: 'text', max_bytes: 256 }, 'test');
  assert.equal(over.truncated, true);
  assert.equal(new TextEncoder().encode(over.text).length, 256);
  const emoji = context.parseOutput('x'.repeat(255) + '😀', { mode: 'text', max_bytes: 256 }, 'test');
  assert.equal(emoji.truncated, true);
  assert.equal(emoji.text, 'x'.repeat(255), 'a UTF-16 surrogate pair must stay intact');
}

{
  const input = { value: 'A private unsent draft', offsetWidth: 1, offsetHeight: 1, getClientRects: () => [1], focus() {}, dispatchEvent() {} };
  context.document = { querySelectorAll: (selector) => selector === '#draft' ? [input] : [] };
  context.Event = class {};
  const selectors = { input: ['#draft'] };
  assert.equal(context.captureCurrentDraft(selectors).text, 'A private unsent draft');
  assert.equal(await context.clearCurrentDraft(selectors, 'An older version'), false);
  assert.equal(input.value, 'A private unsent draft');
  assert.equal(await context.clearCurrentDraft(selectors, 'A private unsent draft'), true);
  assert.equal(input.value, '');
  input.value = 'x'.repeat(16 * 1024 + 1);
  assert.equal(context.captureCurrentDraft(selectors).too_large, true);
  input.value = 'Draft with an unsent attachment';
  input.closest = () => ({ querySelectorAll: () => [], querySelector: () => ({}) });
  assert.equal(context.captureCurrentDraft(selectors).has_attachments, true);
  assert.equal(await context.clearCurrentDraft(selectors, input.value), false);
  assert.equal(input.value, 'Draft with an unsent attachment');
}

{
  const input = { value: 'A private unsent ChatGPT draft', offsetWidth: 1, getClientRects: () => [1], focus() {}, dispatchEvent() {} };
  const stop = { offsetWidth: 1, getClientRects: () => [1] };
  let stopVisible = true;
  context.document = { querySelectorAll(selector) {
    if (selector === '#draft') return [input];
    if (selector.includes('stop-button')) return stopVisible ? [stop] : [];
    return [];
  } };
  const selectors = { input: ['#draft'] };
  assert.equal(context.captureCurrentDraft(selectors, 'chatgpt').provider_busy, true);
  assert.equal(context.captureCurrentDraft(selectors, 'gemini').text, input.value);
  chrome.scripting.executeScript = async ({ func, args }) => [{ result: await func(...args) }];
  await assert.rejects(context.preserveAndClearDraft({}, 42, { title: 'Busy', url: 'https://chatgpt.com/' },
    { name: 'chatgpt', selectors }, { session_id: 'test' }), /ChatGPT still shows Stop/);
  assert.equal(input.value, 'A private unsent ChatGPT draft');
  stopVisible = false;
  assert.equal(context.captureCurrentDraft(selectors, 'chatgpt').text, input.value);
  stopVisible = true;
  assert.equal(await context.clearCurrentDraft(selectors, input.value, 'chatgpt'), false);
  assert.equal(input.value, 'A private unsent ChatGPT draft');
}

{
  const input = { value: 'ContextBridge-owned unsent prompt', offsetWidth: 1, getClientRects: () => [1], focus() {}, dispatchEvent() {},
    ownedJob: '', getAttribute() { return this.ownedJob; }, setAttribute(_name, value) { this.ownedJob = value; }, removeAttribute() { this.ownedJob = ''; } };
  context.document = { querySelectorAll: (selector) => selector === '#draft' ? [input] : [] };
  context.Event = class {};
  const selectors = { input: ['#draft'] };
  const stored = { ownedDrafts: {} };
  const savedDrafts = [];
  chrome.storage.local.get = async (defaults) => ({ ...defaults, ...stored });
  chrome.storage.local.set = async (value) => Object.assign(stored, value);
  chrome.scripting.executeScript = async ({ func, args }) => [{ result: await func(...args) }];
  context.fetch = async (_url, request) => { savedDrafts.push(JSON.parse(request.body)); return { ok: true }; };
  const tab = { id: 42, title: 'Test', url: 'https://chatgpt.com/c/test' };
  const job = { id: 'owned-job', session_id: 'test', prompt: input.value };
  await context.markOwnedDraft(42, tab, job);
  await assert.rejects(context.preserveAndClearDraft({ bridgeUrl: 'http://127.0.0.1:32145', token: 'test', preserveDrafts: true,
    ownedDrafts: stored.ownedDrafts }, 42, tab, { name: 'chatgpt', selectors }, job), /ownership marker is missing/);
  assert.equal(savedDrafts.length, 0);
  input.setAttribute('data-contextbridge-owned-job', job.id);
  assert.ok(stored.ownedDrafts[42].digest);
  await context.preserveAndClearDraft({ bridgeUrl: 'http://127.0.0.1:32145', token: 'test', preserveDrafts: false,
    ownedDrafts: stored.ownedDrafts }, 42, tab, { name: 'chatgpt', selectors }, job);
  assert.equal(input.value, '');
  assert.equal(input.ownedJob, '');
  assert.equal(savedDrafts.length, 0, 'an owned prompt must never enter user-draft history');
  assert.equal(stored.ownedDrafts[42], undefined);
  input.value = 'A genuinely different user draft';
  await context.markOwnedDraft(42, tab, job);
  await context.preserveAndClearDraft({ bridgeUrl: 'http://127.0.0.1:32145', token: 'test', preserveDrafts: true,
    ownedDrafts: stored.ownedDrafts }, 42, tab, { name: 'chatgpt', selectors }, job);
  assert.equal(savedDrafts.length, 1);
  assert.equal(savedDrafts[0].text, 'A genuinely different user draft');
  assert.equal(input.value, '');
}

const element = (text = '', attributes = {}) => ({
  offsetWidth: 1,
  offsetHeight: 1,
  innerText: text,
  textContent: text,
  getClientRects: () => [1],
  getAttribute: (name) => attributes[name] ?? null,
  querySelectorAll: () => [],
  querySelector: () => null,
  classList: [],
  hasAttribute: () => false
});

{
  const markdown = element('CB43-LIVE-OK');
  const response = {
    ...element('Thinking chrome outside the answer'),
    matches: (selector) => selector === 'section[data-turn="assistant"]',
    querySelectorAll: (selector) => selector === '.markdown' ? [markdown] : []
  };
  context.document = { querySelectorAll: (selector) => selector === 'section[data-turn="assistant"]' ? [response] : [] };
  const snapshot = context.captureProgress({ response: ['section[data-turn="assistant"]'] });
  assert.equal(snapshot.text, 'CB43-LIVE-OK', 'a visible ChatGPT answer inside a section must not be mistaken for thinking chrome');
  response.querySelectorAll = () => [];
  assert.equal(context.captureProgress({ response: ['section[data-turn="assistant"]'] }).text, '',
    'a ChatGPT thinking section without answer markdown is not a finished answer');
  assert.equal((source.match(/if \(!markdownParts.length\) markdownParts = boundedNodes\(element, '\.markdown'\);/g) || []).length, 2,
    'both progress sampling and final response capture must use the fallback');
}

{
  const response = element('Generating an image');
  const progress = element('95 %', { 'aria-valuenow': '95' });
  const loading = element();
  context.document = {
    querySelectorAll(selector) {
      if (selector === '#response') return [response];
      if (selector.includes('image-gen-loading-progress')) return [progress];
      if (selector === '[data-testid="image-gen-loading-state"]') return [loading];
      return [];
    }
  };
  const snapshot = context.captureProgress({ response: ['#response'] });
  assert.equal(snapshot.busy, true);
  assert.equal(snapshot.percent, 95);
  assert.equal(snapshot.detail, 'Image generation');
}

{
  const response = element('Older Gemini answer');
  const staleBusy = element();
  const stop = element();
  let generating = false;
  context.document = {
    querySelectorAll(selector) {
      if (selector === '#response') return [response];
      if (selector === '[aria-busy="true"]') return [staleBusy];
      if (selector === 'button[data-testid*="stop" i]') return generating ? [stop] : [];
      return [];
    }
  };
  assert.equal(context.captureProgress({ response: ['#response'] }).active_generation, false);
  generating = true;
  assert.equal(context.captureProgress({ response: ['#response'] }).active_generation, true);
}

{
  const failure = element('Du hast deine Höchstgrenze erreicht. Versuche es später erneut.');
  const retry = element('Erneut versuchen');
  context.document = {
    querySelectorAll(selector) {
      if (selector === '#response') return [failure];
      if (selector === 'button') return [retry];
      return [];
    }
  };
  const snapshot = context.captureProgress({ response: ['#response'] });
  assert.equal(snapshot.text, '');
  assert.equal(snapshot.detail, 'Provider error');
}

{
  const input = element();
  const response = element('Previous answer');
  const alert = element('Usage limit reached. Try again later.');
  context.document = {
    querySelectorAll(selector) {
      if (selector === '#input') return [input];
      if (selector === '#response') return [response];
      if (selector === '[role="alert"]') return [alert];
      return [];
    },
    dispatchEvent() {}
  };
  const result = await context.automate(
    { prompt: 'ignored', metadata: { contextbridge_resume_only: true, contextbridge_baseline_text: 'Previous answer' }, output: { mode: 'text' } },
    { selectors: { input: ['#input'], response: ['#response'], submit: [] } },
    new Date(Date.now() + 5000).toISOString()
  );
  assert.equal(result.ok, false);
  assert.equal(result.code, 'browser_rate_limited');
  assert.equal(result.retryable, true);
}

{
  // Captured from ChatGPT's account-level rate limit: a visible Radix dialog
  // overlays the composer and an unrelated, already completed JSON answer.
  let inputTouched = false;
  const input = { ...element(), focus() { inputTouched = true; } };
  const response = element('{"marker":"CB46-JSON-OK","count":3,"valid":true}');
  const dialog = element('Zu viele Anfragen\nDu stellst zu viele Anfragen in kurzer Zeit. Der Zugriff auf deine Unterhaltungen wurde vorübergehend eingeschränkt. Bitte warte ein paar Minuten.\nVerstanden');
  const stop = element('Stop');
  context.document = {
    querySelectorAll(selector) {
      if (selector === '#input') return [input];
      if (selector === '#response') return [response];
      if (selector.includes('[role="dialog"]')) return [dialog];
      if (selector.includes('stop-button')) return [stop];
      return [];
    }
  };
  const snapshot = context.captureProgress({ response: ['#response'] });
  assert.equal(snapshot.text, '', 'an older completed answer must not be reported through a blocking rate-limit dialog');
  assert.equal(snapshot.provider_error_code, 'browser_rate_limited');
  assert.equal(snapshot.blocking_provider_error_code, 'browser_rate_limited');
  const result = await context.automate(
    { id: 'new-job', prompt: 'This must not be sent', output: { mode: 'text' } },
    { name: 'chatgpt', selectors: { input: ['#input'], response: ['#response'], submit: [] } },
    new Date(Date.now() + 5000).toISOString()
  );
  assert.equal(result.code, 'browser_rate_limited');
  assert.equal(result.ok, false);
  assert.equal(inputTouched, false, 'the modal must stop automation before touching the composer');
  dialog.offsetWidth = 0;
  dialog.offsetHeight = 0;
  dialog.getClientRects = () => [];
  assert.equal(context.captureProgress({ response: ['#response'] }).text, response.innerText,
    'a hidden, dismissed rate-limit dialog must not block future answers');
}

{
  const previousGet = chrome.storage.local.get;
  const previousSet = chrome.storage.local.set;
  const previousTabGet = chrome.tabs.get;
  const state = { tabIds: [11, 12, 13], tabCooldowns: {} };
  chrome.storage.local.get = async (defaults) => ({ ...defaults, ...state });
  chrome.storage.local.set = async (value) => Object.assign(state, value);
  chrome.tabs.get = async (id) => ({ id, url: id === 13 ? 'https://gemini.google.com/app/test' : `https://chatgpt.com/c/${id}` });
  try {
    await context.coolDownTab(11, 300000, 'chatgpt');
    assert.ok(state.tabCooldowns[11] > Date.now());
    assert.equal(state.tabCooldowns[12], state.tabCooldowns[11], 'the same ChatGPT account must cool down across its attached tabs');
    assert.equal(state.tabCooldowns[13], undefined, 'Gemini must remain available');
  } finally {
    chrome.storage.local.get = previousGet;
    chrome.storage.local.set = previousSet;
    chrome.tabs.get = previousTabGet;
  }
}

{
  const input = element();
  const notice = element('Pro ist derzeit sehr gefragt. Für diese Antwort wurde ein anderes Modell verwendet.');
  const response = {
    ...element('Flash answer'),
    closest: (selector) => selector === '.conversation-container'
      ? { querySelector: (child) => child === 'peak-hour-fallback-disclaimer' ? notice : null }
      : null
  };
  context.document = {
    querySelectorAll(selector) {
      if (selector === '#input') return [input];
      if (selector === '#response') return [response];
      return [];
    },
    querySelector: () => null
  };
  const progress = context.captureProgress({ response: ['#response'] });
  assert.equal(progress.model_fallback, true);
  const result = await context.automate(
    { prompt: 'ignored', model: 'Pro', metadata: { contextbridge_resume_only: true, contextbridge_baseline_text: 'Previous answer' }, output: { mode: 'text' } },
    { name: 'gemini', selectors: { input: ['#input'], response: ['#response'], submit: [] } },
    new Date(Date.now() + 5000).toISOString()
  );
  assert.equal(result.ok, false);
  assert.equal(result.code, 'browser_model_unavailable');
  assert.match(result.error, /peak demand/);
}

{
  // On a resumed fresh chat, the original baseline is explicitly empty. The
  // answer may already be visible when automation resumes, but must not be
  // adopted as the baseline or it can never be completed.
  const input = element();
  const send = element();
  const markdown = element('CB45-LIVE-OK');
  const response = {
    ...element('Thinking chrome outside the answer'),
    matches: (selector) => selector === 'section[data-turn="assistant"]',
    querySelectorAll: (selector) => selector === '.markdown' ? [markdown] : []
  };
  context.document = {
    querySelectorAll(selector) {
      if (selector === '#input') return [input];
      if (selector === '#send') return [send];
      if (selector === '#response') return [response];
      return [];
    }
  };
  const result = await context.automate(
    { prompt: 'Never send this again', metadata: { contextbridge_resume_only: true, contextbridge_baseline_text: '' }, output: { mode: 'text' } },
    { name: 'chatgpt', selectors: { input: ['#input'], response: ['#response'], submit: ['#send'] } },
    new Date(Date.now() + 8000).toISOString()
  );
  assert.equal(result.ok, true, result.error);
  assert.equal(result.text, 'CB45-LIVE-OK');
}

{
  const input = { ...element(), closest: () => null };
  context.document = {
    querySelectorAll(selector) {
      return selector === '#input' ? [input] : [];
    }
  };
  const result = await context.automate(
    { prompt: 'Create an image', metadata: { contextbridge_image_tool: true }, output: { mode: 'text', artifacts: true, min_images: 1 } },
    { name: 'chatgpt', selectors: { input: ['#input'], response: [], submit: [] } },
    new Date(Date.now() + 5000).toISOString()
  );
  assert.equal(result.ok, false);
  assert.equal(result.code, 'browser_image_tool_unavailable');
}

{
  const profileMenu = element('', { 'aria-label': 'Profil-Menü öffnen' });
  const accountMenu = element('Angus Uelsmann Pro');
  const securityControl = element('High Security System');
  const pricingControl = element('Fixierung von Preismodell für PRISM aufheben');
  const geminiHistory = element('Aktivitätsverlauf in Gemini-Apps');
  context.document = {
    querySelectorAll(selector) {
      if (selector === 'button, [role="button"]') return [profileMenu, accountMenu, securityControl, pricingControl, geminiHistory];
      return [];
    }
  };
  const capabilities = context.inspectPageCapabilities();
  assert.equal(capabilities.currentModel, '');
  assert.equal(capabilities.currentReasoning, '');
}

{
  const modelControl = element('5.6 Sol', { 'data-testid': 'model-switcher-dropdown-button' });
  const reasoningControl = element('High', { 'aria-label': 'Reasoning effort' });
  context.document = {
    querySelectorAll(selector) {
      if (selector === 'button, [role="button"]') return [modelControl, reasoningControl];
      return [];
    }
  };
  const capabilities = context.inspectPageCapabilities();
  assert.equal(capabilities.currentModel, '5.6 Sol');
  assert.equal(capabilities.currentReasoning, 'High');
}

{
  // ChatGPT can show only an icon labelled "Modell wechseln" while the
  // selected full name is exposed inside its model menu.
  let menuOpen = false;
  const modelControl = {
    ...element('', { 'aria-label': 'Modell wechseln' }),
    click() { menuOpen = !menuOpen; },
    getAttribute(name) { return name === 'aria-expanded' ? String(menuOpen) : name === 'aria-label' ? 'Modell wechseln' : null; }
  };
  const selected = element('GPT-5.6 Sol\nSchnell', { 'aria-checked': 'true' });
  const other = element('GPT-5.5', { 'aria-checked': 'false' });
  const composer = element('');
  context.KeyboardEvent = class {};
  context.document = {
    querySelector: (selector) => selector.startsWith('#prompt-textarea') ? composer : null,
    querySelectorAll(selector) {
      if (selector === 'button, [role="button"]') return [modelControl];
      if (selector.includes('menuitemradio')) return menuOpen ? [selected, other] : [];
      return [];
    },
    dispatchEvent() { menuOpen = false; }
  };
  assert.equal(context.safeToDiscoverPageCapabilities(), true);
  assert.equal(context.inspectPageCapabilities().currentModel, '');
  menuOpen = true;
  assert.equal(context.inspectPageCapabilities().currentModel, 'GPT-5.6 Sol');
  menuOpen = false;
  const capabilities = await context.discoverPageCapabilities();
  assert.equal(capabilities.currentModel, 'GPT-5.6 Sol');
  assert.deepEqual(Array.from(capabilities.models), ['GPT-5.6 Sol', 'GPT-5.5']);
  assert.equal(menuOpen, false);
}

{
  // The older answer's "Modell wechseln" button must not win over the
  // current composer pill, whose menu contains the next prompt's model.
  let menuOpen = false;
  let olderAnswerClicks = 0;
  const olderAnswerControl = {
    ...element('', { 'aria-label': 'Modell wechseln' }),
    click() { olderAnswerClicks++; }
  };
  const composerPill = {
    ...element('Sehr hoch'),
    click() { menuOpen = !menuOpen; },
    getAttribute(name) { return name === 'aria-expanded' ? String(menuOpen) : null; }
  };
  const selectedModel = element('GPT-5.6 Sol\nSchnell', { 'aria-checked': 'true' });
  const otherModel = element('GPT-5.5');
  const lowEffort = element('Niedrig');
  const highEffort = element('Sehr hoch', { 'aria-checked': 'true' });
  context.document = {
    querySelector: () => null,
    querySelectorAll(selector) {
      if (selector === 'button.__composer-pill[aria-haspopup="menu"]') return [composerPill];
      if (selector === 'button, [role="button"]') return [olderAnswerControl, composerPill];
      if (selector.includes('[data-radix-collection-item]')) {
        return menuOpen ? [composerPill, selectedModel, otherModel, lowEffort, highEffort] : [composerPill];
      }
      return [];
    },
    dispatchEvent() { menuOpen = false; }
  };
  const capabilities = await context.discoverPageCapabilities();
  assert.equal(olderAnswerClicks, 0);
  assert.equal(menuOpen, false);
  assert.equal(capabilities.currentModel, 'GPT-5.6 Sol');
  assert.equal(capabilities.currentReasoning, 'Sehr hoch');
  assert.deepEqual(Array.from(capabilities.models), ['GPT-5.6 Sol', 'GPT-5.5']);
  assert.deepEqual(Array.from(capabilities.reasoningLevels), ['Niedrig', 'Sehr hoch']);
  assert.match(capabilities.scanDiagnostic.model, /composer trigger/);
  composerPill.innerText = 'Denkaufwand';
  composerPill.textContent = 'Denkaufwand';
  const genericPillCapabilities = await context.discoverPageCapabilities();
  assert.equal(genericPillCapabilities.currentModel, 'GPT-5.6 Sol');
  assert.equal(olderAnswerClicks, 0);
}

{
  // Live ChatGPT exposes an unlabeled Radix popup button beside the composer
  // plus button; neither the old answer action nor the CSS pill is present.
  let menuOpen = false;
  const plus = element('', { 'data-testid': 'composer-plus-btn', 'aria-label': 'Dateien und mehr hinzufügen' });
  plus.id = 'composer-plus-btn';
  const modelMenu = {
    ...element(''),
    click() { menuOpen = !menuOpen; },
    getAttribute(name) { return name === 'aria-expanded' ? String(menuOpen) : null; }
  };
  const form = { querySelectorAll: () => [plus, modelMenu] };
  const input = { ...element(), closest: () => form };
  const selectedModel = element('GPT-5.6 Sol', { 'aria-checked': 'true' });
  const otherModel = element('GPT-5.5');
  context.document = {
    querySelector(selector) { return selector.startsWith('#prompt-textarea') ? input : null; },
    querySelectorAll(selector) {
      if (selector === 'button.__composer-pill[aria-haspopup="menu"]') return [];
      if (selector === 'button, [role="button"]') return [plus, modelMenu];
      if (selector.includes('[data-radix-collection-item]')) return menuOpen ? [modelMenu, selectedModel, otherModel] : [modelMenu];
      return [];
    },
    dispatchEvent() { menuOpen = false; }
  };
  const capabilities = await context.discoverPageCapabilities();
  assert.equal(capabilities.currentModel, 'GPT-5.6 Sol');
  assert.deepEqual(Array.from(capabilities.models), ['GPT-5.6 Sol', 'GPT-5.5']);
  assert.match(capabilities.scanDiagnostic.model, /composer trigger/);
  assert.equal(menuOpen, false);
}

{
  // The current ChatGPT/Radix trigger opens on pointerdown. A .click() alone
  // leaves aria-expanded false and exposes no new model candidates.
  let menuOpen = false;
  let pointerPresses = 0;
  const modelMenu = {
    ...element('5.6 Sehr hoch', { 'aria-haspopup': 'menu' }),
    dispatchEvent(event) { if (event.type === 'pointerdown') { pointerPresses++; menuOpen = !menuOpen; } },
    click() { throw new Error('The pointer-opened menu must not be clicked again'); },
    getAttribute(name) { return name === 'aria-expanded' ? String(menuOpen) : name === 'aria-haspopup' ? 'menu' : null; }
  };
  const selectedModel = element('GPT-5.6 Sol', { 'aria-checked': 'true' });
  const input = { ...element(''), closest: () => ({ querySelectorAll: () => [modelMenu] }) };
  context.PointerEvent = class { constructor(type) { this.type = type; } };
  context.document = {
    querySelector(selector) { return selector.startsWith('#prompt-textarea') ? input : null; },
    querySelectorAll(selector) {
      if (selector === 'button.__composer-pill[aria-haspopup="menu"]') return [];
      if (selector === 'button, [role="button"]') return [modelMenu];
      if (selector.includes('[data-radix-collection-item]')) return menuOpen ? [modelMenu, selectedModel] : [modelMenu];
      return [];
    },
    dispatchEvent() { menuOpen = false; }
  };
  const capabilities = await context.discoverPageCapabilities();
  assert.deepEqual(Array.from(capabilities.models), ['GPT-5.6 Sol']);
  assert.equal(pointerPresses, 2);
  assert.equal(menuOpen, false);
  delete context.PointerEvent;
}

{
  // ChatGPT can put only a model/effort navigation header in the first popup;
  // the actual model choices appear in its second-level menu.
  let menuOpen = false;
  let modelSubmenuOpen = false;
  let modelChanges = 0;
  const privateDraft = 'Private unsent draft';
  const input = { ...element(privateDraft), closest: () => ({ querySelectorAll: () => [composerPill] }) };
  const composerPill = {
    ...element('Sehr hoch'),
    click() { menuOpen = !menuOpen; if (!menuOpen) modelSubmenuOpen = false; },
    getAttribute(name) { return name === 'aria-expanded' ? String(menuOpen) : name === 'aria-haspopup' ? 'menu' : null; }
  };
  const submenuHeader = {
    ...element('5.6 Sehr hoch', { 'aria-haspopup': 'menu' }),
    click() { modelSubmenuOpen = true; }
  };
  const selectedModel = { ...element('GPT-5.6 Sol', { 'aria-checked': 'true' }), click() { modelChanges++; } };
  const otherModel = { ...element('GPT-5.5'), click() { modelChanges++; } };
  const lowEffort = element('Niedrig');
  context.document = {
    querySelector(selector) { return selector.startsWith('#prompt-textarea') ? input : null; },
    querySelectorAll(selector) {
      if (selector === 'button.__composer-pill[aria-haspopup="menu"]') return [composerPill];
      if (selector === 'button, [role="button"]') return [composerPill];
      if (selector.includes('[data-radix-collection-item]')) {
        return menuOpen ? [composerPill, submenuHeader, lowEffort, ...(modelSubmenuOpen ? [selectedModel, otherModel] : [])] : [composerPill];
      }
      return [];
    },
    dispatchEvent() { menuOpen = false; modelSubmenuOpen = false; }
  };
  const capabilities = await context.discoverPageCapabilities();
  assert.equal(capabilities.currentModel, 'GPT-5.6 Sol');
  assert.deepEqual(Array.from(capabilities.models), ['GPT-5.6 Sol', 'GPT-5.5']);
  assert.match(capabilities.scanDiagnostic.model, /submenu=true/);
  assert.equal(input.innerText, privateDraft);
  assert.equal(modelChanges, 0);
  assert.equal(menuOpen, false);
}

{
  const input = element('');
  context.document = {
    querySelector(selector) {
      if (selector.startsWith('#prompt-textarea')) return input;
      return null;
    }
  };
  assert.equal(context.inspectFreshChat(), true);
  context.document.querySelector = (selector) => selector.startsWith('#prompt-textarea') ? input : element('An existing turn');
  assert.equal(context.inspectFreshChat(), false);
  assert.equal(context.isFreshChatURL('https://chatgpt.com/c/existing'), false);
  assert.equal(context.isFreshChatURL('https://gemini.google.com/app/existing'), false);
}

{
  const modelOption = (primary, secondary = '') => ({
    ...element(`${primary}\n${secondary}`),
    querySelector(selector) {
      if (selector.includes('picker-primary-text')) return element(primary);
      if (selector.includes('picker-secondary-text')) return secondary ? element(secondary) : null;
      return null;
    }
  });
  const picker = {
    ...element('Flash Erweitert', { 'aria-haspopup': 'true', 'aria-controls': 'gemini-mode-menu', 'aria-label': 'Modusauswahl öffnen, derzeit ausgewählt: Flash Erweitert' }),
    click() {}, dispatchEvent() {}
  };
  const menu = { querySelectorAll: () => [modelOption('Flash', 'Erweitert'), modelOption('Pro'), modelOption('Flash-Lite')] };
  context.KeyboardEvent = class {};
  context.document = {
    querySelector: (selector) => selector.startsWith('bard-mode-switcher') ? picker : null,
    querySelectorAll: (selector) => selector === 'button, [role="button"]' ? [picker] : [],
    getElementById: (id) => id === 'gemini-mode-menu' ? menu : null
  };
  assert.equal(context.inspectPageCapabilities().currentModel, 'Flash Erweitert');
  assert.deepEqual(Array.from(context.inspectPageCapabilities().models), ['Flash Erweitert', 'Pro', 'Flash-Lite']);
  const capabilities = await context.discoverPageCapabilities();
  assert.deepEqual(Array.from(capabilities.models), ['Flash Erweitert', 'Pro', 'Flash-Lite']);
}

{
  context.location = { hostname: 'gemini.google.com' };
  let uploadClicks = 0;
  const picker = { ...element('Gemini Flash', { 'aria-label': 'Modusauswahl öffnen, derzeit ausgewählt: Gemini Flash' }),
    className: 'gds-mode-switch-button', click() {}, dispatchEvent() {} };
  const uploads = { ...element('Uploads & Tools', { 'aria-haspopup': 'menu' }), click() { uploadClicks += 1; } };
  const menu = { ...element(''), querySelectorAll: () => [element('Flash'), element('Pro')] };
  context.document = {
    querySelector: () => null,
    querySelectorAll: (selector) => selector === 'button, [role="button"]' ? [uploads, picker]
      : selector === '[role="menu"], .cdk-overlay-pane' ? [menu] : [],
    getElementById: () => null
  };
  assert.equal(context.inspectPageCapabilities().currentModel, 'Gemini Flash');
  const capabilities = await context.discoverPageCapabilities();
  assert.equal(capabilities.currentModel, 'Gemini Flash');
  assert.deepEqual(Array.from(capabilities.models), ['Flash', 'Pro']);
  assert.equal(uploadClicks, 0, 'Gemini model discovery must never click the upload menu');
  context.document.querySelectorAll = (selector) => selector === 'button, [role="button"]' ? [uploads] : [];
  const missing = await context.discoverPageCapabilities();
  assert.equal(missing.currentModel, '');
  assert.deepEqual(Array.from(missing.models), []);
  assert.equal(uploadClicks, 0, 'without a verified picker the model must remain unknown');
  delete context.location;
}

{
  const composer = element('');
  context.document = {
    querySelector: () => composer,
    querySelectorAll: () => []
  };
  assert.equal(context.safeToDiscoverPageCapabilities(), true);
  composer.innerText = 'Unsent private draft';
  context.document.activeElement = composer;
  assert.equal(context.safeToDiscoverPageCapabilities(), false);
  context.document.hasFocus = () => false;
  assert.equal(context.safeToDiscoverPageCapabilities(), true);
  context.document.activeElement = null;
  assert.equal(context.safeToDiscoverPageCapabilities(), true);
}

{
  // An explicit ChatGPT model is checked in the current composer menu. The
  // Radix trigger and its model submenu both open on pointerdown, and an
  // already-selected model must not be clicked or the prompt sent.
  let menuOpen = false;
  let submenuOpen = false;
  let modelClicks = 0;
  const trigger = {
    ...element('Mittel', { 'aria-haspopup': 'menu' }),
    dispatchEvent(event) { if (event.type === 'pointerdown') menuOpen = !menuOpen; },
    getAttribute(name) { return name === 'aria-expanded' ? String(menuOpen) : name === 'aria-haspopup' ? 'menu' : null; },
    click() { throw new Error('Pointer-based composer trigger was clicked'); }
  };
  const submenu = {
    ...element('5.6 Mittel', { 'aria-haspopup': 'menu' }),
    dispatchEvent(event) { if (event.type === 'pointerdown') submenuOpen = true; },
    click() { throw new Error('Pointer-based model submenu was clicked'); }
  };
  const chosen = { ...element('GPT-5.6 Sol', { 'aria-checked': 'true' }), click() { modelClicks++; } };
  const input = { ...element(''), closest: () => ({ querySelectorAll: () => [trigger] }), focus() { throw new Error('Model menu path reached'); } };
  context.PointerEvent = class { constructor(type) { this.type = type; } };
  context.KeyboardEvent = class { constructor(type) { this.type = type; } };
  context.document = {
    querySelectorAll(selector) {
      if (selector === '#input') return [input];
      if (selector === 'button[aria-haspopup="menu"]') return [trigger];
      if (selector.includes('[data-radix-collection-item]')) return menuOpen ? [trigger, submenu, ...(submenuOpen ? [chosen] : [])] : [trigger];
      return [];
    },
    dispatchEvent() { menuOpen = false; submenuOpen = false; }
  };
  const result = await context.automate({ prompt: 'not sent', model: 'GPT-5.6 Sol', output: { mode: 'text' } },
    { name: 'chatgpt', selectors: { input: ['#input'], response: [], submit: [] } },
    new Date(Date.now() + 12000).toISOString());
  assert.equal(result.error, 'Model menu path reached');
  assert.equal(modelClicks, 0);
  const unavailable = await context.automate({ prompt: 'must not send', model: 'GPT-5.5', output: { mode: 'text' } },
    { name: 'chatgpt', selectors: { input: ['#input'], response: [], submit: [] } },
    new Date(Date.now() + 12000).toISOString());
  assert.match(unavailable.error, /Requested model "GPT-5.5" is not available/);
  assert.equal(modelClicks, 0);
  const fallback = await context.automate({ prompt: 'not sent', model: 'GPT-5.5',
    metadata: { contextbridge_model_fallbacks: ['GPT-5.6 Sol'] }, output: { mode: 'text' } },
  { name: 'chatgpt', selectors: { input: ['#input'], response: [], submit: [] } },
  new Date(Date.now() + 12000).toISOString());
  assert.equal(fallback.error, 'Model menu path reached');
  assert.equal(modelClicks, 0);
  delete context.PointerEvent;
}

{
  // ChatGPT can render the input outside its composer form. A GPT-5.5 label
  // in an older answer must not short-circuit a request to change this form.
  let menuOpen = false;
  let switched = false;
  const trigger = {
    ...element('Mittel', { 'aria-haspopup': 'menu' }),
    dispatchEvent(event) { if (event.type === 'pointerdown') menuOpen = !menuOpen; },
    getAttribute(name) { return name === 'aria-expanded' ? String(menuOpen) : name === 'aria-haspopup' ? 'menu' : null; }
  };
  const selected = element('GPT-5.6 Sol', { 'aria-checked': 'true' });
  const target = { ...element('GPT-5.5', { 'aria-checked': 'false' }), click() { switched = true; } };
  const oldAnswerModel = element('GPT-5.5');
  const unrelatedMenu = { ...element('Share', { 'aria-haspopup': 'menu' }), dispatchEvent() { throw new Error('Unrelated menu opened'); }, click() { throw new Error('Unrelated menu clicked'); } };
  const composer = { querySelectorAll: () => [unrelatedMenu, trigger] };
  const input = { ...element(''), closest: () => null, focus() { throw new Error('Switched composer was used'); } };
  context.PointerEvent = class { constructor(type) { this.type = type; } };
  context.document = {
    querySelector(selector) { return selector === 'form' ? composer : null; },
    querySelectorAll(selector) {
      if (selector === '#input') return [input];
      if (selector === 'button.__composer-pill[aria-haspopup="menu"]') return [trigger];
      if (selector === 'button[aria-haspopup="menu"]') return [trigger, oldAnswerModel];
      if (selector.includes('[data-radix-collection-item]')) return menuOpen ? [trigger, selected, target] : [trigger];
      return [];
    },
    dispatchEvent() { menuOpen = false; }
  };
  const result = await context.automate({ prompt: 'not sent', model: 'GPT-5.5', output: { mode: 'text' } },
    { name: 'chatgpt', selectors: { input: ['#input'], response: [], submit: [] } },
    new Date(Date.now() + 12000).toISOString());
  assert.equal(switched, true);
  assert.equal(result.error, 'Switched composer was used');
  delete context.PointerEvent;
}

{
  // A capability scan may leave the composer Radix menu open briefly. The
  // selector must use its existing options without toggling it closed.
  let switched = false;
  const trigger = {
    ...element('Mittel', { 'aria-haspopup': 'menu', 'aria-expanded': 'true' }),
    dispatchEvent() { throw new Error('Already-open menu was toggled'); },
    click() { throw new Error('Already-open menu was clicked'); }
  };
  const target = { ...element('GPT-5.5', { 'aria-checked': 'false' }), click() { switched = true; } };
  const input = { ...element(''), closest: () => ({ querySelectorAll: () => [trigger] }), focus() { throw new Error('Model choice was clicked'); } };
  context.document = {
    querySelectorAll(selector) {
      if (selector === '#input') return [input];
      if (selector === 'button[aria-haspopup="menu"]') return [trigger];
      if (selector.includes('[data-radix-collection-item]')) return [trigger, target];
      return [];
    }
  };
  const result = await context.automate({ prompt: 'not sent', model: 'GPT-5.5', output: { mode: 'text' } },
    { name: 'chatgpt', selectors: { input: ['#input'], response: [], submit: [] } },
    new Date(Date.now() + 12000).toISOString());
  assert.equal(switched, true);
  assert.equal(result.error, 'Model choice was clicked');
}

{
  let selectedPro = false;
  const picker = {
    ...element('Flash Erweitert', { 'aria-haspopup': 'true', 'aria-controls': 'gemini-mode-menu', 'aria-label': 'Modusauswahl öffnen, derzeit ausgewählt: Flash Erweitert' }),
    closest: () => ({}), click() {},
    getAttribute(name) {
      if (name === 'aria-label') return `Modusauswahl öffnen, derzeit ausgewählt: ${selectedPro ? 'Pro Erweitert' : 'Flash Erweitert'}`;
      return name === 'aria-controls' ? 'gemini-mode-menu' : name === 'aria-haspopup' ? 'true' : null;
    }
  };
  const account = { ...element('Angus Pro', { 'aria-haspopup': 'menu' }), closest: () => null };
  const pro = {
    ...element('Pro\nSuitable for complex tasks'),
    querySelector(selector) { return selector.includes('picker-primary-text') ? element('Pro') : null; },
    click() { selectedPro = true; }
  };
  const flash = { ...element('Flash\nErweitert'), querySelector: () => element('Flash'), click() {} };
  const oldInput = { ...element(), focus() { throw new Error('Stale composer was used'); } };
  const newInput = { ...element(), focus() { throw new Error('Fresh composer was used'); } };
  context.document = {
    querySelector(selector) { return selector === 'bard-mode-switcher button[aria-haspopup]' ? picker : null; },
    querySelectorAll(selector) {
      if (selector === '#input') return [selectedPro ? newInput : oldInput];
      if (selector === 'bard-mode-switcher button[aria-haspopup]') return [picker];
      if (selector === 'button[aria-haspopup="menu"]') return [account];
      return [];
    },
    getElementById: () => ({ querySelectorAll: () => [flash, pro] }),
    dispatchEvent() {}
  };
  const result = await context.automate({ prompt: 'test', model: 'Pro', output: { mode: 'text' } },
    { name: 'gemini', selectors: { input: ['#input'], response: ['#response'], submit: [] } },
    new Date(Date.now() + 5000).toISOString());
  assert.equal(selectedPro, true);
  assert.equal(result.error, 'Fresh composer was used');
}

{
  let proClicked = false;
  let sendClicked = false;
  const picker = {
    ...element('Flash Erweitert', { 'aria-haspopup': 'true', 'aria-controls': 'gemini-mode-menu' }),
    closest: () => ({}), click() {},
    getAttribute(name) {
      if (name === 'aria-label') return 'Modusauswahl öffnen, derzeit ausgewählt: Flash Erweitert';
      return name === 'aria-controls' ? 'gemini-mode-menu' : name === 'aria-haspopup' ? 'true' : null;
    }
  };
  const pro = { ...element('3.1 Pro'), querySelector(selector) { return selector.includes('picker-primary-text') ? element('3.1 Pro') : null; }, click() { proClicked = true; } };
  const input = element('');
  const send = { ...element(''), click() { sendClicked = true; } };
  context.document = {
    querySelector(selector) { return selector === 'bard-mode-switcher button[aria-haspopup]' ? picker : null; },
    querySelectorAll(selector) {
      if (selector === '#input') return [input];
      if (selector === '#send') return [send];
      if (selector === 'bard-mode-switcher button[aria-haspopup]') return [picker];
      return [];
    },
    getElementById: () => ({ querySelectorAll: () => [pro] }),
    dispatchEvent() {}
  };
  const result = await context.automate({ prompt: 'must not send with Flash', model: '3.1 Pro', output: { mode: 'text' } },
    { name: 'gemini', selectors: { input: ['#input'], response: [], submit: ['#send'] } },
    new Date(Date.now() + 5000).toISOString());
  assert.equal(proClicked, true);
  assert.equal(sendClicked, false);
  assert.equal(result.code, 'browser_model_unavailable');
}

{
  let mode = 'Pro Erweitert';
  let sent = false;
  class TextArea {
    constructor() { this.value = ''; this.offsetWidth = 1; this.offsetHeight = 1; }
    getClientRects() { return [1]; }
    focus() {}
    dispatchEvent() {}
  }
  context.HTMLTextAreaElement = TextArea;
  context.HTMLInputElement = class {};
  context.InputEvent = class { constructor(type) { this.type = type; } };
  context.Event = class {};
  const input = new TextArea();
  const picker = {
    ...element('Pro Erweitert', { 'aria-haspopup': 'true' }), closest: () => ({}),
    getAttribute(name) { return name === 'aria-label' ? `Modusauswahl öffnen, derzeit ausgewählt: ${mode}` : name === 'aria-haspopup' ? 'true' : null; }
  };
  const send = { ...element(''), click() { sent = true; mode = 'Flash Erweitert'; } };
  const response = element('Wrong-model response');
  context.document = {
    querySelector(selector) { return selector === 'bard-mode-switcher button[aria-haspopup]' ? picker : null; },
    querySelectorAll(selector) {
      if (selector === '#input') return [input];
      if (selector === '#send') return [send];
      if (selector === '#response') return sent ? [response] : [];
      if (selector === 'bard-mode-switcher button[aria-haspopup]') return [picker];
      return [];
    }
  };
  const result = await context.automate({ prompt: 'short test', model: 'Pro Erweitert', output: { mode: 'text' } },
    { name: 'gemini', selectors: { input: ['#input'], response: ['#response'], submit: ['#send'] } },
    new Date(Date.now() + 12000).toISOString());
  assert.equal(sent, true);
  assert.equal(result.ok, false);
  assert.equal(result.code, 'browser_model_unavailable');
}

{
  let sent = false;
  class TextArea {
    constructor() { this.value = ''; this.offsetWidth = 1; this.offsetHeight = 1; }
    getClientRects() { return [1]; }
    focus() {}
    dispatchEvent() {}
  }
  context.HTMLTextAreaElement = TextArea;
  context.HTMLInputElement = class {};
  context.InputEvent = class {};
  context.Event = class {};
  const input = new TextArea();
  const picker = {
    ...element('Pro Erweitert', { 'aria-haspopup': 'true' }), closest: () => ({}),
    getAttribute(name) { return name === 'aria-label' ? 'Modusauswahl öffnen, derzeit ausgewählt: Pro Erweitert' : name === 'aria-haspopup' ? 'true' : null; }
  };
  const send = { ...element(''), click() { sent = true; input.value = ''; } };
  const response = element('Completed Gemini answer');
  context.document = {
    querySelector(selector) { return selector === 'bard-mode-switcher button[aria-haspopup]' ? picker : null; },
    querySelectorAll(selector) {
      if (selector === '#input') return [input];
      if (selector === '#send') return sent ? [] : [send];
      if (selector === '#response') return sent ? [response] : [];
      if (selector === 'bard-mode-switcher button[aria-haspopup]') return [picker];
      return [];
    }
  };
  const result = await context.automate({ prompt: 'short test', model: 'Pro Erweitert', output: { mode: 'text' } },
    { name: 'gemini', selectors: { input: ['#input'], response: ['#response'], submit: ['#send'] } },
    new Date(Date.now() + 9000).toISOString());
  assert.equal(sent, true);
  assert.equal(result.ok, true);
  assert.equal(result.text, 'Completed Gemini answer');
  assert.equal(result.selected_model, 'Pro Erweitert');
}

{
  let sent = false;
  class TextArea {
    constructor() { this.value = ''; this.offsetWidth = 1; this.offsetHeight = 1; }
    getClientRects() { return [1]; }
    focus() {}
    dispatchEvent() {}
  }
  context.HTMLTextAreaElement = TextArea;
  context.HTMLInputElement = class {};
  context.InputEvent = class {};
  context.Event = class {};
  const input = new TextArea();
  const send = { ...element(''), click() { sent = true; input.value = ''; } };
  const retry = element('Erneut versuchen');
  const banner = element('Etwas ist schiefgelaufen. Bitte versuche es erneut.');
  const failedUserTurn = {
    ...element(),
    querySelector(selector) {
      if (selector === 'button[data-testid="regenerate-thread-error-button"]') return retry;
      if (selector.includes('text-orange-600')) return banner;
      return null;
    }
  };
  const oldAssistant = element('An earlier answer about rate limits');
  context.document = {
    querySelectorAll(selector) {
      if (selector === '#input') return [input];
      if (selector === '#send') return sent ? [] : [send];
      if (selector === '#response') return [oldAssistant];
      if (selector === '[data-turn="user"]') return sent ? [element(), failedUserTurn] : [element()];
      if (selector === 'button') return sent ? [retry] : [];
      return [];
    }
  };
  const result = await context.automate(
    { prompt: 'test', output: { mode: 'text' } },
    { name: 'chatgpt', selectors: { input: ['#input'], response: ['#response'], submit: ['#send'] } },
    new Date(Date.now() + 9000).toISOString()
  );
  assert.equal(result.ok, false);
  assert.equal(result.code, 'browser_provider_error');
  assert.match(result.error, /Etwas ist schiefgelaufen/);
}

{
  let sent = false;
  let fakeNow = Date.now();
  const realDate = context.Date;
  const realSetTimeout = context.setTimeout;
  context.Date = class extends Date { static now() { return fakeNow; } };
  context.setTimeout = (callback, milliseconds) => realSetTimeout(() => { fakeNow += milliseconds; callback(); }, 1);
  class TextArea {
    constructor() { this.value = ''; this.offsetWidth = 1; this.offsetHeight = 1; }
    getClientRects() { return [1]; }
    focus() {}
    dispatchEvent() {}
  }
  context.HTMLTextAreaElement = TextArea;
  context.HTMLInputElement = class {};
  context.InputEvent = class {};
  context.Event = class {};
  const input = new TextArea();
  const send = { ...element(''), click() { sent = true; input.value = ''; } };
  const response = element('Gemini answer with stale busy flag');
  const staleBusy = element();
  context.document = {
    querySelectorAll(selector) {
      if (selector === '#input') return [input];
      if (selector === '#send') return sent ? [] : [send];
      if (selector === '#response') return sent ? [response] : [];
      if (selector === '[aria-busy="true"]') return sent ? [staleBusy] : [];
      return [];
    }
  };
  try {
    const result = await context.automate(
      { prompt: 'test', output: { mode: 'text' } },
      { name: 'gemini', selectors: { input: ['#input'], response: ['#response'], submit: ['#send'] } },
      new Date(Date.now() + 60000).toISOString()
    );
    assert.equal(result.ok, true, JSON.stringify(result));
    assert.equal(result.text, 'Gemini answer with stale busy flag');
    assert.ok(fakeNow - Date.now() >= 12000);
  } finally {
    context.Date = realDate;
    context.setTimeout = realSetTimeout;
  }
}

{
  let musicSelected = true;
  const removeMusic = { ...element('', { 'aria-label': 'Auswahl von „Musik“ aufheben' }), click() { musicSelected = false; } };
  const composer = { querySelectorAll: () => musicSelected ? [removeMusic] : [] };
  const oldInput = { ...element(), closest: () => composer, focus() { throw new Error('Stale music composer was used'); } };
  const newInput = { ...element(), closest: () => composer, focus() { throw new Error('Fresh text composer was used'); } };
  context.document = {
    querySelectorAll(selector) { return selector === '#input' ? [musicSelected ? oldInput : newInput] : []; },
    dispatchEvent() {}
  };
  const result = await context.automate({ prompt: 'plain text', output: { mode: 'text' } },
    { name: 'gemini', selectors: { input: ['#input'], response: [], submit: [] } },
    new Date(Date.now() + 5000).toISOString());
  assert.equal(musicSelected, false);
  assert.equal(result.error, 'Fresh text composer was used');
}

{
  let menuOpened = false;
  let musicSelected = false;
  let menuChecks = 0;
  const composer = { querySelectorAll: () => [], querySelector: () => musicSelected ? element('', { 'aria-label': 'Auswahl von „Musik“ aufheben' }) : null };
  const input = { ...element(), closest: () => composer, focus() { throw new Error('Selected music composer was used'); } };
  const trigger = { ...element('Uploads & Tools', { 'aria-expanded': 'false' }), click() { menuOpened = true; } };
  const choice = { ...element('Musik erstellen', { 'aria-checked': 'false' }), click() { musicSelected = true; } };
  context.document = {
    querySelectorAll(selector) {
      if (selector === '#input') return [input];
      if (selector === 'button[aria-label*="Uploads & Tools" i]') return [trigger];
      if (selector === 'button[role="menuitemcheckbox"], [role="menuitem"], [role="option"]') return menuOpened && ++menuChecks >= 3 ? [choice] : [];
      return [];
    }
  };
  const result = await context.automate(
    { prompt: 'create music', metadata: { contextbridge_music_tool: true }, output: { mode: 'text', artifacts: true, min_media: 1 } },
    { name: 'gemini', selectors: { input: ['#input'], response: [], submit: [] } },
    new Date(Date.now() + 5000).toISOString()
  );
  assert.equal(menuOpened, true);
  assert.ok(menuChecks >= 3);
  assert.equal(musicSelected, true);
  assert.equal(result.error, 'Selected music composer was used');
}

{
  let triggerClicks = 0;
  let musicSelected = false;
  const composer = { querySelector: () => musicSelected ? element('', { 'aria-label': 'Auswahl von „Musik“ aufheben' }) : null,
    querySelectorAll: () => [] };
  const input = { ...element(), closest: () => composer, focus() { throw new Error('Open-menu music composer was used'); } };
  const trigger = { ...element('Uploads & Tools', { 'aria-expanded': 'true' }), click() { triggerClicks++; } };
  const choice = { ...element('Musik erstellen', { 'aria-checked': 'false' }), click() { musicSelected = true; } };
  context.document = {
    querySelectorAll(selector) {
      if (selector === '#input') return [input];
      if (selector === 'button[aria-label*="Uploads & Tools" i]') return [trigger];
      if (selector === 'button[role="menuitemcheckbox"], [role="menuitem"], [role="option"]') return [choice];
      return [];
    }
  };
  const result = await context.automate(
    { prompt: 'create music', metadata: { contextbridge_music_tool: true }, output: { mode: 'text', artifacts: true, min_media: 1 } },
    { name: 'gemini', selectors: { input: ['#input'], response: [], submit: [] } },
    new Date(Date.now() + 5000).toISOString()
  );
  assert.equal(triggerClicks, 0, 'an already-open menu must not be toggled closed');
  assert.equal(musicSelected, true);
  assert.equal(result.error, 'Open-menu music composer was used');
}

{
  context.atob = (value) => Buffer.from(value, 'base64').toString('binary');
  context.File = class { constructor(parts, name, options) { this.parts = parts; this.name = name; this.type = options.type; } };
  context.DataTransfer = class {
    constructor() { this.files = []; this.items = { add: (file) => { this.files.push(file); } }; }
  };
  context.Event = class { constructor(type) { this.type = type; } };
  let menuOpen = false;
  let triggerVisible = true;
  let triggerClicks = 0;
  let preview = false;
  const uploaded = [];
  const composer = { innerText: '', querySelector: () => null,
    querySelectorAll: (selector) => selector.startsWith('img,') && preview ? [element()] : [] };
  const input = { ...element(), closest: () => composer, focus() { throw new Error('Upload completed before prompt entry'); } };
  const trigger = { ...element('Uploads & Tools'), click() { menuOpen = true; triggerClicks += 1; },
    getAttribute: (name) => name === 'aria-expanded' ? String(menuOpen) : (name === 'aria-label' ? 'Uploads & Tools' : null) };
  const field = { type: 'file', accept: '.txt,.pdf,.doc', files: [], dispatchEvent(event) {
    uploaded.push(event.type); if (event.type === 'change') preview = true;
  } };
  context.document = {
    querySelectorAll(selector) {
      if (selector === '#input') return [input];
      if (selector === 'input[type="file"]') return menuOpen ? [field] : [];
      if (selector === 'button') return triggerVisible ? [trigger] : [];
      return [];
    }
  };
  const job = { prompt: 'Describe the image', image_base64: Buffer.from([137, 80, 78, 71]).toString('base64'),
    image_media_type: 'image/png', output: { mode: 'text' } };
  const profile = { name: 'gemini', selectors: { input: ['#input'], file_input: ['input[type="file"]'], response: [], submit: [] } };
  const firstUpload = await context.automate(job, profile, new Date(Date.now() + 5000).toISOString());
  assert.equal(firstUpload.error, 'Upload completed before prompt entry');
  assert.equal(triggerClicks, 1, 'Gemini upload opens its own menu when the file field is not mounted');
  assert.equal(field.files[0].name, 'contextbridge-image.png');
  assert.deepEqual(uploaded, ['input', 'change']);
  field.files = [];
  preview = false;
  triggerVisible = false;
  uploaded.length = 0;
  const secondUpload = await context.automate(job, profile, new Date(Date.now() + 5000).toISOString());
  assert.equal(secondUpload.error, 'Upload completed before prompt entry');
  assert.equal(triggerClicks, 1, 'a mounted upload field must work even if its menu trigger is not currently found');
  field.files = [{ name: 'user-attachment.png' }];
  const existing = await context.automate(job, profile, new Date(Date.now() + 5000).toISOString());
  assert.match(existing.error, /unsent attachment is already present/);
  assert.equal(existing.code, 'browser_composer_busy');
  assert.deepEqual(uploaded, ['input', 'change'], 'an existing attachment must not be replaced');
}

for (const [kind, showPreview, expectedCode] of [
  ['image', false, 'browser_upload_unavailable'],
  ['image', true, 'browser_automation_error'],
  ['file', false, 'browser_upload_unavailable']
]) {
  const realDate = context.Date;
  const realSetTimeout = context.setTimeout;
  let fakeNow = Date.now();
  let preview = false;
  context.Date = class extends Date { static now() { return fakeNow; } };
  context.setTimeout = (callback, milliseconds) => realSetTimeout(() => { fakeNow += milliseconds; callback(); }, 1);
  const composer = {
    innerText: kind === 'image' ? 'Describe contextbridge-image.png' : 'Read demo.txt', querySelector: () => null,
    querySelectorAll: (selector) => selector.startsWith('img,') && preview ? [element('', { alt: kind === 'image' ? 'contextbridge-image.png' : 'demo.txt' })] : []
  };
  const input = { ...element(), closest: () => composer, focus() { throw new Error('Attachment proof passed before prompt entry'); } };
  const field = { type: 'file', accept: '*/*', files: [], dispatchEvent(event) {
    if (event.type === 'change' && showPreview) preview = true;
  } };
  context.document = { querySelectorAll(selector) {
    if (selector === '#input') return [input];
    if (selector === 'input[type="file"]') return [field];
    return [];
  } };
  const job = kind === 'image'
    ? { prompt: 'Describe', image_base64: Buffer.from([137, 80, 78, 71]).toString('base64'), image_media_type: 'image/png', output: { mode: 'text' } }
    : { prompt: 'Read', metadata: { contextbridge_input_file: { name: 'demo.txt', media_type: 'text/plain',
      data_base64: Buffer.from('demo').toString('base64') } }, output: { mode: 'text' } };
  try {
    const result = await context.automate(job,
      { name: 'chatgpt', selectors: { input: ['#input'], file_input: ['input[type="file"]'], response: [], submit: [] } },
      new Date(fakeNow + 3000).toISOString());
    assert.equal(result.code, expectedCode);
    if (showPreview) assert.equal(result.error, 'Attachment proof passed before prompt entry');
    else assert.match(result.error, /did not show the uploaded (?:image|file)/i);
  } finally {
    context.Date = realDate;
    context.setTimeout = realSetTimeout;
  }
}

for (const kind of ['image', 'file']) {
  const realDate = context.Date;
  const realSetTimeout = context.setTimeout;
  let fakeNow = Date.now();
  let preview = false;
  let sent = false;
  context.Date = class extends Date { static now() { return fakeNow; } };
  context.setTimeout = (callback, milliseconds) => realSetTimeout(() => { fakeNow += milliseconds; callback(); }, 1);
  class TextArea {
    constructor() { this.value = ''; this.offsetWidth = 1; this.offsetHeight = 1; }
    getClientRects() { return [1]; }
    focus() {}
    dispatchEvent() {}
    setAttribute() {}
  }
  context.HTMLTextAreaElement = TextArea;
  context.HTMLInputElement = class {};
  context.InputEvent = class {};
  const input = new TextArea();
  const expectedName = kind === 'image' ? 'contextbridge-image.png' : 'demo.txt';
  const previewNode = element('', { alt: expectedName });
  const composer = { querySelector: () => null,
    querySelectorAll: (selector) => selector.startsWith('img,') && preview ? [previewNode] : [] };
  input.closest = () => composer;
  const field = { type: 'file', accept: '*/*', files: [], dispatchEvent(event) {
    if (event.type === 'change') preview = true;
  } };
  const send = { ...element('', { 'aria-label': 'Send message' }), click() { sent = true; input.value = ''; } };
  const answer = element(`${kind} accepted`);
  context.document = { querySelectorAll(selector) {
    if (selector === '#input') return [input];
    if (selector === '#send') return [send];
    if (selector === '#response') return sent ? [answer] : [];
    if (selector === 'input[type="file"]') return [field];
    if (selector === '[data-turn="user"]') return [];
    return [];
  } };
  const job = kind === 'image'
    ? { id: 'chatgpt-image-success', prompt: 'Describe', image_base64: Buffer.from([137, 80, 78, 71]).toString('base64'),
      image_media_type: 'image/png', output: { mode: 'text' } }
    : { id: 'chatgpt-file-success', prompt: 'Read', metadata: { contextbridge_input_file: { name: 'demo.txt',
      media_type: 'text/plain', data_base64: Buffer.from('demo').toString('base64') } }, output: { mode: 'text' } };
  try {
    const result = await context.automate(job,
      { name: 'chatgpt', selectors: { input: ['#input'], file_input: ['input[type="file"]'], response: ['#response'], submit: ['#send'] } },
      new Date(fakeNow + 30000).toISOString());
    assert.equal(sent, true, `the verified ChatGPT ${kind} must reach Send`);
    assert.equal(result.ok, true, JSON.stringify(result));
    assert.equal(result.text, `${kind} accepted`);
  } finally {
    context.Date = realDate;
    context.setTimeout = realSetTimeout;
  }
}

{
  // Providers can remount the composer after a file input change. A preview
  // that existed immediately after upload is not enough if it disappears
  // before Send becomes actionable.
  const realDate = context.Date;
  const realSetTimeout = context.setTimeout;
  let fakeNow = Date.now();
  let preview = false;
  let sent = false;
  context.Date = class extends Date { static now() { return fakeNow; } };
  context.setTimeout = (callback, milliseconds) => realSetTimeout(() => { fakeNow += milliseconds; callback(); }, 1);
  class TextArea {
    constructor() { this.value = ''; this.offsetWidth = 1; this.offsetHeight = 1; }
    getClientRects() { return [1]; }
    focus() {}
    setAttribute() {}
    dispatchEvent(event) { if (event.type === 'input') preview = false; }
  }
  context.HTMLTextAreaElement = TextArea;
  context.HTMLInputElement = class {};
  context.InputEvent = class { constructor(type) { this.type = type; } };
  const input = new TextArea();
  const previewNode = element('', { alt: 'contextbridge-image.png' });
  const composer = { querySelector: () => null,
    querySelectorAll: (selector) => selector.startsWith('img,') && preview ? [previewNode] : [] };
  input.closest = () => composer;
  const field = { type: 'file', accept: 'image/*', files: [], dispatchEvent(event) {
    if (event.type === 'change') preview = true;
  } };
  const send = { ...element('', { 'aria-label': 'Send message' }), click() { sent = true; } };
  context.document = { querySelectorAll(selector) {
    if (selector === '#input') return [input];
    if (selector === '#send') return [send];
    if (selector === 'input[type="file"]') return [field];
    return [];
  } };
  try {
    const result = await context.automate({ id: 'upload-remounted-away', prompt: 'Describe',
      image_base64: Buffer.from([137, 80, 78, 71]).toString('base64'), image_media_type: 'image/png', output: { mode: 'text' } },
    { name: 'chatgpt', selectors: { input: ['#input'], file_input: ['input[type="file"]'], response: [], submit: ['#send'] } },
    new Date(fakeNow + 4000).toISOString());
    assert.equal(result.code, 'browser_upload_unavailable');
    assert.match(result.error, /no longer shows the uploaded image/i);
    assert.equal(sent, false, 'a disappeared upload preview must stop before Send');
  } finally {
    context.Date = realDate;
    context.setTimeout = realSetTimeout;
  }
}

{
  // An image picker can change the page and produce an unrelated answer.
  // Without a new user turn containing the actual prompt, that answer is not
  // proof that the image and text were submitted together.
  const originalDate = context.Date;
  let clockOffset = 0;
  let sent = false;
  let preview = false;
  let userTurnReads = 0;
  context.Date = class extends Date { static now() { return Date.now() + clockOffset; } };
  context.HTMLTextAreaElement = class {
    constructor() { this.value = ''; this.offsetWidth = 1; this.offsetHeight = 1; }
    getClientRects() { return [1]; }
    focus() {}
    dispatchEvent() {}
    setAttribute() {}
  };
  context.HTMLInputElement = class {};
  context.InputEvent = class {};
  const composer = {
    innerText: '',
    querySelector: () => null,
    querySelectorAll(selector) { return selector.startsWith('img,') && preview ? [element()] : []; }
  };
  const input = new context.HTMLTextAreaElement();
  input.closest = () => composer;
  const field = { type: 'file', accept: '.txt,.pdf', files: [], dispatchEvent(event) {
    if (event.type === 'change') preview = true;
  } };
  const send = { ...element('', { 'aria-label': 'Nachricht senden' }), click() { sent = true; } };
  const unrelated = element('Clean typography, minimal layout, and a strong personal branding connection to angusu.de.');
  context.document = {
    querySelectorAll(selector) {
      if (selector === '#input') return [input];
      if (selector === '#send') return [send];
      if (selector === '#response') return sent ? [unrelated] : [];
      if (selector === 'input[type="file"]') return [field];
      if (selector === 'user-query') {
        userTurnReads += 1;
        if (sent && userTurnReads > 1) clockOffset = 16000;
        return [];
      }
      return [];
    }
  };
  try {
    const result = await context.automate({ id: 'image-and-text', prompt: 'Read this image, then identify the owner of angusu.de.',
      image_base64: Buffer.from([137, 80, 78, 71]).toString('base64'), image_media_type: 'image/png', output: { mode: 'text' } },
    { name: 'gemini', selectors: { input: ['#input'], file_input: ['input[type="file"]'], response: ['#response'], submit: ['#send'] } },
    new Date(Date.now() + 60000).toISOString());
    assert.equal(sent, true);
    assert.equal(result.ok, false, 'an unrelated answer must never complete the image job');
    assert.equal(result.code, 'browser_submit_unavailable');
  } finally {
    context.Date = originalDate;
  }
}

{
  let sent = false;
  let preview = false;
  let fakeNow = Date.now();
  const realDate = context.Date;
  const realSetTimeout = context.setTimeout;
  context.Date = class extends Date { static now() { return fakeNow; } };
  context.setTimeout = (callback, milliseconds) => realSetTimeout(() => { fakeNow += milliseconds; callback(); }, 1);
  class TextArea {
    constructor() { this.value = ''; this.offsetWidth = 1; this.offsetHeight = 1; }
    getClientRects() { return [1]; }
    focus() {}
    dispatchEvent() {}
    setAttribute() {}
  }
  context.HTMLTextAreaElement = TextArea;
  context.HTMLInputElement = class {};
  context.InputEvent = class {};
  const prompt = 'Read the two lines in this image.';
  const input = new TextArea();
  const composer = { innerText: '', querySelector: () => null,
    querySelectorAll: (selector) => selector.startsWith('img,') && preview ? [element()] : [] };
  input.closest = () => composer;
  const field = { type: 'file', accept: 'image/*', files: [], dispatchEvent(event) {
    if (event.type === 'change') preview = true;
  } };
  const send = { ...element('', { 'aria-label': 'Nachricht senden' }), click() { sent = true; input.value = ''; } };
  const answer = element('IamAngusU\nContextBridge');
  const unrelated = element('Unrelated earlier answer');
  const content = { id: 'user-query-content-2', textContent: prompt };
  const scope = { querySelectorAll: (selector) => selector === 'model-response' ? [answer, unrelated] : [] };
  const turn = { querySelector: () => content, closest: () => scope,
    compareDocumentPosition: (candidate) => candidate === answer ? 4 : 2 };
  context.document = {
    querySelectorAll(selector) {
      if (selector === '#input') return [input];
      if (selector === '#send') return sent ? [] : [send];
      if (selector === 'input[type="file"]') return [field];
      if (selector === 'user-query') return sent ? [turn] : [];
      if (selector === '#response') return sent ? [answer, unrelated] : [];
      return [];
    }
  };
  try {
    const result = await context.automate({ id: 'paired-image-and-prompt', prompt,
      image_base64: Buffer.from([137, 80, 78, 71]).toString('base64'), image_media_type: 'image/png', output: { mode: 'text' } },
    { name: 'gemini', selectors: { input: ['#input'], file_input: ['input[type="file"]'], response: ['#response'], submit: ['#send'] } },
    new Date(Date.now() + 60000).toISOString());
    assert.equal(result.ok, true);
    assert.equal(result.text, 'IamAngusU\nContextBridge', 'the answer must follow the verified prompt, not an unrelated response');
    assert.equal(result.submitted_prompt_verified, true, 'a paired Gemini image response is its own submission proof');
  } finally {
    context.Date = realDate;
    context.setTimeout = realSetTimeout;
  }
}

for (const [renderedUserText, shouldPass] of [['', true], ['A different visible user prompt', false]]) {
  // Gemini can render an uploaded image turn with no readable prompt text.
  // The fallback is limited to an exact retained composer draft, confirmed
  // attachment preview, one clicked Send, and one new paired user turn.
  let sent = false;
  let preview = false;
  let fakeNow = Date.now();
  const realDate = context.Date;
  const realSetTimeout = context.setTimeout;
  context.Date = class extends Date { static now() { return fakeNow; } };
  context.setTimeout = (callback, milliseconds) => realSetTimeout(() => { fakeNow += milliseconds; callback(); }, 1);
  class TextArea {
    constructor() { this.value = ''; this.offsetWidth = 1; this.offsetHeight = 1; }
    getClientRects() { return [1]; }
    focus() {}
    dispatchEvent() {}
    setAttribute() {}
  }
  context.HTMLTextAreaElement = TextArea;
  context.HTMLInputElement = class {};
  context.InputEvent = class {};
  const input = new TextArea();
  const composer = { innerText: '', querySelector: () => null,
    querySelectorAll: (selector) => selector.startsWith('img,') && preview ? [element()] : [] };
  input.closest = () => composer;
  const field = { type: 'file', accept: 'image/*', files: [], dispatchEvent(event) {
    if (event.type === 'change') preview = true;
  } };
  const send = { ...element('', { 'aria-label': 'Nachricht senden' }), click() { sent = true; input.value = ''; } };
  const answer = element('IamAngusU\nContextBridge');
  const scope = { querySelectorAll: (selector) => selector === 'model-response' ? [answer] : [] };
  const turn = { textContent: renderedUserText, querySelector: () => null, closest: () => scope,
    compareDocumentPosition: () => 4 };
  context.document = {
    querySelectorAll(selector) {
      if (selector === '#input') return [input];
      if (selector === '#send') return sent ? [] : [send];
      if (selector === 'input[type="file"]') return [field];
      if (selector === 'user-query') return sent ? [turn] : [];
      if (selector === '#response') return sent ? [answer] : [];
      return [];
    }
  };
  try {
    const result = await context.automate({ id: 'image-turn-without-text', prompt: 'Read the two lines in this image.',
      image_base64: Buffer.from([137, 80, 78, 71]).toString('base64'), image_media_type: 'image/png', output: { mode: 'text' } },
    { name: 'gemini', selectors: { input: ['#input'], file_input: ['input[type="file"]'], response: ['#response'], submit: ['#send'] } },
    new Date(Date.now() + 60000).toISOString());
    assert.equal(sent, true);
    assert.equal(result.ok, shouldPass, 'a blank Gemini image turn is acceptable only with the full submission chain');
    if (shouldPass) {
      assert.equal(result.text, 'IamAngusU\nContextBridge');
      assert.equal(result.submitted_prompt_verified, true);
    } else assert.equal(result.code, 'browser_submit_unavailable');
  } finally {
    context.Date = realDate;
    context.setTimeout = realSetTimeout;
  }
}

{
  let sent = false;
  class TextArea {
    constructor() { this.value = ''; this.offsetWidth = 1; this.offsetHeight = 1; }
    getClientRects() { return [1]; }
    focus() {}
    dispatchEvent() {}
    setAttribute() {}
  }
  context.HTMLTextAreaElement = TextArea;
  context.InputEvent = class {};
  const input = new TextArea();
  input.closest = () => null;
  const send = { ...element('', { 'aria-label': 'Nachricht senden' }), click() { sent = true; } };
  context.document = {
    querySelectorAll(selector) {
      if (selector === '#input') return [input];
      if (selector === '#send') return [send];
      return [];
    }
  };
  const result = await context.automate({ prompt: 'Do not send after the deadline', output: { mode: 'text' } },
    { name: 'gemini', selectors: { input: ['#input'], response: [], submit: ['#send'] } },
    new Date(Date.now() - 1000).toISOString());
  assert.equal(sent, false, 'a delayed background script must never send after the job deadline');
  assert.equal(result.code, 'browser_timeout');
}

{
  const removeMusic = element('', { 'aria-label': 'Auswahl von „Musik“ aufheben' });
  const composer = { querySelectorAll: () => [removeMusic] };
  const input = { ...element(), tagName: 'DIV', closest: () => composer };
  context.document = {
    querySelectorAll(selector) { return selector === '#input' ? [input] : []; },
    querySelector: () => null
  };
  const snapshot = context.inspectPageDOM({ input: ['#input'], submit: [], response: [] });
  assert.equal(snapshot.tools[0].aria_label, 'Auswahl von „Musik“ aufheben');
  assert.equal(snapshot.input_has_text, false);
}

{
  const stop = { ...element(), tagName: 'BUTTON', disabled: true, querySelector: () => ({}), getAttribute(name) {
    return name === 'data-testid' ? 'stop-button' : null;
  } };
  context.document = {
    querySelectorAll(selector) {
      if (selector.includes('button[data-testid*="stop" i]')) return [stop];
      return [];
    },
    querySelector: () => null
  };
  const snapshot = context.inspectPageDOM({ input: [], submit: [], response: [] });
  assert.equal(snapshot.stop_button_disabled, true);
  assert.equal(snapshot.stop_button_spinning, true);
  assert.equal(snapshot.busy_indicators.includes('stop_button'), true);
}

{
  const input = { ...element(), tagName: 'DIV', closest: () => ({ querySelectorAll: () => [] }) };
  const music = { ...element('Musik erstellen', { 'aria-checked': 'false' }), tagName: 'BUTTON', getAttribute(name) {
    return name === 'role' ? 'menuitemcheckbox' : name === 'aria-checked' ? 'false' : null;
  } };
  context.document = {
    querySelectorAll(selector) {
      if (selector === '#input') return [input];
      if (selector === '[role="menuitem"], [role="option"], toolbox-drawer-item [role="menuitemcheckbox"]') return [music];
      return [];
    },
    querySelector: () => null
  };
  const snapshot = context.inspectPageDOM({ input: ['#input'], submit: [], response: [] });
  assert.equal(snapshot.tools[0].role, 'menuitemcheckbox');
  assert.equal(snapshot.tools[0].text, 'Musik erstellen');
}

{
  const input = { ...element('My unsent draft'), closest: () => null };
  context.document = { querySelectorAll: (selector) => selector === '#input' ? [input] : [] };
  const result = await context.automate({ prompt: 'Different job', output: { mode: 'text' } },
    { name: 'gemini', selectors: { input: ['#input'], response: [], submit: [] } },
    new Date(Date.now() + 5000).toISOString());
  assert.equal(result.code, 'browser_composer_busy');
}

{
  class MockTextArea {
    constructor(onFocus) { this.value = ''; this.offsetWidth = 1; this.offsetHeight = 1; this.onFocus = onFocus; }
    getClientRects() { return [1]; }
    focus() { this.onFocus?.(); }
    dispatchEvent() {}
  }
  context.HTMLTextAreaElement = MockTextArea;
  context.HTMLInputElement = class {};
  context.InputEvent = class {};
  context.Event = class {};
  let liveInput;
  const second = new MockTextArea();
  const first = new MockTextArea(() => { liveInput = second; });
  liveInput = first;
  const send = { ...element('', { 'aria-label': 'Nachricht senden' }), click() { throw new Error('Fresh send control was used'); } };
  context.document = {
    querySelectorAll(selector) {
      if (selector === '#input') return [liveInput];
      if (selector === '#send') return [send];
      return [];
    }
  };
  const result = await context.automate({ prompt: 'CB-PRO-TEST', output: { mode: 'text' } },
    { name: 'gemini', selectors: { input: ['#input'], response: [], submit: ['#send'] } },
    new Date(Date.now() + 5000).toISOString());
  assert.equal(first.value, 'CB-PRO-TEST');
  assert.equal(second.value, '');
  assert.equal(result.code, 'browser_submit_unavailable');
  const stable = new MockTextArea();
  liveInput = stable;
  const stableResult = await context.automate({ prompt: 'CB-PRO-TEST', output: { mode: 'text' } },
    { name: 'gemini', selectors: { input: ['#input'], response: [], submit: ['#send'] } },
    new Date(Date.now() + 5000).toISOString());
  assert.equal(stable.value, 'CB-PRO-TEST');
  assert.equal(stableResult.error, 'Fresh send control was used');
}

{
  let insertions = 0;
  let scopedNode = null;
  const commands = [];
  const selection = { anchorNode: null, removeAllRanges() {}, addRange(range) { this.anchorNode = range.node; } };
  context.window = { getSelection: () => selection };
  const input = {
    ...element(''),
    matches: (selector) => selector.startsWith('.ql-editor'),
    focus() { context.document.activeElement = input; },
    dispatchEvent() {}
  };
  const send = { ...element(''), click() { throw new Error('Quill send was used'); } };
  context.document = {
    activeElement: null,
    createRange: () => ({ node: null, selectNodeContents(node) { this.node = node; scopedNode = node; } }),
    querySelectorAll(selector) { return selector === '#input' ? [input] : selector === '#send' ? [send] : []; },
    execCommand(command, _show, value) {
      commands.push(command);
      if (command === 'insertText') { insertions += 1; input.innerText = value; return true; }
      return true;
    }
  };
  // executeScript serializes this function without the background script's
  // top-level scope. Test that injected path, not only the in-file VM call.
  const isolated = vm.createContext({ document: context.document, window: context.window,
    HTMLTextAreaElement: context.HTMLTextAreaElement, HTMLInputElement: context.HTMLInputElement,
    InputEvent: context.InputEvent, Event: context.Event, setTimeout, clearTimeout, Date, Promise });
  const injectedAutomate = vm.runInContext(`(${context.automate.toString()})`, isolated);
  const result = await injectedAutomate({ prompt: 'One Quill input', output: { mode: 'text' } },
    { name: 'gemini', selectors: { input: ['#input'], response: [], submit: ['#send'] } },
    new Date(Date.now() + 5000).toISOString());
  assert.equal(insertions, 1);
  assert.equal(scopedNode, input);
  assert.deepEqual(commands, ['insertText']);
  assert.equal(result.error, 'Quill send was used');
}

{
  let sent = false;
  let now = Date.now();
  class FastDate extends Date {
    static now() { now += 5000; return now; }
    static parse(value) { return Date.parse(value); }
  }
  class TextArea {
    constructor() { this.value = ''; this.offsetWidth = 1; }
    getClientRects() { return [1]; }
    focus() {}
    dispatchEvent() {}
  }
  const input = new TextArea();
  const send = { ...element(), click() { sent = true; } };
  const stop = element('', { 'data-testid': 'stop-button' });
  const response = element('READY');
  const document = {
    querySelectorAll(selector) {
      if (selector === '#input') return [input];
      if (selector === '#send') return [send];
      if (selector === '#response') return sent ? [response] : [];
      if (sent && selector.includes('stop')) return [stop];
      return [];
    }
  };
  const isolated = vm.createContext({ document, window: {}, HTMLTextAreaElement: TextArea,
    HTMLInputElement: class {}, InputEvent: class {}, Event: class {},
    setTimeout: (callback) => callback(), clearTimeout() {}, Date: FastDate, Promise });
  const injectedAutomate = vm.runInContext(`(${context.automate.toString()})`, isolated);
  const result = await injectedAutomate(
    { prompt: 'READY', output: { mode: 'text' } },
    { name: 'chatgpt', selectors: { input: ['#input'], submit: ['#send'], response: ['#response'] } },
    new Date(Date.now() + 3600000).toISOString()
  );
  assert.equal(result.code, 'stalled_response');
  assert.equal(result.recoverable, true);
}

for (const disabled of [true, false]) {
  let sent = false;
  let now = Date.now();
  class FastDate extends Date {
    static now() { now += 5000; return now; }
    static parse(value) { return Date.parse(value); }
  }
  class TextArea {
    constructor() { this.value = ''; this.offsetWidth = 1; }
    getClientRects() { return [1]; }
    focus() {}
    dispatchEvent() {}
  }
  const input = new TextArea();
  const send = { ...element(), click() { sent = true; } };
  const stop = { ...element('', { 'data-testid': 'stop-button' }), disabled };
  const previous = element('Older answer');
  const document = {
    querySelectorAll(selector) {
      if (selector === '#input') return [input];
      if (selector === '#send') return [send];
      if (selector === '#response') return [previous];
      if (selector === '[data-turn="user"]') return sent ? [element('Submitted prompt')] : [];
      if (sent && selector.includes('stop')) return [stop];
      return [];
    }
  };
  const isolated = vm.createContext({ document, window: {}, HTMLTextAreaElement: TextArea,
    HTMLInputElement: class {}, InputEvent: class {}, Event: class {},
    setTimeout: (callback) => callback(), clearTimeout() {}, Date: FastDate, Promise });
  const injectedAutomate = vm.runInContext(`(${context.automate.toString()})`, isolated);
  const result = await injectedAutomate(
    { prompt: 'Submitted prompt', output: { mode: 'text' } },
    { name: 'chatgpt', selectors: { input: ['#input'], submit: ['#send'], response: ['#response'] } },
    new Date(Date.now() + 3600000).toISOString()
  );
  assert.equal(result.code, disabled ? 'stalled_response' : 'browser_timeout');
  assert.equal(result.recoverable, disabled);
}

{
  let sent = false;
  let now = Date.now();
  class FastDate extends Date {
    static now() { now += 5000; return now; }
    static parse(value) { return Date.parse(value); }
  }
  class TextArea {
    constructor() { this.value = ''; this.offsetWidth = 1; }
    getClientRects() { return [1]; }
    focus() {}
    dispatchEvent() {}
  }
  const input = new TextArea();
  const send = { ...element(), click() { sent = true; } };
  const stop = element('', { 'data-testid': 'stop-button' });
  const ariaBusy = element();
  const response = element('MODEASCII19');
  const document = {
    querySelectorAll(selector) {
      if (selector === '#input') return [input];
      if (selector === '#send') return [send];
      if (selector === '#response') return sent ? [response] : [];
      if (sent && selector === '[aria-busy="true"]') return [ariaBusy];
      if (sent && selector.includes('stop')) return [stop];
      return [];
    }
  };
  const isolated = vm.createContext({ document, window: {}, HTMLTextAreaElement: TextArea,
    HTMLInputElement: class {}, InputEvent: class {}, Event: class {},
    setTimeout: (callback) => callback(), clearTimeout() {}, Date: FastDate, Promise });
  const injectedAutomate = vm.runInContext(`(${context.automate.toString()})`, isolated);
  const result = await injectedAutomate(
    { prompt: 'MODEASCII19', output: { mode: 'text' } },
    { name: 'gemini', selectors: { input: ['#input'], submit: ['#send'], response: ['#response'] } },
    new Date(Date.now() + 3600000).toISOString()
  );
  assert.equal(result.code, 'stalled_response');
  assert.equal(result.recoverable, true);
}

{
  const input = element();
  const previous = element('Previous answer', { 'data-testid': 'conversation-turn-2' });
  const remounted = element('Previous answer', { 'data-testid': 'conversation-turn-2' });
  let responseReads = 0;
  context.document = {
    querySelectorAll(selector) {
      if (selector === '#input') return [input];
      if (selector === '#response') {
        responseReads += 1;
        return [responseReads === 1 ? previous : remounted];
      }
      return [];
    },
    dispatchEvent() {}
  };
  const result = await context.automate(
    { prompt: 'ignored', metadata: { contextbridge_resume_only: true, contextbridge_baseline_text: 'Previous answer' }, output: { mode: 'text' } },
    { selectors: { input: ['#input'], response: ['#response'], submit: [] } },
    new Date(Date.now() + 2000).toISOString()
  );
  assert.equal(result.ok, false);
  assert.equal(result.code, 'browser_timeout');
}

{
  const input = element();
  const response = element('Finished answer');
  const stop = element('', { 'data-testid': 'stop-button' });
  let stopChecks = 0;
  context.document = {
    querySelectorAll(selector) {
      if (selector === '#input') return [input];
      if (selector === '#response') return [response];
      if (selector === 'button[data-testid*="stop" i]') {
        stopChecks += 1;
        return stopChecks <= 2 ? [stop] : [];
      }
      return [];
    },
    dispatchEvent() {}
  };
  const result = await context.automate(
    { prompt: 'ignored', metadata: { contextbridge_resume_only: true, contextbridge_baseline_text: 'Previous answer' }, output: { mode: 'text' } },
    { selectors: { input: ['#input'], response: ['#response'], submit: ['#send'] } },
    new Date(Date.now() + 10000).toISOString()
  );
  assert.equal(result.ok, true);
  assert.equal(result.text, 'Finished answer');
}

{
  const answer = element('Actual answer');
  const fullTurn = {
    ...element('ChatGPT: thought for 10s Actual answer Copy Edit'),
    querySelectorAll(selector) {
      return selector === '[data-message-author-role="assistant"]' ? [answer] : [];
    }
  };
  context.document = {
    querySelectorAll(selector) {
      return selector === '#turns' ? [fullTurn] : [];
    }
  };
  assert.equal(context.captureProgress({ response: ['#turns'] }).text, 'Actual answer');

  const thinkingOnly = {
    ...element('Pro-Denkvorgang 12s'),
    matches: (selector) => selector === 'section[data-turn="assistant"]'
  };
  context.document = {
    querySelectorAll(selector) {
      return selector === '#turns' ? [thinkingOnly] : [];
    }
  };
  assert.equal(context.captureProgress({ response: ['#turns'] }).text, '');

  const image = { naturalWidth: 512, naturalHeight: 512 };
  const imageTurn = {
    ...element('ChatGPT: Worked for 44s Edit'),
    matches: () => true,
    querySelector: (selector) => selector === 'img' ? image : null
  };
  context.document = {
    querySelectorAll(selector) {
      return selector === '#turns' ? [imageTurn] : [];
    }
  };
  assert.equal(context.captureProgress({ response: ['#turns'] }).text, '');

  const loadingTurn = {
    ...element('Denkt nach ... Ein Bild wird erstellt 67 %'),
    matches: () => true,
    querySelector: (selector) => selector.includes('image-gen-loading-state') ? loading : null,
    querySelectorAll: (selector) => selector === '[data-message-author-role="assistant"]' ? [element('Denkt nach ... 67 %')] : []
  };
  const loading = element();
  context.document = {
    querySelectorAll(selector) {
      return selector === '#turns' ? [loadingTurn] : [];
    }
  };
  assert.equal(context.captureProgress({ response: ['#turns'] }).text, '');
}

{
  context.crypto = webcrypto;
  context.btoa = (value) => Buffer.from(value, 'binary').toString('base64');
  const bytes = new Uint8Array([137, 80, 78, 71]);
  let fetches = 0;
  context.fetch = async (url, options) => {
    fetches += 1;
    assert.equal(options.credentials, String(url).includes('/backend-api/files/') ? 'include' : 'omit');
    assert.equal(options.redirect, 'error');
    let sent = false;
    return {
      ok: true,
      headers: { get: () => 'image/png' },
      body: { getReader: () => ({
        read: async () => sent ? { done: true } : (sent = true, { done: false, value: bytes }),
        cancel: async () => {}
      }) }
    };
  };
  const reference = { name: 'image-1.png', media_type: 'image/png',
    url: 'https://chatgpt.com/backend-api/files/file_abc123/download', contextbridge_provenance: 'chatgpt_file_citation' };
  const hydrated = await context.hydrateArtifactReferences([reference], 'https://chatgpt.com/c/test', { artifacts: true });
  assert.equal(hydrated[0].data_base64, Buffer.from(bytes).toString('base64'));
  assert.equal(hydrated[0].size, bytes.length);
  assert.equal(hydrated[0].sha256.length, 64);
  assert.equal(hydrated[0].contextbridge_provenance, undefined, 'internal provenance must not leave the extension');
  const generic = await context.hydrateArtifactReferences(
    [{ name: 'public.png', media_type: 'image/png', url: 'https://chatgpt.com/generated.png' }],
    'https://chatgpt.com/c/test', { artifacts: true });
  assert.equal(generic[0].data_base64, Buffer.from(bytes).toString('base64'));
  const anchorOnly = await context.hydrateArtifactReferences(
    [{ name: 'download.png', media_type: 'image/png', url: 'https://chatgpt.com/download-looking-link',
      contextbridge_provenance: 'generic_link' }],
    'https://chatgpt.com/c/test', { artifacts: true });
  assert.equal(anchorOnly[0].data_base64, undefined, 'a generic link is retained but never transferred');
  assert.equal(anchorOnly[0].contextbridge_provenance, undefined, 'the no-transfer marker stays internal');
  const spoofed = await context.hydrateArtifactReferences(
    [{ name: 'private.json', media_type: 'application/json', url: 'https://chatgpt.com/backend-api/accounts',
      contextbridge_provenance: 'chatgpt_file_citation' }],
    'https://chatgpt.com/c/test', { artifacts: true });
  assert.equal(spoofed[0].data_base64, undefined, 'a provenance label cannot credential-fetch a non-citation endpoint');
  const external = await context.hydrateArtifactReferences(
    [{ ...reference, url: 'https://other.example/generated.png' }],
    'https://chatgpt.com/c/test',
    { artifacts: true }
  );
  assert.equal(external[0].data_base64, undefined);
  assert.equal(fetches, 2);

  let cancelled = false;
  let chunk = 0;
  context.fetch = async () => ({
    ok: true,
    headers: { get: (name) => String(name).toLowerCase() === 'content-type' ? 'image/png' : null },
    body: { getReader: () => ({
      read: async () => ++chunk === 1 ? { done: false, value: new Uint8Array(700) }
        : chunk === 2 ? { done: false, value: new Uint8Array(400) } : { done: true },
      cancel: async () => { cancelled = true; }
    }) }
  });
  const oversized = await context.hydrateArtifactReferences([reference], 'https://chatgpt.com/c/test',
    { artifacts: true, max_artifact_bytes: 1024 });
  assert.equal(oversized[0].data_base64, undefined,
    'a chunked artifact with no Content-Length must not cross the byte ceiling');
  assert.equal(oversized[0].url, reference.url, 'an over-limit stream remains only a reference');
  assert.equal(cancelled, true, 'the reader must be cancelled as soon as the streamed limit is exceeded');
}

{
  const bytes = new Uint8Array([0, 0, 0, 24, 102, 116, 121, 112, 109, 112, 52, 50, 0, 0, 0, 0, 109, 112, 52, 50, 0, 0, 0, 0]);
  let fetches = 0;
  let contentType = 'video/mp4';
  context.fetch = async (_url, options) => {
    fetches += 1;
    assert.equal(options.credentials, 'omit');
    let sent = false;
    return {
      ok: true,
      headers: { get: () => contentType },
      body: { getReader: () => ({
        read: async () => sent ? { done: true } : (sent = true, { done: false, value: bytes }),
        cancel: async () => {}
      }) }
    };
  };
  const media = { name: 'song.mp4', media_type: 'video/mp4', url: 'https://contribution.usercontent.google.com/download?filename=song.mp4' };
  const hydrated = await context.hydrateArtifactReferences([media], 'https://gemini.google.com/app', { artifacts: true });
  assert.equal(hydrated[0].data_base64, Buffer.from(bytes).toString('base64'));
  contentType = 'application/octet-stream';
  const binaryDownload = await context.hydrateArtifactReferences([media], 'https://gemini.google.com/app', { artifacts: true });
  assert.equal(binaryDownload[0].media_type, 'video/mp4');
  contentType = 'text/html';
  const badDownload = await context.hydrateArtifactReferences([media], 'https://gemini.google.com/app', { artifacts: true });
  assert.equal(badDownload[0].data_base64, undefined);
  const hostile = await context.hydrateArtifactReferences([{ ...media, url: 'https://other.example/song.mp4' }], 'https://gemini.google.com/app', { artifacts: true });
  assert.equal(hostile[0].data_base64, undefined);
  assert.equal(fetches, 3);
}

{
  const toolbar = { ...element('Bild erstellen', { 'aria-label': 'Bild erstellen', 'data-testid': 'create-image' }), tagName: 'BUTTON', id: 'image-tool', type: 'button' };
  const composer = { querySelectorAll: () => [toolbar] };
  const prompt = { ...element(), tagName: 'DIV', id: 'prompt-textarea', closest: () => composer };
  const upload = { ...element('', { 'data-testid': 'upload-photos-input' }), offsetWidth: 0, offsetHeight: 0, getClientRects: () => [], tagName: 'INPUT', id: 'upload-photos', type: 'file', accept: 'image/*', multiple: true };
  const answer = { ...element('private answer'), querySelectorAll: (selector) => selector === 'img' ? [{}, {}] : [] };
  context.document = {
    visibilityState: 'hidden',
    wasDiscarded: true,
    querySelectorAll(selector) {
      if (selector === '#prompt-textarea') return [prompt];
      if (selector === '#send') return [toolbar];
      if (selector === 'input[type="file"]') return [upload];
      if (selector === '#turns') return [answer];
      return [];
    },
    querySelector: () => null
  };
  const snapshot = context.inspectPageDOM({ input: ['#prompt-textarea'], submit: ['#send'], response: ['#turns'] });
  assert.equal(snapshot.file_inputs[0].id, 'upload-photos');
  assert.equal(snapshot.file_inputs[0].visible, false);
  assert.equal(snapshot.file_inputs[0].accept, 'image/*');
  assert.equal(snapshot.last_response_images, 2);
  assert.equal(snapshot.last_response_loaded_images, 0);
  assert.equal(snapshot.page_visibility, 'hidden');
  assert.equal(snapshot.was_discarded, true);
  assert.equal(JSON.stringify(snapshot).includes('private answer'), false);
}

{
  const response = {
    ...element('private streaming answer'),
    querySelectorAll: (selector) => selector.includes('aria-busy') ? [element()] : []
  };
  const busy = element();
  const stop = element();
  context.document = {
    querySelectorAll(selector) {
      if (selector === '#turns') return [response];
      if (selector === '[aria-busy="true"]') return [busy];
      if (selector.startsWith('button[data-testid*="stop" i],')) return [stop];
      return [];
    },
    querySelector: () => null
  };
  const snapshot = context.inspectPageDOM({ response: ['#turns'] });
  assert.equal(snapshot.last_response_busy, true);
  assert.deepEqual([...snapshot.busy_indicators], ['aria_busy', 'stop_button']);
  assert.equal(snapshot.last_response_characters, 'private streaming answer'.length);
  assert.equal(JSON.stringify(snapshot).includes('private streaming answer'), false);
}

{
  let sent = false;
  let fakeNow = Date.now();
  const realDate = context.Date;
  const realSetTimeout = context.setTimeout;
  context.Date = class extends Date { static now() { return fakeNow; } };
  context.setTimeout = (callback, milliseconds) => realSetTimeout(() => { fakeNow += milliseconds; callback(); }, 1);
  class TextArea {
    constructor() { this.value = ''; this.offsetWidth = 1; this.offsetHeight = 1; }
    getClientRects() { return [1]; }
    focus() {}
    dispatchEvent() {}
  }
  context.HTMLTextAreaElement = TextArea;
  context.HTMLInputElement = class {};
  context.InputEvent = class {};
  context.Event = class {};
  const input = new TextArea();
  const send = { ...element(''), click() { sent = true; input.value = ''; } };
  const image = { complete: true, naturalWidth: 512, naturalHeight: 512, src: '', getAttribute: () => null };
  const response = {
    ...element(''), matches: () => true,
    querySelector: (selector) => selector === 'img' ? image : null,
    querySelectorAll: (selector) => selector === 'img' ? [image] : []
  };
  context.document = {
    querySelectorAll(selector) {
      if (selector === '#input') return [input];
      if (selector === '#send') return sent ? [] : [send];
      if (selector === '#response') return sent ? [response] : [];
      if (selector === '[aria-busy="true"]') return sent ? [element()] : [];
      return [];
    }
  };
  try {
    const result = await context.automate(
      { prompt: 'one image', output: { mode: 'text', artifacts: true, min_images: 1 } },
      { name: 'gemini', selectors: { input: ['#input'], response: ['#response'], submit: ['#send'] } },
      new Date(Date.now() + 60000).toISOString()
    );
    assert.equal(result.ok, true);
    assert.ok(fakeNow - Date.now() >= 12000);
  } finally {
    context.Date = realDate;
    context.setTimeout = realSetTimeout;
  }
}

{
  const handlers = {};
  const hints = [];
  const previousSetTimeout = context.setTimeout;
  const previousClearTimeout = context.clearTimeout;
  chrome.runtime.sendMessage = async (message) => { hints.push(message); };
  context.setTimeout = (fn) => { fn(); return 1; };
  context.clearTimeout = () => {};
  context.document = { addEventListener: (name, handler) => { handlers[name] = handler; } };
  assert.equal(context.watchPageCapabilityInteractions(), true);
  assert.equal(context.watchPageCapabilityInteractions(), true, 'watcher must not install twice');
  const control = {
    innerText: 'GPT-5.5', getAttribute: () => '', matches: () => false
  };
  const target = { closest: () => control };
  handlers.click({ isTrusted: false, target });
  assert.equal(hints.length, 0, 'synthetic scanner clicks must not retrigger a scan');
  handlers.click({ isTrusted: true, target });
  assert.equal(hints.length, 1);
  assert.equal(hints[0].type, 'page-capability-interaction');
  context.setTimeout = previousSetTimeout;
  context.clearTimeout = previousClearTimeout;
}

{
  const originalSettings = context.settings;
  const originalProfileForTab = context.profileForTab;
  const originalTabGet = chrome.tabs.get;
  const originalExecute = chrome.scripting.executeScript;
  const originalStorageSet = chrome.storage.local.set;
  const work = { profile: { name: 'gemini' }, job: { session_id: 'late-url', contextbridge_session_key: 'late-url' } };
  const key = context.workSessionKey(work);
  const permanentURL = 'https://gemini.google.com/app/owned-conversation';
  const cfg = { tabIds: [42], sessionBindingsMigrated: true, sessionBindings: {
    [key]: { tabId: 42, url: 'https://gemini.google.com/app', ownedTurn: { id: 'user-query-content-1', digest: 'a'.repeat(64), provider: 'gemini' } }
  } };
  let proofMatches = true;
  let proofChecks = 0;
  context.settings = async () => cfg;
  context.profileForTab = () => ({ name: 'gemini', selectors: { input: ['#input'], response: ['model-response'] } });
  chrome.tabs.get = async () => ({ id: 42, url: permanentURL });
  chrome.scripting.executeScript = async () => { proofChecks += 1; return [{ result: { owned_turn_matches: proofMatches } }]; };
  chrome.storage.local.set = async (value) => Object.assign(cfg, value);
  try {
    assert.equal(await context.resolveWorkTab(cfg, work, 42), 42);
    assert.equal(cfg.sessionBindings[key].url, permanentURL);
    assert.equal(proofChecks, 1, 'late URL binding must be checked against the owned user turn');
    cfg.sessionBindings[key].url = 'https://gemini.google.com/app';
    proofMatches = false;
    await assert.rejects(context.resolveWorkTab(cfg, work, 42), /session tab moved or closed/i);
    assert.equal(cfg.sessionBindings[key].url, 'https://gemini.google.com/app', 'uncertain ownership must not change the binding');
  } finally {
    context.settings = originalSettings;
    context.profileForTab = originalProfileForTab;
    chrome.tabs.get = originalTabGet;
    chrome.scripting.executeScript = originalExecute;
    chrome.storage.local.set = originalStorageSet;
  }
}

{
  const previousLocation = context.location;
  const previousSendMessage = chrome.runtime.sendMessage;
  let authorityChecks = 0;
  chrome.runtime.sendMessage = async () => { authorityChecks += 1; return { ok: true }; };
  context.location = { href: 'https://chatgpt.com/c/other', origin: 'https://chatgpt.com', hostname: 'chatgpt.com' };
  const changed = await context.automate({ id: 'url-guard', prompt: 'must not leak', output: { mode: 'text' } },
    { name: 'chatgpt', selectors: { input: ['#input'], submit: ['#send'], response: [] } },
    new Date(Date.now() + 5000).toISOString(), null, 'https://chatgpt.com/c/expected',
    { jobId: 'url-guard', generation: 7 });
  assert.equal(changed.code, 'browser_session_changed');
  assert.equal(authorityChecks, 0, 'a changed conversation must fail before contacting or touching the provider page');

  chrome.runtime.sendMessage = async () => { authorityChecks += 1; return { ok: false, error: 'browser_job_lease_lost' }; };
  context.location = { href: 'https://chatgpt.com/c/expected', origin: 'https://chatgpt.com', hostname: 'chatgpt.com' };
  context.document = { querySelectorAll: () => [] };
  const cancelled = await context.automate({ id: 'lease-guard', prompt: 'must not send', output: { mode: 'text' } },
    { name: 'chatgpt', selectors: { input: ['#input'], submit: ['#send'], response: [] } },
    new Date(Date.now() + 5000).toISOString(), null, 'https://chatgpt.com/c/expected',
    { jobId: 'lease-guard', generation: 8 });
  assert.equal(cancelled.code, 'browser_lease_lost');
  assert.equal(authorityChecks, 1, 'the injected automation must consult authoritative lease state before page work');
  chrome.runtime.sendMessage = previousSendMessage;
  if (previousLocation === undefined) delete context.location;
  else context.location = previousLocation;
}

{
  const previousLocation = context.location;
  const previousDocument = context.document;
  const previousSendMessage = chrome.runtime.sendMessage;
  const previousTextArea = context.HTMLTextAreaElement;
  const previousInput = context.HTMLInputElement;
  const previousInputEvent = context.InputEvent;
  let clicked = false;
  class TextArea {
    constructor() { this.value = ''; this.offsetWidth = 1; this.offsetHeight = 1; }
    getClientRects() { return [1]; }
    focus() {}
    dispatchEvent() {}
    setAttribute() {}
    closest() { return { querySelector: () => null, querySelectorAll: () => [] }; }
  }
  context.HTMLTextAreaElement = TextArea;
  context.HTMLInputElement = class {};
  context.InputEvent = class {};
  const input = new TextArea();
  const send = { ...element('', { 'aria-label': 'Send message' }), click() { clicked = true; } };
  context.location = { href: 'https://chatgpt.com/c/expected', origin: 'https://chatgpt.com', hostname: 'chatgpt.com' };
  context.document = { querySelectorAll(selector) {
    if (selector === '#input') return [input];
    if (selector === '#send') return [send];
    if (selector === '[data-turn="user"]') return [];
    return [];
  } };
  chrome.runtime.sendMessage = async (message) => {
    if (message.action === 'send') {
      await Promise.resolve();
      context.location.href = 'https://chatgpt.com/c/changed-during-send-claim';
    }
    return { ok: true };
  };
  try {
    const result = await context.automate({ id: 'send-url-race', prompt: 'do not leak', output: { mode: 'text' } },
      { name: 'chatgpt', selectors: { input: ['#input'], submit: ['#send'], response: [] } },
      new Date(Date.now() + 5000).toISOString(), null, 'https://chatgpt.com/c/expected',
      { jobId: 'send-url-race', generation: 9 });
    assert.equal(result.code, 'browser_session_changed');
    assert.equal(clicked, false, 'a URL change while awaiting Send authorization must fail before the provider click');
  } finally {
    chrome.runtime.sendMessage = previousSendMessage;
    context.document = previousDocument;
    if (previousLocation === undefined) delete context.location;
    else context.location = previousLocation;
    context.HTMLTextAreaElement = previousTextArea;
    context.HTMLInputElement = previousInput;
    context.InputEvent = previousInputEvent;
  }
}

{
  const previousSettings = context.settings;
  const previousFetch = context.fetch;
  const previousDelay = context.delay;
  const previousTabGet = chrome.tabs.get;
  const previousStorageSet = chrome.storage.local.set;
  const activeLeases = vm.runInContext('activeBrowserLeases', context);
  const expectedURL = 'https://chatgpt.com/c/lease-owned';
  const state = { running: true, tabIds: [22], browserJobClaims: {
    'claimed-job': { generation: 11, sessionKey: 'session', baselineDigest: 'a'.repeat(64), at: Date.now() }
  } };
  let claims = 0;
  context.settings = async () => ({ bridgeUrl: 'http://127.0.0.1:32145', token: 'token', ...state });
  context.fetch = async (url, options) => {
    assert.match(url, /\/claim$/);
    assert.equal(options.headers['X-ContextBridge-Lease-Generation'], '11');
    assert.equal(JSON.parse(options.body).action, 'send');
    claims += 1;
    return { ok: true };
  };
  chrome.tabs.get = async () => ({ id: 22, url: expectedURL });
  chrome.storage.local.set = async (update) => Object.assign(state, update);
  activeLeases.set('claimed-job', { jobId: 'claimed-job', generation: 11, tabId: 22, expectedURL, cancelled: false });
  const authorized = await context.authorizeBrowserJobAction(
    { jobId: 'claimed-job', generation: 11, action: 'send', expectedURL }, { tab: { id: 22 } });
  assert.equal(authorized.ok, true);
  assert.equal(state.browserJobClaims['claimed-job'].state, 'sent_unknown');
  assert.equal(claims, 1);
  context.delay = async () => {};
  context.fetch = async () => { throw new TypeError('temporary local bridge outage'); };
  const unavailable = await context.authorizeBrowserJobAction(
    { jobId: 'claimed-job', generation: 11, action: 'observe', expectedURL }, { tab: { id: 22 } });
  assert.equal(unavailable.error, 'browser_bridge_unavailable');
  assert.equal(activeLeases.get('claimed-job').cancelled, false,
    'an indeterminate transport failure must fail closed without inventing authoritative lease loss');
  context.fetch = async () => ({ ok: false, status: 409 });
  const lost = await context.authorizeBrowserJobAction(
    { jobId: 'claimed-job', generation: 11, action: 'observe', expectedURL }, { tab: { id: 22 } });
  assert.equal(lost.error, 'browser_job_lease_lost');
  assert.equal(activeLeases.get('claimed-job').cancelled, true, 'an HTTP 409 must cancel the in-memory lease');
  const stale = await context.authorizeBrowserJobAction(
    { jobId: 'claimed-job', generation: 10, action: 'send', expectedURL }, { tab: { id: 22 } });
  assert.equal(stale.ok, false);
  assert.equal(claims, 1, 'a stale generation must fail before the server action endpoint');
  activeLeases.delete('claimed-job');
  context.settings = previousSettings;
  context.fetch = previousFetch;
  context.delay = previousDelay;
  chrome.tabs.get = previousTabGet;
  chrome.storage.local.set = previousStorageSet;
}

{
  const previousSettings = context.settings;
  const previousFetch = context.fetch;
  const previousTabGet = chrome.tabs.get;
  const previousExecute = chrome.scripting.executeScript;
  const previousStorageSet = chrome.storage.local.set;
  const activeLeases = vm.runInContext('activeBrowserLeases', context);
  const freshURL = 'https://chatgpt.com/';
  const permanentURL = 'https://chatgpt.com/c/new-owned-chat';
  const ownedTurn = { id: 'owned-turn', digest: 'b'.repeat(64), provider: 'chatgpt' };
  const state = { running: true, tabIds: [31], sessionBindingsMigrated: true,
    sessionBindings: { session: { tabId: 31, url: freshURL, autoCreated: true } },
    browserJobClaims: { job: { generation: 4, sessionKey: 'session', tabId: 31,
      expectedURL: freshURL, state: 'sent_unknown', at: Date.now() } } };
  context.settings = async () => ({ bridgeUrl: 'http://127.0.0.1:32145', token: 'token', ...state });
  context.fetch = async (url) => {
    assert.match(url, /\/lease$/);
    return { ok: true };
  };
  chrome.tabs.get = async () => ({ id: 31, url: permanentURL });
  chrome.scripting.executeScript = async () => [{ result: ownedTurn }];
  chrome.storage.local.set = async (update) => Object.assign(state, update);
  const lease = { jobId: 'job', generation: 4, tabId: 31, expectedURL: freshURL, cancelled: false,
    sessionKey: 'session', prompt: 'owned prompt', profileName: 'chatgpt', sentUnknown: true };
  activeLeases.set('job', lease);
  const promoted = await context.authorizeBrowserJobAction(
    { jobId: 'job', generation: 4, action: 'observe', expectedURL: freshURL }, { tab: { id: 31 } });
  assert.equal(promoted.ok, true);
  assert.equal(promoted.expectedURL, permanentURL);
  assert.equal(lease.expectedURL, permanentURL);
  assert.equal(state.sessionBindings.session.url, permanentURL);
  assert.equal(state.browserJobClaims.job.expectedURL, permanentURL);
  assert.equal(state.sessionBindings.session.ownedTurn.id, ownedTurn.id);

  chrome.tabs.get = async () => ({ id: 31, url: 'https://chatgpt.com/c/unrelated-chat' });
  const changed = await context.authorizeBrowserJobAction(
    { jobId: 'job', generation: 4, action: 'observe', expectedURL: permanentURL }, { tab: { id: 31 } });
  assert.equal(changed.ok, false);
  assert.equal(changed.error, 'browser_conversation_changed');
  activeLeases.delete('job');
  context.settings = previousSettings;
  context.fetch = previousFetch;
  chrome.tabs.get = previousTabGet;
  chrome.scripting.executeScript = previousExecute;
  chrome.storage.local.set = previousStorageSet;
}

{
  assert.ok(context.boundedPendingCompletion({ mode: 'text', text: 'small' }, 1));
  assert.equal(context.boundedPendingCompletion({ mode: 'text', artifacts: [{ data_base64: 'A'.repeat(1024 * 1024) }] }, 1), null,
    'oversized Base64 results must never be written to extension local storage');
  const previousSettings = context.settings;
  const previousFetch = context.fetch;
  const previousStorageSet = chrome.storage.local.set;
  context.settings = async () => ({ pendingCompletions: {} });
  chrome.storage.local.set = async () => { throw new Error('QUOTA_BYTES exceeded'); };
  assert.equal(await context.rememberPendingCompletion('small', { mode: 'text', text: 'safe retry' }, 2), false,
    'a storage quota failure must not replace the authoritative completion path');
  context.fetch = async () => ({ ok: true, status: 200 });
  assert.equal(await context.completeWork({ bridgeUrl: 'http://127.0.0.1:32145', token: 'token' },
    'acknowledged', { mode: 'text', text: 'done' }, 3), true,
  'local cache cleanup failure must not undo an authoritative relay acknowledgement');
  context.fetch = (_url, options) => new Promise((resolve, reject) => {
    options.signal.addEventListener('abort', () => reject(options.signal.reason || new Error('aborted')), { once: true });
  });
  await assert.rejects(context.completeWork({ bridgeUrl: 'http://127.0.0.1:32145', token: 'token' },
    'hung-completion', { mode: 'text', text: 'done' }, 4, 5), (error) => error?.name === 'AbortError',
  'a hung completion request must abort so pending-completion recovery can run');
  await assert.rejects(context.fetchWithTimeout('http://127.0.0.1:32145/v1/browser/jobs/next?wait=25&profile=chatgpt',
    { headers: { Authorization: 'Bearer token' } }, 5), (error) => error?.name === 'AbortError',
  'a hung long-poll request must receive an AbortSignal deadline');
  context.settings = previousSettings;
  context.fetch = previousFetch;
  chrome.storage.local.set = previousStorageSet;
}

{
  const previousExecute = chrome.scripting.executeScript;
  let executions = 0;
  chrome.scripting.executeScript = () => { executions += 1; return new Promise(() => {}); };
  await assert.rejects(context.executeDiagnosticScript(77, { target: { tabId: 77 }, func() {} }, 5), /respond in time/);
  await assert.rejects(context.executeDiagnosticScript(77, { target: { tabId: 77 }, func() {} }, 5), /still pending/);
  assert.equal(executions, 1, 'heartbeat diagnostics must not accumulate unresolved injected scripts');
  await assert.rejects(context.captureTabProgress(78, { response: [] }), /respond in time/);
  await assert.rejects(context.captureTabProgress(78, { response: [] }), /still pending/);
  assert.equal(executions, 2, 'progress sampling must keep at most one unresolved scan per tab');
  vm.runInContext('diagnosticScriptsInFlight.delete(77); progressScriptsInFlight.delete(78)', context);
  chrome.scripting.executeScript = previousExecute;
}

{
  const latest = { ...element('LATEST-LONG-CHAT'), tagName: 'SECTION', id: 'latest',
    matches: () => false, querySelectorAll: () => [], querySelector: () => null };
  const responses = Array.from({ length: 10001 }, (_, index) => index === 10000 ? latest : element(`old-${index}`));
  const empty = [];
  context.document = {
    visibilityState: 'hidden', wasDiscarded: false,
    querySelector: () => null,
    querySelectorAll(selector) {
      if (selector === '#many-responses') return responses;
      return empty;
    }
  };
  const health = context.inspectPageDOM({ input: [], submit: [], response: ['#many-responses'] });
  assert.equal(health.assistant_turns, 10000, 'reported long-chat counts stay bounded');
  assert.equal(health.last_response_characters, 'LATEST-LONG-CHAT'.length,
    'the latest response must not become the ten-thousandth response after the diagnostic cap');
}

{
  let turnText = 'exact owned prompt';
  const content = { get textContent() { return turnText; } };
  const turn = {
    getAttribute: (name) => name === 'data-turn-id' ? 'turn-1' : null,
    querySelector: () => content,
    compareDocumentPosition: (candidate) => candidate?.after ? 4 : 2
  };
  const after = { after: true };
  context.document = { querySelectorAll(selector) {
    if (selector === 'section[data-turn="user"]') return [turn];
    if (selector === '#answer') return [after];
    return [];
  } };
  assert.ok(await context.inspectLatestOwnedTurn('exact owned prompt', 'chatgpt', true, { response: ['#answer'] }));
  turnText = 'exact owned prompt Copy Edit';
  assert.equal(await context.inspectLatestOwnedTurn('exact owned prompt', 'chatgpt', true, { response: ['#answer'] }), null,
    'decorated or containing text must not promote a fresh-chat lease');
  turnText = 'exact owned prompt';
  after.after = false;
  assert.equal(await context.inspectLatestOwnedTurn('exact owned prompt', 'chatgpt', true, { response: ['#answer'] }), null,
    'an older assistant response must not satisfy a newly owned user turn');
}

{
  const promptPart = { textContent: 'exact nested prompt' };
  const attachmentPart = { textContent: 'upload.png', closest: () => ({}) };
  const content = {
    textContent: 'upload.png exact nested prompt Copy Edit',
    querySelector: (selector) => /data-message-content-part|whitespace-pre-wrap|\.markdown/.test(selector) ? promptPart : null,
    querySelectorAll: (selector) => /data-message-content-part|whitespace-pre-wrap|\.markdown/.test(selector) ? [attachmentPart, promptPart] : []
  };
  const turn = { getAttribute: () => 'turn-with-controls', querySelector: () => content };
  context.document = { querySelectorAll: (selector) => selector === 'section[data-turn="user"]' ? [turn] : [] };
  assert.ok(await context.inspectLatestOwnedTurn('exact nested prompt', 'chatgpt'),
    'provider action chrome may decorate the wrapper, but the nested prompt node must still match exactly');
}

{
  const content = { textContent: 'bounded latest prompt' };
  const latestTurn = { getAttribute: () => 'latest', querySelector: () => content };
  const hugeNodeList = { length: 10001, item: (index) => index === 10000 ? latestTurn : null,
    [Symbol.iterator]() { throw new Error('hot ownership scan copied the entire DOM'); } };
  context.document = { querySelectorAll: (selector) => selector === 'section[data-turn="user"]' ? hugeNodeList : [] };
  assert.ok(await context.inspectLatestOwnedTurn('bounded latest prompt', 'chatgpt'),
    'ownership inspection must address only the last turn even in a very long chat');
}

console.log('Browser progress and provider failures verified');
