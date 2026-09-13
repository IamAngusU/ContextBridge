if (!globalThis.ContextBridgeProfiles && typeof importScripts === 'function') {
  importScripts('profiles.js');
}

const api = globalThis.browser || globalThis.chrome;

let stopRequested = false;
let heartbeatTimer = 0;
const pollers = new Map();
const busyTabs = new Set();

api.runtime.onInstalled.addListener(() => resume());
api.runtime.onStartup.addListener(() => resume());

api.runtime.onMessage.addListener((message, sender, sendResponse) => {
  Promise.resolve(handleMessage(message || {}, sender))
    .then((result) => sendResponse(result || { ok: true }))
    .catch((error) => sendResponse({ ok: false, error: error.message || String(error) }));
  return true;
});

async function handleMessage(message, sender) {
  switch (message.type) {
    case 'start':
      return startPairing();
    case 'stop':
      return stopPairing();
    case 'test':
      return testBridge();
    case 'status':
      return currentStatus();
    case 'start-teaching':
      return startTeaching(Number(message.tabId));
    case 'picker-complete':
      return saveTaughtProfile(message.profile, sender);
    case 'picker-cancelled':
      await api.storage.local.set({ teachingTabId: 0 });
      await sendHeartbeat('paused');
      return { ok: true };
    case 'verify-profile':
      return verifyTaughtProfile(Number(message.tabId));
    case 'scan-capabilities':
      return scanPageCapabilities(Number(message.tabId));
    case 'remove-profile':
      return removeTaughtProfile(String(message.origin || ''));
    default:
      return { ok: false, error: 'Unknown extension message' };
  }
}

async function startPairing() {
  const cfg = await settings();
  if (!cfg.token) throw new Error('Enter the local pairing token first');
  const tabIds = configuredTabIDs(cfg);
  if (!tabIds.length) throw new Error('Select at least one AI tab first');
  for (const tabId of tabIds) {
    const tab = await api.tabs.get(tabId);
    if (!tab?.url || !/^https?:/i.test(tab.url)) throw new Error('One selected tab is not a supported web page');
    if (cfg.useVisualProfile && !profileForTab(cfg, tab)) throw new Error(`Detect or customize ${tab.title || 'the selected page'} before starting the bridge`);
  }

  stopRequested = false;
  await api.storage.local.set({ running: true, teachingTabId: 0, lastError: '' });
  startHeartbeat();
  poll();
  await sendHeartbeat('waiting');
  return { ok: true };
}

async function stopPairing() {
  stopRequested = true;
  stopHeartbeat();
  await api.storage.local.set({ running: false, teachingTabId: 0 });
  await sendHeartbeat('paused');
  return { ok: true };
}

async function resume() {
  const { running } = await api.storage.local.get({ running: false });
  if (!running) return;
  stopRequested = false;
  startHeartbeat();
  poll();
}

async function testBridge() {
  const cfg = await settings();
  try {
    const response = await fetch(`${cfg.bridgeUrl}/health`, { cache: 'no-store' });
    const data = await response.json();
    return { ok: response.ok && data.ok, version: data.version || '' };
  } catch (error) {
    return { ok: false, error: error.message || 'Local bridge unavailable' };
  }
}

async function currentStatus() {
  const cfg = await settings();
  const tabs = [];
  for (const tabId of configuredTabIDs(cfg)) {
    try {
      const tab = await api.tabs.get(tabId);
      tabs.push({ ...tabSummary(tab), busy: busyTabs.has(tabId), profile: profileForTab(cfg, tab)?.name || '' });
    } catch (_) {}
  }
  const tab = tabs[0] || null;
  const profile = tab ? profileForTab(cfg, tab) : null;
  return {
    ok: true,
    running: cfg.running,
    tab,
    tabs,
    taught: Boolean(profile),
    profile,
    lastError: cfg.lastError || ''
  };
}

async function startTeaching(tabId) {
  if (!tabId) throw new Error('Select a tab to teach');
  const tab = await api.tabs.get(tabId);
  if (!tab?.url || !/^https?:/i.test(tab.url)) throw new Error('Only regular HTTP and HTTPS pages can be taught');
  const origin = new URL(tab.url).origin;
  const cfg = await settings();
  const existing = cfg.taughtProfiles[origin] || null;

  await api.scripting.executeScript({
    target: { tabId },
    files: ['picker.js']
  });
  await api.tabs.sendMessage(tabId, {
    type: 'contextbridge-picker-start',
    existing,
    extensionName: api.i18n.getMessage('extensionName') || 'ContextBridge'
  });
  await api.storage.local.set({ tabId, tabIds: [tabId], teachingTabId: tabId, running: false });
  stopRequested = true;
  stopHeartbeat();
  await sendHeartbeat('teaching');
  return { ok: true, tab: tabSummary(tab), existing: Boolean(existing) };
}

async function saveTaughtProfile(rawProfile, sender) {
  const tab = sender?.tab;
  if (!tab?.id || !tab.url || !/^https?:/i.test(tab.url)) throw new Error('The taught tab could not be verified');
  const origin = new URL(tab.url).origin;
  const profile = normalizeTaughtProfile(rawProfile, origin, tab.title || new URL(tab.url).hostname);
  const cfg = await settings();
  const taughtProfiles = { ...cfg.taughtProfiles, [origin]: profile };
  await api.storage.local.set({
    taughtProfiles,
    tabId: tab.id,
    tabIds: [tab.id],
    teachingTabId: 0,
    activeOrigin: origin,
    useVisualProfile: true,
    lastError: ''
  });
  await sendHeartbeat('ready');
  return { ok: true, profile };
}

async function verifyTaughtProfile(tabId) {
  if (!tabId) throw new Error('Select a tab first');
  const tab = await api.tabs.get(tabId);
  const cfg = await settings();
  const profile = profileForTab(cfg, tab);
  if (!profile) throw new Error('This page has not been taught yet');
  const results = await api.scripting.executeScript({
    target: { tabId },
    func: inspectSelectors,
    args: [profile.selectors]
  });
  const report = results?.[0]?.result || {};
  const verification = globalThis.ContextBridgeProfiles.verification(profile, report);
  return {
    ok: verification.ok,
    report: { ...report, response_pending: verification.responsePending },
    profile
  };
}

