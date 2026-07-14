let polling = false;
let stopRequested = false;

chrome.runtime.onInstalled.addListener(() => resume());
chrome.runtime.onStartup.addListener(() => resume());

chrome.runtime.onMessage.addListener((message, _sender, sendResponse) => {
  if (message.type === 'start') {
    stopRequested = false;
    chrome.storage.local.set({ running: true }).then(() => {
      poll();
      sendResponse({ ok: true });
    });
    return true;
  }
  if (message.type === 'stop') {
    stopRequested = true;
    chrome.storage.local.set({ running: false }).then(() => sendResponse({ ok: true }));
    return true;
  }
  if (message.type === 'test') {
    testBridge().then(sendResponse);
    return true;
  }
});

async function resume() {
  const { running } = await chrome.storage.local.get({ running: false });
  if (running) poll();
}

async function testBridge() {
  const cfg = await settings();
  try {
    const response = await fetch(`${cfg.bridgeUrl}/health`, { cache: 'no-store' });
    const data = await response.json();
    return { ok: response.ok && data.ok, version: data.version };
  } catch (error) {
    return { ok: false, error: error.message };
  }
}

async function poll() {
  if (polling) return;
  polling = true;
  try {
    while (!stopRequested) {
      const cfg = await settings();
      if (!cfg.running || !cfg.token || !cfg.tabId) break;
      try {
        const response = await fetch(`${cfg.bridgeUrl}/v1/browser/jobs/next?wait=25&profile=${encodeURIComponent(cfg.profile)}`, {
          headers: { Authorization: `Bearer ${cfg.token}` }, cache: 'no-store'
        });
        if (response.status === 204) continue;
        if (!response.ok) throw new Error(`Bridge returned ${response.status}`);
        const work = await response.json();
        await processWork(cfg, work);
      } catch (error) {
        await delay(2500);
      }
    }
  } finally {
    polling = false;
  }
}

async function processWork(cfg, work) {
  let decision;
  try {
    const tab = await chrome.tabs.get(cfg.tabId);
    if (!matches(tab.url || '', work.profile.match_url || '')) throw new Error('Selected tab does not match the configured browser profile');
    const results = await chrome.scripting.executeScript({
      target: { tabId: cfg.tabId },
      func: automate,
      args: [work.job, work.profile]
    });
    const answer = results?.[0]?.result;
    if (!answer?.ok) throw new Error(answer?.error || 'No browser response was captured');
    decision = parseDecision(answer.text, work.profile.label || 'browser');
  } catch (error) {
    decision = { verdict: 'review', flags: ['browser_automation_error'], confidence: 0.4, model: work.profile.label || 'browser' };
  }
  await fetch(`${cfg.bridgeUrl}/v1/browser/jobs/${encodeURIComponent(work.job.id)}/complete`, {
    method: 'POST',
    headers: { Authorization: `Bearer ${cfg.token}`, 'Content-Type': 'application/json' },
    body: JSON.stringify(decision)
  });
}

function automate(job, profile) {
  const selectors = profile.selectors || {};
  const first = (items) => (items || []).map((selector) => document.querySelector(selector)).find(Boolean);
  const all = (items) => {
    for (const selector of items || []) {
      const found = [...document.querySelectorAll(selector)];
      if (found.length) return found;
    }
    return [];
  };
  const setInput = (element, value) => {
    element.focus();
    if (element instanceof HTMLTextAreaElement || element instanceof HTMLInputElement) {
      const setter = Object.getOwnPropertyDescriptor(Object.getPrototypeOf(element), 'value')?.set;
      setter ? setter.call(element, value) : (element.value = value);
    } else {
      element.textContent = value;
    }
    element.dispatchEvent(new InputEvent('input', { bubbles: true, inputType: 'insertText', data: value }));
    element.dispatchEvent(new Event('change', { bubbles: true }));
  };
  const addImage = (element, encoded, mediaType) => {
    const bytes = Uint8Array.from(atob(encoded), (char) => char.charCodeAt(0));
    const file = new File([bytes], `contextbridge.${(mediaType || 'image/png').split('/')[1] || 'png'}`, { type: mediaType || 'image/png' });
    const transfer = new DataTransfer();
    transfer.items.add(file);
    element.files = transfer.files;
    element.dispatchEvent(new Event('change', { bubbles: true }));
  };
  return new Promise(async (resolve) => {
    try {
      const input = first(selectors.input);
      if (!input) throw new Error('Input field not found');
      const before = all(selectors.response);
      const previousText = before.length ? (before[before.length - 1].innerText || before[before.length - 1].textContent || '') : '';
      if (job.image_base64) {
        const fileInput = first(selectors.file_input);
        if (!fileInput) throw new Error('Image input not found');
        addImage(fileInput, job.image_base64, job.image_media_type);
        await new Promise((done) => setTimeout(done, 1200));
      }
      setInput(input, job.prompt);
      await new Promise((done) => setTimeout(done, 250));
      const submit = first(selectors.submit);
      if (submit) submit.click();
      else input.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', code: 'Enter', bubbles: true }));

      const deadline = Date.now() + 120000;
      let stableText = '';
      let stableSince = 0;
      while (Date.now() < deadline) {
        await new Promise((done) => setTimeout(done, 750));
        const responses = all(selectors.response);
        const latest = responses.length ? (responses[responses.length - 1].innerText || responses[responses.length - 1].textContent || '').trim() : '';
        if (!latest || latest === previousText) continue;
        if (latest !== stableText) {
          stableText = latest;
          stableSince = Date.now();
          continue;
        }
        if (Date.now() - stableSince >= 1500 && latest.includes('{') && latest.includes('}')) {
          resolve({ ok: true, text: latest });
          return;
        }
      }
      throw new Error('Timed out waiting for a stable response');
    } catch (error) {
      resolve({ ok: false, error: error.message });
    }
  });
}

function parseDecision(text, model) {
  try {
    const start = text.indexOf('{');
    const end = text.lastIndexOf('}');
    if (start < 0 || end <= start) throw new Error('No JSON object');
    const parsed = JSON.parse(text.slice(start, end + 1));
    const verdict = ['allow', 'review'].includes(String(parsed.verdict).toLowerCase()) ? String(parsed.verdict).toLowerCase() : 'review';
    return {
      verdict,
      flags: Array.isArray(parsed.flags) ? parsed.flags.slice(0, 20).map(String) : [],
      confidence: Number.isFinite(Number(parsed.confidence)) ? Math.max(0, Math.min(1, Number(parsed.confidence))) : 0.5,
      model: String(parsed.model || model).slice(0, 80)
    };
  } catch (_) {
    return { verdict: 'review', flags: ['browser_invalid_json'], confidence: 0.4, model };
  }
}

function matches(url, pattern) {
  const escaped = pattern.replace(/[.+?^${}()|[\]\\]/g, '\\$&').replace(/\*/g, '.*');
  return new RegExp(`^${escaped}$`).test(url);
}

async function settings() {
  return chrome.storage.local.get({
    bridgeUrl: 'http://127.0.0.1:32145', token: '', profile: 'chatgpt', tabId: 0, running: false
  });
}

function delay(ms) { return new Promise((resolve) => setTimeout(resolve, ms)); }

resume();
