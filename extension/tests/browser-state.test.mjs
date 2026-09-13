import assert from 'node:assert/strict';
import fs from 'node:fs';
import vm from 'node:vm';
import { webcrypto } from 'node:crypto';

const source = fs.readFileSync(new URL('../src/background.js', import.meta.url), 'utf8');
const listeners = { addListener() {} };
const chrome = {
  runtime: { onInstalled: listeners, onStartup: listeners, onMessage: listeners, getManifest: () => ({ version: 'test' }) },
  storage: { local: { get: async (defaults) => defaults, set: async () => {} } },
  tabs: {}, scripting: {}, i18n: { getMessage: () => '' }
};
const context = vm.createContext({ chrome, console, URL, setTimeout, clearTimeout, setInterval, clearInterval, Date, Promise });
vm.runInContext(source, context);

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
  assert.equal(JSON.stringify(snapshot).includes('private answer'), false);
}

console.log('Browser progress and provider failures verified');