async function removeTaughtProfile(origin) {
  const cfg = await settings();
  const taughtProfiles = { ...cfg.taughtProfiles };
  delete taughtProfiles[origin];
  await api.storage.local.set({ taughtProfiles });
  return { ok: true };
}

async function scanPageCapabilities(tabId) {
  if (!tabId) throw new Error('Select an AI tab first');
  if (busyTabs.has(tabId)) throw new Error('Wait until this tab finishes its current job');
  const results = await api.scripting.executeScript({ target: { tabId }, func: discoverPageCapabilities });
  const capabilities = results?.[0]?.result || {};
  const cfg = await settings();
  const tabCapabilities = { ...(cfg.tabCapabilities || {}), [tabId]: capabilities };
  await api.storage.local.set({ tabCapabilities });
  return { ok: true, capabilities };
}

function normalizeTaughtProfile(raw, origin, fallbackLabel) {
  const selectors = raw?.selectors || {};
  const normalized = {
    input: cleanSelectors(selectors.input),
    submit: cleanSelectors(selectors.submit),
    response: cleanSelectors(selectors.response),
    file_input: cleanSelectors(selectors.file_input)
  };
  if (!normalized.input.length || !normalized.response.length) throw new Error('Prompt and response targets are required');
  return {
    name: `visual-${new URL(origin).hostname.replace(/[^a-z0-9]+/gi, '-').toLowerCase()}`,
    label: String(raw?.label || fallbackLabel || new URL(origin).hostname).slice(0, 100),
    origin,
    match_url: `${origin}/*`,
    selectors: normalized,
    learned_at: new Date().toISOString(),
    source: 'visual'
  };
}

function cleanSelectors(value) {
  if (!Array.isArray(value)) return [];
  return [...new Set(value.map((item) => String(item).trim()).filter(Boolean))].slice(0, 8);
}

function profileForTab(cfg, tab) {
  if (!tab?.url || !/^https?:/i.test(tab.url)) return null;
  try {
    return cfg.taughtProfiles[new URL(tab.url).origin] || globalThis.ContextBridgeProfiles?.forURL(tab.url) || null;
  } catch (_) {
    return null;
  }
}

async function poll() {
  const cfg = await settings();
  if (!cfg.running || !cfg.token) return;
  for (const tabId of configuredTabIDs(cfg)) {
    if (pollers.has(tabId)) continue;
    const running = pollTab(tabId).finally(() => pollers.delete(tabId));
    pollers.set(tabId, running);
  }
}

async function pollTab(tabId) {
  while (!stopRequested) {
      const cfg = await settings();
      if (!cfg.running || !cfg.token || !configuredTabIDs(cfg).includes(tabId)) break;
      if (Number(cfg.tabCooldowns?.[tabId] || 0) > Date.now()) {
        await delay(2000);
        continue;
      }
      if (busyTabs.has(tabId)) {
        await delay(250);
        continue;
      }
      await flushPendingCompletions(cfg);
      try {
        let requestedProfile = cfg.profile || '';
        if (cfg.useVisualProfile) {
          try {
            const selectedTab = await api.tabs.get(tabId);
            requestedProfile = profileForTab(cfg, selectedTab)?.name || requestedProfile;
          } catch (_) {}
        }
        const response = await fetch(`${cfg.bridgeUrl}/v1/browser/jobs/next?wait=25&profile=${encodeURIComponent(requestedProfile)}`, {
          headers: { Authorization: `Bearer ${cfg.token}` },
          cache: 'no-store'
        });
        if (response.status === 204) continue;
        if (!response.ok) throw new Error(`Bridge returned ${response.status}`);
        const work = await response.json();
        if (cfg.pendingCompletions[work?.job?.id]) {
          await completeWork(cfg, work.job.id, cfg.pendingCompletions[work.job.id]);
          continue;
        }
        await processWork(cfg, work, tabId);
      } catch (error) {
        await api.storage.local.set({ lastError: error.message || String(error) });
        await delay(2000);
      }
  }
}

