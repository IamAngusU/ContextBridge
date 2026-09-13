import assert from 'node:assert/strict';
import fs from 'node:fs';
import vm from 'node:vm';
import { webcrypto } from 'node:crypto';
import { TextEncoder } from 'node:util';

const source = fs.readFileSync(new URL('../src/background.js', import.meta.url), 'utf8');
const listeners = { addListener() {} };
const chrome = {
  runtime: { onInstalled: listeners, onStartup: listeners, onMessage: listeners, getManifest: () => ({ version: 'test' }) },
  storage: { local: { get: async (defaults) => defaults, set: async () => {} } },
  tabs: {}, scripting: {}, i18n: { getMessage: () => '' }
};
const context = vm.createContext({ chrome, console, URL, TextEncoder, setTimeout, clearTimeout, setInterval, clearInterval, Date, Promise });
vm.runInContext(source, context);
assert.equal(context.classifyFailureReason('Prompt editor did not retain the submitted text'), 'prompt_not_retained');
assert.equal(context.classifyFailureReason('Send button stayed disabled after filling the prompt'), 'send_disabled');
assert.equal(context.classifyFailureReason('ChatGPT still shows Stop; the draft was left untouched'), 'provider_busy');
assert.equal(context.classifyFailureReason('A provider error containing private text'), 'other');
assert.equal(context.isNewAssistantTurn({ response_count: 2 }, { response_count: 2, text: 'Old music player clock changed', active_generation: true }), false);
assert.equal(context.isNewAssistantTurn({ response_count: 2 }, { response_count: 3, text: 'Fresh answer', active_generation: true }), true);
assert.equal(context.isNewAssistantTurn({ response_count: 2, response_identity: 'old' }, { response_count: 2, response_identity: 'new' }), true);
assert.equal(context.isNewAssistantTurn({ response_count: 9, response_identity: 'latest' }, { response_count: 8, response_identity: 'older' }), false);
assert.equal(context.isEditAssistantTurn({ response_count: 9 }, { response_count: 8, active_generation: true }, 'Older answer', 'Prior answer'), false);
assert.equal(context.isEditAssistantTurn({ response_count: 9 }, { response_count: 9, active_generation: true }, 'Fresh edited answer', 'Prior answer'), true);

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
  const composer = element('');
  context.document = {
    querySelector: () => composer,
    querySelectorAll: () => []
  };
  assert.equal(context.safeToDiscoverPageCapabilities(), true);
  composer.innerText = 'Unsent private draft';
  assert.equal(context.safeToDiscoverPageCapabilities(), false);
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
  context.InputEvent = class {};
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
    assert.equal(result.ok, true);
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
  context.fetch = async (_url, options) => {
    fetches += 1;
    assert.equal(options.credentials, 'include');
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
  const reference = { name: 'image-1.png', media_type: 'image/png', url: 'https://chatgpt.com/generated.png' };
  const hydrated = await context.hydrateArtifactReferences([reference], 'https://chatgpt.com/c/test', { artifacts: true });
  assert.equal(hydrated[0].data_base64, Buffer.from(bytes).toString('base64'));
  assert.equal(hydrated[0].size, bytes.length);
  assert.equal(hydrated[0].sha256.length, 64);
  const external = await context.hydrateArtifactReferences(
    [{ ...reference, url: 'https://other.example/generated.png' }],
    'https://chatgpt.com/c/test',
    { artifacts: true }
  );
  assert.equal(external[0].data_base64, undefined);
  assert.equal(fetches, 1);
}

{
  const bytes = new Uint8Array([0, 0, 0, 24, 102, 116, 121, 112, 109, 112, 52, 50, 0, 0, 0, 0, 109, 112, 52, 50, 0, 0, 0, 0]);
  let fetches = 0;
  let contentType = 'video/mp4';
  context.fetch = async (_url, options) => {
    fetches += 1;
    assert.equal(options.credentials, 'include');
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

console.log('Browser progress and provider failures verified');
