import assert from 'node:assert/strict';
import fs from 'node:fs';
import vm from 'node:vm';

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
  const profileMenu = element('', { 'aria-label': 'Profil-Menü öffnen' });
  const accountMenu = element('Angus Uelsmann Pro');
  const securityControl = element('High Security System');
  context.document = {
    querySelectorAll(selector) {
      if (selector === 'button, [role="button"]') return [profileMenu, accountMenu, securityControl];
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

console.log('Browser progress and provider failures verified');