async function processWork(cfg, work, claimedTabId) {
  let decision;
  let tab;
  let effectiveProfile = work.profile || {};
  let leaseTimer = 0;
  let progressTimer = 0;
  let progressBusy = false;
  let progressSequence = 0;
  let baselineText = '';
  let latestProgressText = '';
  let latestProgressState = '';
  let tabId = claimedTabId;
  let tabSlotHeld = false;
  let failureCode = 'browser_automation_error';
  await api.storage.local.set({ lastError: '' });
  try {
    tabId = await resolveWorkTab(cfg, work, claimedTabId);
    await waitForTabSlot(tabId, work.deadline);
    tabSlotHeld = true;
    tab = await api.tabs.get(tabId);
    const taught = cfg.useVisualProfile ? profileForTab(cfg, tab) : null;
    if (taught) effectiveProfile = taught;
    if (!effectiveProfile?.selectors) throw new Error('No usable page profile is available');
    if (!matches(tab.url || '', effectiveProfile.match_url || '')) throw new Error('The selected tab no longer matches its taught page');
    await sendHeartbeat('working');
    leaseTimer = setInterval(() => renewLease(cfg, work.job.id), 25000);
    const initial = await captureTabProgress(tabId, effectiveProfile.selectors);
    baselineText = initial.text || '';
    latestProgressText = baselineText;
    latestProgressState = `${Boolean(initial.busy)}|${Number(initial.percent) || 0}|${initial.detail || ''}`;
    const sample = async () => {
      if (progressBusy) return;
      progressBusy = true;
      try {
        const snapshot = await captureTabProgress(tabId, effectiveProfile.selectors);
        const text = String(snapshot.text || '').trim();
        const textChanged = Boolean(text && text !== baselineText && text !== latestProgressText);
        const progressState = `${Boolean(snapshot.busy)}|${Number(snapshot.percent) || 0}|${snapshot.detail || ''}`;
        if (textChanged || progressState !== latestProgressState) {
          if (textChanged) latestProgressText = text;
          latestProgressState = progressState;
          progressSequence += 1;
          await reportProgress(cfg, work.job.id, {
            sequence: progressSequence,
            text: textChanged ? text.slice(0, 1024 * 1024) : '',
            phase: snapshot.busy ? 'generating' : 'stabilizing',
            detail: snapshot.detail || '',
            percent: Number(snapshot.percent) || 0,
            busy: Boolean(snapshot.busy)
          });
        }
      } catch (_) {
      } finally {
        progressBusy = false;
      }
    };
    progressTimer = setInterval(sample, 800);
    let answer;
    try {
      const results = await api.scripting.executeScript({ target: { tabId }, func: automate, args: [work.job, effectiveProfile, work.deadline] });
      answer = results?.[0]?.result;
    } catch (error) {
      failureCode = 'browser_navigation_interrupted';
      progressSequence += 1;
      await reportProgress(cfg, work.job.id, { sequence: progressSequence, text: '', phase: 'recovering', detail: 'The tab navigated; reattaching to the conversation', busy: true });
      await waitForTabReady(tabId, 30000);
      const resumeJob = { ...work.job, metadata: { ...(work.job.metadata || {}), contextbridge_resume_only: true, contextbridge_baseline_text: baselineText } };
      const resumed = await api.scripting.executeScript({ target: { tabId }, func: automate, args: [resumeJob, effectiveProfile, work.deadline] });
      answer = resumed?.[0]?.result;
    }
    if (!answer?.ok && answer?.recoverable) {
      progressSequence += 1;
      await reportProgress(cfg, work.job.id, { sequence: progressSequence, text: '', phase: 'recovering', detail: answer.error || 'Recovering browser tab', percent: Number(answer.percent) || 0, busy: true });
      await api.tabs.reload(tabId);
      await waitForTabReady(tabId, 30000);
      const resumeJob = { ...work.job, metadata: { ...(work.job.metadata || {}), contextbridge_resume_only: true, contextbridge_baseline_text: baselineText } };
      const resumed = await api.scripting.executeScript({ target: { tabId }, func: automate, args: [resumeJob, effectiveProfile, work.deadline] });
      answer = resumed?.[0]?.result;
    }
    if (!answer?.ok) {
      failureCode = /^browser_[a-z_]+$/.test(String(answer?.code || '')) ? answer.code : failureCode;
      if (failureCode === 'browser_rate_limited') {
		await coolDownTab(tabId, 5 * 60 * 1000);
        progressSequence += 1;
        await reportProgress(cfg, work.job.id, { sequence: progressSequence, text: '', phase: 'rate_limited', detail: answer?.error || 'Provider rate limit', busy: false });
      }
      throw new Error(answer?.error || 'No browser response was captured');
    }
    if (answer.text && answer.text !== latestProgressText) {
      progressSequence += 1;
      await reportProgress(cfg, work.job.id, {
        sequence: progressSequence,
        text: String(answer.text).slice(0, 1024 * 1024),
        phase: 'final',
        busy: false
      });
    }
    decision = parseOutput(answer.text, work.job.output || {}, answer.selected_model || work.job.model || effectiveProfile.label || 'browser', answer.artifacts || []);
  } catch (error) {
    const mode = outputMode(work.job.output || {});
    decision = mode === 'decision'
      ? { verdict: 'review', flags: [failureCode], confidence: 0.4, model: effectiveProfile?.label || 'browser' }
      : { mode, error: failureCode, model: effectiveProfile?.label || 'browser' };
    await api.storage.local.set({ lastError: error.message || String(error) });
  } finally {
    if (leaseTimer) clearInterval(leaseTimer);
    if (progressTimer) clearInterval(progressTimer);
    if (tabSlotHeld) busyTabs.delete(tabId);
  }

  const pendingCompletions = { ...cfg.pendingCompletions, [work.job.id]: decision };
  await api.storage.local.set({ pendingCompletions });
  await completeWork(await settings(), work.job.id, decision);
  await sendHeartbeat('waiting');
}

async function coolDownTab(tabId, duration) {
  const cfg = await settings();
  const tabCooldowns = { ...(cfg.tabCooldowns || {}), [tabId]: Date.now() + duration };
  await api.storage.local.set({ tabCooldowns });
}

async function resolveWorkTab(cfg, work, claimedTabId) {
  const session = String(work?.job?.session_id || '').trim();
  if (!session) return claimedTabId;
  const sessionTabs = { ...(cfg.sessionTabs || {}) };
  const mapped = Number(sessionTabs[session] || 0);
  if (mapped && configuredTabIDs(cfg).includes(mapped)) {
    try {
      const tab = await api.tabs.get(mapped);
      const requested = String(work?.profile?.name || '');
      const actual = profileForTab(cfg, tab)?.name || '';
      if (!requested || requested === actual) return mapped;
    } catch (_) {}
  }
  sessionTabs[session] = claimedTabId;
  const entries = Object.entries(sessionTabs).slice(-200);
  await api.storage.local.set({ sessionTabs: Object.fromEntries(entries) });
  return claimedTabId;
}

async function waitForTabSlot(tabId, deadlineValue) {
  const deadline = Math.min(Date.now() + 300000, Date.parse(deadlineValue || '') || Date.now() + 300000);
  while (busyTabs.has(tabId) && Date.now() < deadline) await delay(250);
  if (busyTabs.has(tabId)) throw new Error('The session tab stayed busy until the job deadline');
  busyTabs.add(tabId);
}

async function waitForTabReady(tabId, timeout) {
  const deadline = Date.now() + timeout;
  while (Date.now() < deadline) {
    try {
      const tab = await api.tabs.get(tabId);
      if (tab.status === 'complete') {
        await delay(600);
        return;
      }
    } catch (_) {}
    await delay(250);
  }
  throw new Error('The browser tab did not finish reloading');
}

async function captureTabProgress(tabId, selectors) {
  const results = await api.scripting.executeScript({
    target: { tabId },
    func: captureProgress,
    args: [selectors]
  });
  return results?.[0]?.result || { text: '', busy: false };
}

async function reportProgress(cfg, jobId, progress) {
  try {
    const response = await fetch(`${cfg.bridgeUrl}/v1/browser/jobs/${encodeURIComponent(jobId)}/progress`, {
      method: 'POST',
      headers: {
        Authorization: `Bearer ${cfg.token}`,
        'Content-Type': 'application/json'
      },
      body: JSON.stringify(progress)
    });
    if (!response.ok && response.status !== 409) throw new Error(`Could not stream browser progress: ${response.status}`);
  } catch (_) {}
}

async function renewLease(cfg, jobId) {
  try {
    await fetch(`${cfg.bridgeUrl}/v1/browser/jobs/${encodeURIComponent(jobId)}/lease`, {
      method: 'POST',
      headers: { Authorization: `Bearer ${cfg.token}` },
      cache: 'no-store'
    });
  } catch (_) {}
}

async function completeWork(cfg, jobId, decision) {
  const response = await fetch(`${cfg.bridgeUrl}/v1/browser/jobs/${encodeURIComponent(jobId)}/complete`, {
    method: 'POST',
    headers: {
      Authorization: `Bearer ${cfg.token}`,
      'Content-Type': 'application/json'
    },
    body: JSON.stringify(decision)
  });
  if (!response.ok && response.status !== 409) throw new Error(`Could not return browser result: ${response.status}`);
  const latest = await settings();
  const pendingCompletions = { ...latest.pendingCompletions };
  delete pendingCompletions[jobId];
  await api.storage.local.set({ pendingCompletions });
}

async function flushPendingCompletions(cfg) {
  for (const [jobId, decision] of Object.entries(cfg.pendingCompletions)) {
    try {
      await completeWork(await settings(), jobId, decision);
    } catch (_) {
      break;
    }
  }
}

function automate(job, profile, jobDeadline) {
  const selectors = profile.selectors || {};
  const isVisible = (element) => Boolean(element && (element.offsetWidth || element.offsetHeight || element.getClientRects().length));
  const first = (items) => (items || []).map((selector) => {
    try { return [...document.querySelectorAll(selector)].find(isVisible) || null; } catch (_) { return null; }
  }).find(Boolean);
  const all = (items) => {
    for (const selector of items || []) {
      try {
        const found = [...document.querySelectorAll(selector)];
        if (found.length) return found;
      } catch (_) {}
    }
    return [];
  };
  const visibleText = (element) => (element?.innerText || element?.textContent || '').trim();
  const pageState = () => {
    let percent = 0;
    let detail = '';
    for (const indicator of document.querySelectorAll('[data-testid="image-gen-loading-progress"], [role="progressbar"][aria-valuenow]')) {
      if (!isVisible(indicator)) continue;
      percent = Math.max(percent, Math.max(0, Math.min(100, Number(indicator.getAttribute('aria-valuenow')) || 0)));
      detail = 'Image generation';
    }
    for (const selector of [
      '[aria-busy="true"]', '[data-is-streaming="true"]', '.result-streaming',
      '[data-testid="image-gen-loading-state"]', '[data-testid="image-gen-loading-state-frame"]',
      'button[data-testid*="stop" i]', 'button[aria-label*="stop" i]',
      'button[aria-label*="beenden" i]', 'button[aria-label*="abbrechen" i]'
    ]) {
      try {
        if ([...document.querySelectorAll(selector)].some(isVisible)) return { busy: true, percent, detail: detail || 'Generating' };
      } catch (_) {}
    }
    return { busy: false, percent, detail: '', composerReady: Boolean(first(selectors.submit)) };
  };
  const pageBusy = () => pageState().busy;
  const providerError = (responseElement) => {
    const containers = [];
    for (const selector of ['[role="alert"]', '[aria-live="assertive"]', '[data-testid*="error" i]', '.toast-error', '.error-message']) {
      try { containers.push(...[...document.querySelectorAll(selector)].filter(isVisible)); } catch (_) {}
    }
	if (responseElement) {
		try { containers.push(...[...responseElement.querySelectorAll('[data-testid*="error" i], [data-testid*="rate" i], [class*="error" i]')].filter(isVisible)); } catch (_) {}
	}
    const text = containers.map(visibleText).filter(Boolean).join('\n').slice(0, 4000);
	const retryVisible = [...document.querySelectorAll('button')].filter(isVisible).some((button) => /retry|try again|regenerate|erneut|noch einmal|wiederholen/.test(`${visibleText(button)} ${button.getAttribute('aria-label') || ''}`.toLowerCase()));
	const responseText = responseElement ? visibleText(responseElement).slice(0, 1500) : '';
	const semanticText = text || (retryVisible ? responseText : '');
    if (!semanticText) return null;
    const lower = semanticText.toLowerCase();
    if (/rate.?limit|usage.?limit|too many requests|quota|capacity|limit erreicht|nutzungslimit|zu viele anfragen|später erneut|try again later|temporarily unavailable/.test(lower)) {
      return { code: 'browser_rate_limited', message: semanticText.slice(0, 300), retryable: true };
    }
    if (/something went wrong|etwas ist schief|network error|verbindungsfehler|failed to (?:generate|respond)|antwort konnte nicht|generation failed/.test(lower)) {
      return { code: 'browser_provider_error', message: semanticText.slice(0, 300), retryable: true };
    }
    return null;
  };
  const setInput = (element, value) => {
    element.focus();
    if (element instanceof HTMLTextAreaElement || element instanceof HTMLInputElement) {
      const prototype = element instanceof HTMLTextAreaElement ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
      const setter = Object.getOwnPropertyDescriptor(prototype, 'value')?.set;
      setter ? setter.call(element, value) : (element.value = value);
    } else {
      const selection = window.getSelection();
      const range = document.createRange();
      range.selectNodeContents(element);
      selection.removeAllRanges();
      selection.addRange(range);
      document.execCommand('insertText', false, value);
      if (!visibleText(element)) element.textContent = value;
    }
    element.dispatchEvent(new InputEvent('input', { bubbles: true, inputType: 'insertText', data: value }));
    element.dispatchEvent(new Event('change', { bubbles: true }));
  };
  const addImage = (element, encoded, mediaType) => {
    const bytes = Uint8Array.from(atob(encoded), (char) => char.charCodeAt(0));
    const extension = (mediaType || 'image/png').split('/')[1]?.replace(/[^a-z0-9]/gi, '') || 'png';
    const file = new File([bytes], `contextbridge.${extension}`, { type: mediaType || 'image/png' });
    const transfer = new DataTransfer();
    transfer.items.add(file);
    element.files = transfer.files;
    element.dispatchEvent(new Event('input', { bubbles: true }));
    element.dispatchEvent(new Event('change', { bubbles: true }));
  };
  const wait = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
	const normalizedWords = (value) => String(value || '').toLowerCase().normalize('NFKD').replace(/[^a-z0-9äöüß]+/g, ' ').trim().split(/\s+/).filter(Boolean);
	const preferenceTerms = (kind, value) => {
		const normalized = String(value || '').trim().toLowerCase();
		if (kind === 'reasoning') {
			const aliases = {
				instant: ['instant', 'sofort', 'fast', 'schnell'], low: ['low', 'niedrig'], medium: ['medium', 'mittel'],
				high: ['high', 'hoch'], xhigh: ['xhigh', 'very high', 'sehr hoch'], max: ['max', 'maximum'], pro: ['pro']
			};
			return aliases[normalized] || normalizedWords(normalized);
		}
		return normalizedWords(normalized).filter((word) => word !== 'gpt' && word !== 'model' && word !== 'modell');
	};
	const choosePreference = async (kind, requested) => {
		if (!requested || ['auto', 'default'].includes(String(requested).toLowerCase())) return '';
		const triggerSelectors = kind === 'model'
			? ['button[data-testid*="model" i]', 'button[aria-label*="model" i]', 'button[aria-haspopup="menu"]']
			: ['button[data-testid*="reason" i]', 'button[data-testid*="effort" i]', 'button[aria-label*="reason" i]', 'button[aria-label*="denk" i]', 'button[aria-haspopup="menu"]'];
		const requestedTerms = preferenceTerms(kind, requested);
		const preferenceMatches = (element) => {
			const label = `${visibleText(element)} ${element.getAttribute('aria-label') || ''}`;
			const normalized = normalizedWords(label).join(' ');
			return kind === 'reasoning'
				? requestedTerms.some((term) => normalized.includes(normalizedWords(term).join(' ')))
				: requestedTerms.every((term) => normalizedWords(label).includes(term));
		};
		const triggers = triggerSelectors.flatMap((selector) => { try { return [...document.querySelectorAll(selector)].filter(isVisible); } catch (_) { return []; } });
		const current = triggers.find(preferenceMatches);
		if (current) return visibleText(current) || requested;
		const trigger = triggers.find((element) => {
			const label = `${visibleText(element)} ${element.getAttribute('aria-label') || ''}`.toLowerCase();
			return kind === 'model' ? /gpt|gemini|model|modell|astra|sol|terra|luna/.test(label) : /reason|denk|effort|thinking|sofort|instant|hoch|high|pro|max/.test(label);
		});
		if (!trigger) throw new Error(`The ${kind} selector is not visible in this provider UI`);
		trigger.click();
		await wait(350);
		const options = [...document.querySelectorAll('[role="menuitem"], [role="option"], [data-radix-collection-item], [aria-checked], [aria-selected]')].filter(isVisible);
		const match = options.find(preferenceMatches);
		if (!match) {
			document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', code: 'Escape', bubbles: true }));
			throw new Error(`Requested ${kind} "${requested}" is not available in this chat`);
		}
		match.click();
		await wait(350);
		return visibleText(match) || requested;
	};
	const cleanFileName = (value, fallback) => {
		const clean = String(value || '').split(/[\\/]/).pop().replace(/[\u0000-\u001f<>:"|?*]/g, '-').trim().slice(0, 180);
		return clean && clean !== '.' ? clean : fallback;
	};
	const extensionFor = (mediaType) => ({
		'image/png': 'png', 'image/jpeg': 'jpg', 'image/webp': 'webp', 'image/gif': 'gif',
		'image/svg+xml': 'svg', 'application/pdf': 'pdf', 'application/zip': 'zip',
		'application/json': 'json', 'text/csv': 'csv', 'text/plain': 'txt'
	}[String(mediaType || '').toLowerCase()] || 'bin');
	const mediaTypeFor = (url, fallback) => {
		const value = String(url || '').split(/[?#]/)[0].toLowerCase();
		if (value.endsWith('.png')) return 'image/png';
		if (/\.jpe?g$/.test(value)) return 'image/jpeg';
		if (value.endsWith('.webp')) return 'image/webp';
		if (value.endsWith('.gif')) return 'image/gif';
		if (value.endsWith('.svg')) return 'image/svg+xml';
		if (value.endsWith('.pdf')) return 'application/pdf';
		if (value.endsWith('.zip')) return 'application/zip';
		if (value.endsWith('.json')) return 'application/json';
		if (value.endsWith('.csv')) return 'text/csv';
		if (/\.(txt|md|js|ts|tsx|jsx|py|go|rs|java|c|cpp|h|css|html|xml|ya?ml)$/.test(value)) return 'text/plain';
		return fallback || 'application/octet-stream';
	};
	const bytesToBase64 = (bytes) => {
		let binary = '';
		for (let offset = 0; offset < bytes.length; offset += 0x8000) {
			binary += String.fromCharCode(...bytes.subarray(offset, Math.min(offset + 0x8000, bytes.length)));
		}
		return btoa(binary);
	};
	const digestHex = async (bytes) => {
		const digest = await crypto.subtle.digest('SHA-256', bytes);
		return [...new Uint8Array(digest)].map((value) => value.toString(16).padStart(2, '0')).join('');
	};
	const collectArtifacts = async (responseElement, spec) => {
		if (!spec?.artifacts || !responseElement) return [];
		const maxBytes = Math.max(1024, Math.min(Number(spec.max_artifact_bytes) || 12 * 1024 * 1024, 12 * 1024 * 1024));
		const candidates = [];
		const seen = new Set();
		const add = (url, name, mediaType) => {
			url = String(url || '').trim();
			if (!url || seen.has(url) || !/^(https:|blob:|data:)/i.test(url)) return;
			seen.add(url);
			candidates.push({ url, name, mediaType });
		};
		for (const image of responseElement.querySelectorAll('img')) {
			if ((image.naturalWidth && image.naturalWidth < 128) || (image.naturalHeight && image.naturalHeight < 128)) continue;
			const source = image.currentSrc || image.src;
			const mediaType = mediaTypeFor(source, 'image/png');
			let name = '';
			try { name = new URL(source, location.href).pathname.split('/').pop(); } catch (_) {}
			if (!/\.[a-z0-9]{2,5}$/i.test(name)) name = `image-${candidates.length + 1}.${extensionFor(mediaType)}`;
			add(source, cleanFileName(name, `image-${candidates.length + 1}.${extensionFor(mediaType)}`), mediaType);
		}
		for (const anchor of responseElement.querySelectorAll('a[href]')) {
			const href = anchor.href;
			const label = `${anchor.download || ''} ${anchor.getAttribute('aria-label') || ''} ${visibleText(anchor)}`.toLowerCase();
			const path = (() => { try { return new URL(href, location.href).pathname; } catch (_) { return ''; } })();
			if (!anchor.hasAttribute('download') && !/download|herunterladen|save|speichern/.test(label) && !/\.(pdf|zip|json|csv|txt|md|docx|xlsx|pptx|png|jpe?g|webp|gif)(?:$|[?#])/i.test(href)) continue;
			const mediaType = mediaTypeFor(href);
			add(href, cleanFileName(anchor.download || path.split('/').pop(), `file-${candidates.length + 1}.${extensionFor(mediaType)}`), mediaType);
		}
		for (const citation of responseElement.querySelectorAll('[data-file-citation-primary-file-id]')) {
			const fileId = String(citation.getAttribute('data-file-citation-primary-file-id') || '');
			if (!/^file_[a-z0-9]+$/i.test(fileId)) continue;
			const label = citation.querySelector('p')?.textContent || citation.querySelector('button')?.textContent || '';
			const endpoint = `${location.origin}/backend-api/files/${encodeURIComponent(fileId)}/download`;
			add(endpoint, cleanFileName(label, `file-${candidates.length + 1}.bin`), mediaTypeFor(label));
		}
		const artifacts = [];
		let total = 0;
		for (const candidate of candidates.slice(0, 12)) {
			const artifact = { name: candidate.name, media_type: candidate.mediaType };
			try {
				const response = await fetch(candidate.url, { credentials: 'include' });
				if (!response.ok) throw new Error(`HTTP ${response.status}`);
				const blob = await response.blob();
				if (!blob.size || total + blob.size > maxBytes) throw new Error('artifact exceeds transfer limit');
				const bytes = new Uint8Array(await blob.arrayBuffer());
				artifact.media_type = mediaTypeFor(candidate.url, blob.type || candidate.mediaType);
				artifact.size = bytes.length;
				artifact.sha256 = await digestHex(bytes);
				artifact.data_base64 = bytesToBase64(bytes);
				total += bytes.length;
			} catch (_) {
				if (/^https:/i.test(candidate.url)) artifact.url = candidate.url;
			}
			if (artifact.data_base64 || artifact.url) artifacts.push(artifact);
		}
		for (const code of [...responseElement.querySelectorAll('pre code')].slice(0, Math.max(0, 12 - artifacts.length))) {
			const content = String(code.textContent || '');
			if (!content.trim()) continue;
			const bytes = new TextEncoder().encode(content);
			if (total + bytes.length > maxBytes) continue;
			const language = [...code.classList].map((item) => item.match(/(?:language-|lang-)([a-z0-9_+-]+)/i)?.[1]).find(Boolean) || 'txt';
			artifacts.push({
				name: cleanFileName(`code-${artifacts.length + 1}.${language}`, `code-${artifacts.length + 1}.txt`),
				media_type: 'text/plain', size: bytes.length,
				sha256: await digestHex(bytes), data_base64: bytesToBase64(bytes)
			});
			total += bytes.length;
		}
		return artifacts;
	};

  return new Promise(async (resolve) => {
    try {
      const input = first(selectors.input);
      if (!input) throw new Error('Prompt input was not found');
      const before = all(selectors.response);
      const previousElement = before.length ? before[before.length - 1] : null;
      const previousText = String(job.metadata?.contextbridge_baseline_text || (before.length ? visibleText(before[before.length - 1]) : ''));
      const resumeOnly = Boolean(job.metadata?.contextbridge_resume_only);
      const selectedModel = !resumeOnly && job.model ? await choosePreference('model', job.model) : String(job.model || '');
      const selectedReasoning = !resumeOnly && job.reasoning ? await choosePreference('reasoning', job.reasoning) : String(job.reasoning || '');
      if (!resumeOnly && job.image_base64) {
        const fileInput = first(selectors.file_input);
        if (!fileInput) throw new Error('This job has an image, but no image input was taught');
        addImage(fileInput, job.image_base64, job.image_media_type);
        await wait(1000);
      }
      if (!resumeOnly) {
        setInput(input, job.prompt);
        await wait(300);
        let submit = first(selectors.submit);
        for (let attempt = 0; !submit && attempt < 8; attempt += 1) {
          await wait(150);
          submit = first(selectors.submit);
        }
        if (submit) {
          submit.click();
        } else {
          input.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', code: 'Enter', bubbles: true }));
          input.dispatchEvent(new KeyboardEvent('keyup', { key: 'Enter', code: 'Enter', bubbles: true }));
        }
      }

		const suppliedDeadline = Date.parse(jobDeadline || '');
		const defaultWait = job.output?.artifacts ? 300000 : 180000;
		const deadline = Number.isFinite(suppliedDeadline)
			? Math.min(Date.now() + defaultWait, suppliedDeadline - 1000)
			: Date.now() + defaultWait;
      let stableText = '';
      let stableSince = 0;
      let sawBusy = false;
      let lastPercent = 0;
      let progressChangedAt = Date.now();
      while (Date.now() < deadline) {
        await wait(650);
        const responses = all(selectors.response);
        const latestElement = responses.length ? responses[responses.length - 1] : null;
        const latest = visibleText(latestElement);
        const changedResponse = latestElement !== previousElement || responses.length > before.length || latest !== previousText;
        const state = pageState();
        const busy = state.busy;
        sawBusy = sawBusy || busy;
		if (state.percent !== lastPercent) {
			lastPercent = state.percent;
			progressChangedAt = Date.now();
		}
		if (busy && lastPercent >= 95 && Date.now() - progressChangedAt > 45000) {
			resolve({ ok: false, error: `Image generation stalled at ${lastPercent}%`, code: 'stalled_generation', percent: lastPercent, recoverable: !resumeOnly && job.metadata?.contextbridge_auto_reload !== false });
			return;
		}
		const providerFailure = providerError(latestElement);
		if (providerFailure && !busy) {
			resolve({ ok: false, error: providerFailure.message, code: providerFailure.code, retryable: providerFailure.retryable });
			return;
		}
		const artifactCount = job.output?.artifacts && latestElement
			? [...latestElement.querySelectorAll('img')].filter((image) => (!image.naturalWidth || image.naturalWidth >= 128) && (!image.naturalHeight || image.naturalHeight >= 128)).length
				+ latestElement.querySelectorAll('a[download], pre code, [data-file-citation-primary-file-id]').length
			: 0;
		const stableValue = latest || (artifactCount ? `artifact:${artifactCount}` : '');
		if (!stableValue || !changedResponse) continue;
		if (stableValue !== stableText) {
			stableText = stableValue;
          stableSince = Date.now();
          continue;
        }
        const mode = String(job.output?.mode || 'decision').toLowerCase();
        const structured = mode === 'text' || ((latest.includes('{') && latest.includes('}')) || (latest.includes('[') && latest.includes(']')));
        const stableFor = sawBusy ? 1300 : 2600;
		const composerFinished = !(selectors.submit || []).length || state.composerReady;
        if (Date.now() - stableSince >= stableFor && structured && !busy && composerFinished) {
		  const artifacts = await collectArtifacts(latestElement, job.output || {});
		  resolve({ ok: true, text: latest || `Generated ${artifacts.length} artifact(s).`, artifacts, selected_model: selectedModel, selected_reasoning: selectedReasoning });
          return;
        }
      }
      resolve({ ok: false, error: 'Timed out while waiting for a stable response', code: 'browser_timeout', recoverable: false, percent: lastPercent });
      return;
    } catch (error) {
      const message = error.message || String(error);
      const code = /requested model|model selector/i.test(message)
		? 'browser_model_unavailable'
		: (/requested reasoning|reasoning selector/i.test(message) ? 'browser_reasoning_unavailable' : 'browser_automation_error');
      resolve({ ok: false, error: message, code });
    }
  });
}

function captureProgress(selectors) {
  const visibleText = (element) => (element?.innerText || element?.textContent || '').trim();
  const isVisible = (element) => Boolean(element && (element.offsetWidth || element.offsetHeight || element.getClientRects().length));
  let responses = [];
  for (const selector of selectors?.response || []) {
    try {
      responses = [...document.querySelectorAll(selector)].filter(isVisible);
      if (responses.length) break;
    } catch (_) {}
  }
  let busy = false;
  let percent = 0;
  let detail = '';
  for (const indicator of document.querySelectorAll('[data-testid="image-gen-loading-progress"], [role="progressbar"][aria-valuenow]')) {
    if (!isVisible(indicator)) continue;
    percent = Math.max(percent, Math.max(0, Math.min(100, Number(indicator.getAttribute('aria-valuenow')) || 0)));
    detail = 'Image generation';
  }
  for (const selector of [
    '[aria-busy="true"]', '[data-is-streaming="true"]', '.result-streaming',
    '[data-testid="image-gen-loading-state"]', '[data-testid="image-gen-loading-state-frame"]',
    'button[data-testid*="stop" i]', 'button[aria-label*="stop" i]',
    'button[aria-label*="beenden" i]', 'button[aria-label*="abbrechen" i]'
  ]) {
    try {
      if ([...document.querySelectorAll(selector)].some(isVisible)) {
        busy = true;
        break;
      }
    } catch (_) {}
  }
  return { text: responses.length ? visibleText(responses[responses.length - 1]) : '', busy, percent, detail: detail || (busy ? 'Generating' : '') };
}

function inspectSelectors(selectors) {
  const count = (items) => {
    for (const selector of items || []) {
      try {
        const amount = document.querySelectorAll(selector).length;
        if (amount) return amount;
      } catch (_) {}
    }
    return 0;
  };
  return {
    input: count(selectors.input),
    submit: count(selectors.submit),
    response: count(selectors.response),
    file_input: count(selectors.file_input),
    enterFallback: true
  };
}

function parseDecision(text, model) {
  try {
    const start = text.indexOf('{');
    const end = text.lastIndexOf('}');
    if (start < 0 || end <= start) throw new Error('No JSON object');
    const parsed = JSON.parse(text.slice(start, end + 1));
    const verdict = ['allow', 'review'].includes(String(parsed.verdict).toLowerCase())
      ? String(parsed.verdict).toLowerCase()
      : 'review';
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

function parseOutput(text, spec, model, artifacts = []) {
  const mode = outputMode(spec);
	if (mode === 'decision') return { ...parseDecision(text, model), artifacts };
  const clean = String(text || '').trim();
	if (mode === 'text') return { mode, text: clean, model, artifacts };
  try {
    const objectStart = clean.indexOf('{');
    const arrayStart = clean.indexOf('[');
    const start = objectStart < 0 ? arrayStart : (arrayStart < 0 ? objectStart : Math.min(objectStart, arrayStart));
    const end = start >= 0 && clean[start] === '[' ? clean.lastIndexOf(']') : clean.lastIndexOf('}');
    if (start < 0 || end <= start) throw new Error('No JSON value');
    const json = JSON.parse(clean.slice(start, end + 1));
	return { mode, json, model, artifacts };
  } catch (_) {
	return { mode, error: 'browser_invalid_json', model, artifacts };
  }
}

function outputMode(spec) {
  const mode = String(spec?.mode || '').trim().toLowerCase();
  return ['decision', 'json', 'text'].includes(mode) ? mode : 'decision';
}

function matches(url, pattern) {
  if (!pattern) return true;
  const escaped = pattern.replace(/[.+?^${}()|[\]\\]/g, '\\$&').replace(/\*/g, '.*');
  return new RegExp(`^${escaped}$`).test(url);
}

function startHeartbeat() {
  stopHeartbeat();
  heartbeatTimer = setInterval(() => sendHeartbeat('waiting'), 12000);
}

function stopHeartbeat() {
  if (heartbeatTimer) clearInterval(heartbeatTimer);
  heartbeatTimer = 0;
}

async function sendHeartbeat(state) {
  const cfg = await settings();
  if (!cfg.token) return;
  const tabs = [];
  for (const tabId of configuredTabIDs(cfg)) {
    try {
      const tab = await api.tabs.get(tabId);
      const profile = profileForTab(cfg, tab);
      let capabilities = cfg.tabCapabilities?.[tabId] || {};
      try {
        const report = await api.scripting.executeScript({ target: { tabId }, func: inspectPageCapabilities });
        const live = report?.[0]?.result || {};
        capabilities = {
          ...capabilities,
          currentModel: live.currentModel || capabilities.currentModel || '',
          currentReasoning: live.currentReasoning || capabilities.currentReasoning || '',
          models: [...new Set([...(capabilities.models || []), ...(live.models || [])])],
          reasoningLevels: [...new Set([...(capabilities.reasoningLevels || []), ...(live.reasoningLevels || [])])]
        };
      } catch (_) {}
      tabs.push({
        id: tabId,
        origin: tab?.url && /^https?:/i.test(tab.url) ? new URL(tab.url).origin : '',
        title: tab?.title || '', profile: profile?.name || '',
        state: busyTabs.has(tabId) ? 'working' : (Number(cfg.tabCooldowns?.[tabId] || 0) > Date.now() ? 'rate_limited' : 'waiting'),
        current_model: capabilities.currentModel || '', current_reasoning: capabilities.currentReasoning || '',
        models: capabilities.models || [], reasoning_levels: capabilities.reasoningLevels || []
      });
    } catch (_) {}
  }
  const tab = tabs[0] || null;
  const profile = tab ? (cfg.taughtProfiles[tab.origin] || globalThis.ContextBridgeProfiles?.forURL(tab.origin)) : null;
  const effectiveState = busyTabs.size ? 'working' : state;
  try {
    await fetch(`${cfg.bridgeUrl}/v1/browser/heartbeat`, {
      method: 'POST',
      headers: { Authorization: `Bearer ${cfg.token}`, 'Content-Type': 'application/json' },
      body: JSON.stringify({
        state: effectiveState,
        origin: tab?.origin || '',
        tab_title: tab?.title || '',
        profile_label: profile?.label || cfg.profile || '',
        selectors_ready: tabs.some((item) => cfg.useVisualProfile ? Boolean(item.profile) : Boolean(cfg.profile)),
        extension_version: api.runtime.getManifest().version,
        browser: navigator.userAgent.includes('Firefox/') ? 'firefox' : 'chromium',
        active_tabs: tabs.length,
        busy_tabs: busyTabs.size,
        tabs
      })
    });
  } catch (_) {}
}

async function settings() {
  return api.storage.local.get({
    bridgeUrl: 'http://127.0.0.1:32145',
    token: '',
    profile: 'chatgpt',
    tabId: 0,
    tabIds: [],
    running: false,
    useVisualProfile: true,
    taughtProfiles: {},
    sessionTabs: {},
    tabCapabilities: {},
    tabCooldowns: {},
    pendingCompletions: {},
    lastError: ''
  });
}

function configuredTabIDs(cfg) {
  const values = Array.isArray(cfg?.tabIds) && cfg.tabIds.length ? cfg.tabIds : [cfg?.tabId];
  return [...new Set(values.map(Number).filter((value) => Number.isInteger(value) && value > 0))].slice(0, 16);
}

function inspectPageCapabilities() {
  const visible = (element) => Boolean(element && (element.offsetWidth || element.offsetHeight || element.getClientRects().length));
  const text = (element) => String(element?.innerText || element?.textContent || element?.getAttribute?.('aria-label') || '').replace(/\s+/g, ' ').trim().slice(0, 100);
  const unique = (values, limit) => [...new Set(values.filter(Boolean))].slice(0, limit);
  const controls = [...document.querySelectorAll('button, [role="button"]')].filter(visible);
  const options = [...document.querySelectorAll('[role="menuitem"], [role="option"], [aria-checked], [aria-selected]')].filter(visible);
  const modelPattern = /(?:gpt|gemini|astra|sol|terra|luna|flash|thinking|pro)(?:[\s._-]*\d)?/i;
  const reasoningPattern = /(?:reason|denk|effort|thinking|instant|sofort|low|niedrig|medium|mittel|high|hoch|pro|max)/i;
  const currentModel = text(controls.find((element) => {
    const label = `${text(element)} ${element.getAttribute('aria-label') || ''}`;
    return modelPattern.test(label) && !/modelle ergänzen|add models/i.test(label);
  }));
  const currentReasoning = text(controls.find((element) => reasoningPattern.test(`${text(element)} ${element.getAttribute('aria-label') || ''}`)));
  return {
    currentModel,
    currentReasoning,
    models: unique(options.map(text).filter((value) => modelPattern.test(value)), 50),
    reasoningLevels: unique(options.map(text).filter((value) => reasoningPattern.test(value)), 20)
  };
}

async function discoverPageCapabilities() {
  const visible = (element) => Boolean(element && (element.offsetWidth || element.offsetHeight || element.getClientRects().length));
  const text = (element) => String(element?.innerText || element?.textContent || element?.getAttribute?.('aria-label') || '').replace(/\s+/g, ' ').trim().slice(0, 100);
  const unique = (values, limit) => [...new Set(values.filter(Boolean))].slice(0, limit);
  const wait = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
  const modelPattern = /(?:gpt|gemini|astra|sol|terra|luna|flash|thinking|pro)(?:[\s._-]*\d)?/i;
  const reasoningPattern = /(?:reason|denk|effort|thinking|instant|sofort|low|niedrig|medium|mittel|high|hoch|pro|max)/i;
  const scan = async (kind) => {
    const pattern = kind === 'model' ? modelPattern : reasoningPattern;
    const triggers = [...document.querySelectorAll('button, [role="button"]')].filter(visible).filter((element) => {
      const label = `${text(element)} ${element.getAttribute('aria-label') || ''}`;
      if (!pattern.test(label) || /modelle ergänzen|add models/i.test(label)) return false;
      return kind === 'model' || !/(?:gpt|gemini|astra|sol|terra|luna|flash)[\s._-]*\d?/i.test(label);
    });
    if (!triggers.length) return { current: '', values: [] };
    const current = text(triggers[0]);
    triggers[0].click();
    await wait(350);
    const values = unique([...document.querySelectorAll('[role="menuitem"], [role="option"], [data-radix-collection-item], [aria-checked], [aria-selected]')].filter(visible).map(text).filter((value) => pattern.test(value)), kind === 'model' ? 50 : 20);
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', code: 'Escape', bubbles: true }));
    await wait(150);
    return { current, values };
  };
  const models = await scan('model');
  const reasoning = await scan('reasoning');
  return { currentModel: models.current, currentReasoning: reasoning.current, models: models.values, reasoningLevels: reasoning.values };
}

function tabSummary(tab) {
  return {
    id: tab.id,
    title: tab.title || 'Untitled',
    url: tab.url || '',
    origin: tab.url && /^https?:/i.test(tab.url) ? new URL(tab.url).origin : ''
  };
}

function delay(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

resume();
