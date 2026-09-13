if (!globalThis.ContextBridgeProfiles && typeof importScripts === 'function') {
  importScripts('profiles.js');
}

const api = globalThis.browser || globalThis.chrome;

let stopRequested = false;
let heartbeatTimer = 0;
let heartbeatInFlight = null;
const pollers = new Map();
const busyTabs = new Set();
const freshTabChecks = new Map();
let freshTabWrite = Promise.resolve();
const acceptedModelLabel = /^(?:gpt[\s._-]*\d+(?:\.\d+)?(?:[\s._-]*(?:astra|sol|terra|luna|pro|mini|nano|codex|thinking|instant))?|gemini(?:[\s._-]*\d+(?:\.\d+)?(?:[\s._-]*(?:pro|flash|lite|thinking|preview|experimental))*)?|astra|sol|terra|luna|\d+(?:\.\d+)?\s+(?:pro|flash|astra|sol|terra|luna))$/i;

api.runtime.onInstalled.addListener(() => resume());
api.runtime.onStartup.addListener(() => resume());
api.tabs.onUpdated?.addListener((tabId, changeInfo, tab) => {
  if (changeInfo.status !== 'complete') return;
  scheduleFreshTabCheck(tabId, tab?.url || changeInfo.url || '');
  void settings().then((cfg) => {
    if (cfg.running && configuredTabIDs(cfg).includes(tabId)) return sendHeartbeat('waiting');
  }).catch(() => {});
});
api.tabs.onRemoved?.addListener((tabId) => {
  if (freshTabChecks.has(tabId)) clearTimeout(freshTabChecks.get(tabId));
  freshTabChecks.delete(tabId);
  void detachClosedTab(tabId);
});

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
    case 'refresh-tabs':
      if ((await settings()).running) {
        poll();
        await sendHeartbeat(busyTabs.size ? 'working' : 'waiting');
      }
      return { ok: true };
    case 'check-tab-freshness':
      return { ok: true, fresh: await checkFreshTab(Number(message.tabId)) };
    case 'discover-fresh-tabs':
      await discoverFreshTabs();
      return { ok: true };
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
  void discoverFreshTabs();
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
  void discoverFreshTabs();
}

function isFreshChatURL(value) {
  try {
    const url = new URL(value);
    if (url.search || url.hash) return false;
    if (['chatgpt.com', 'chat.openai.com'].includes(url.hostname)) return url.pathname === '/';
    if (url.hostname === 'gemini.google.com') return url.pathname === '/app' || url.pathname === '/app/';
  } catch (_) {}
  return false;
}

function inspectFreshChat() {
  const input = document.querySelector('#prompt-textarea, rich-textarea [contenteditable="true"][role="textbox"], div.ql-editor[contenteditable="true"][role="textbox"]');
  if (!input) return false;
  if (String(input.value || input.innerText || input.textContent || '').trim()) return false;
  if (document.querySelector('section[data-turn], [data-message-author-role], user-query, model-response')) return false;
  if (input.closest?.('form')?.querySelector?.('[data-testid*="attachment" i], [data-test-id*="attachment" i], .attachment-chip, .file-chip')) return false;
  return true;
}

async function checkFreshTab(tabId) {
  if (!tabId) return false;
  const firstTab = await api.tabs.get(tabId);
  if (!isFreshChatURL(firstTab?.url)) return false;
  const first = await api.scripting.executeScript({ target: { tabId }, func: inspectFreshChat });
  if (!first?.[0]?.result) return false;
  await delay(1000);
  const secondTab = await api.tabs.get(tabId);
  if (secondTab?.url !== firstTab.url || !isFreshChatURL(secondTab.url)) return false;
  const second = await api.scripting.executeScript({ target: { tabId }, func: inspectFreshChat });
  return second?.[0]?.result === true;
}

function scheduleFreshTabCheck(tabId, url) {
  if (!isFreshChatURL(url)) return;
  if (freshTabChecks.has(tabId)) clearTimeout(freshTabChecks.get(tabId));
  freshTabChecks.set(tabId, setTimeout(() => {
    freshTabChecks.delete(tabId);
    void maybeAutoAttachFreshTab(tabId);
  }, 1500));
}

async function maybeAutoAttachFreshTab(tabId) {
  try {
    const cfg = await settings();
    if (!cfg.running || !cfg.autoAttachFreshTabs || cfg.autoAttachBlockedTabIds.includes(tabId) || configuredTabIDs(cfg).includes(tabId) || configuredTabIDs(cfg).length >= 16) return;
    if (!await api.permissions.contains({ permissions: ['tabs'] })) return;
    const tab = await api.tabs.get(tabId);
    if (!isFreshChatURL(tab?.url) || !await api.permissions.contains({ origins: [new URL(tab.url).origin + '/*'] })) return;
    if (!await checkFreshTab(tabId)) return;
    freshTabWrite = freshTabWrite.catch(() => {}).then(async () => {
      const latest = await settings();
      if (!latest.running || !latest.autoAttachFreshTabs || latest.autoAttachBlockedTabIds.includes(tabId) || configuredTabIDs(latest).includes(tabId) || configuredTabIDs(latest).length >= 16) return;
      const current = await api.tabs.get(tabId);
      if (current.url !== tab.url) return;
      const tabIds = [...configuredTabIDs(latest), tabId];
      await api.storage.local.set({ tabId: tabIds[0], tabIds });
      poll();
      await sendHeartbeat('waiting');
    });
    await freshTabWrite;
  } catch (_) { /* A missing permission or a changing page must fail closed. */ }
}

async function discoverFreshTabs() {
  try {
    const cfg = await settings();
    if (!cfg.running || !cfg.autoAttachFreshTabs || !await api.permissions.contains({ permissions: ['tabs'] })) return;
    for (const tab of await api.tabs.query({})) {
      if (isFreshChatURL(tab.url)) await maybeAutoAttachFreshTab(tab.id);
    }
  } catch (_) {}
}

async function detachClosedTab(tabId) {
  const cfg = await settings();
  const autoAttachBlockedTabIds = cfg.autoAttachBlockedTabIds.filter((id) => id !== tabId);
  const tabCapabilities = { ...cfg.tabCapabilities };
  const tabCapabilityScans = { ...cfg.tabCapabilityScans };
  const tabFailures = { ...cfg.tabFailures };
  delete tabCapabilities[tabId];
  delete tabCapabilityScans[tabId];
  delete tabFailures[tabId];
  if (!configuredTabIDs(cfg).includes(tabId)) {
    await api.storage.local.set({ autoAttachBlockedTabIds, tabCapabilities, tabCapabilityScans, tabFailures });
    return;
  }
  const tabIds = configuredTabIDs(cfg).filter((id) => id !== tabId);
  await api.storage.local.set({ tabId: tabIds[0] || 0, tabIds, tabCapabilities, tabCapabilityScans, tabFailures, autoAttachBlockedTabIds });
  if (cfg.running) await sendHeartbeat('waiting');
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
      const busy = busyTabs.has(tabId);
      const coolingDown = Number(cfg.tabCooldowns?.[tabId] || 0) > Date.now();
      tabs.push({
        ...tabSummary(tab),
        busy,
        state: busy ? 'working' : (coolingDown ? 'rate_limited' : 'waiting'),
        profile: profileForTab(cfg, tab)?.name || '',
        currentModel: cfg.tabCapabilities?.[tabId]?.currentModel || '',
        lastFailure: cfg.tabFailures?.[tabId] || null
      });
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
  const tabCapabilityScans = { ...(cfg.tabCapabilityScans || {}), [tabId]: Date.now() };
  await api.storage.local.set({ tabCapabilities, tabCapabilityScans });
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
        const latest = await settings();
        if (!latest.running || !configuredTabIDs(latest).includes(tabId)) {
          await completeWork(latest, work.job.id, { mode: outputMode(work.job.output || {}), error: 'browser_tab_detached', model: 'browser' });
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
  let failureReason = 'other';
  await api.storage.local.set({ lastError: '' });
  try {
    tabId = await resolveWorkTab(cfg, work, claimedTabId);
    await waitForTabSlot(tabId, work.deadline);
    tabSlotHeld = true;
    const previousFailures = (await api.storage.local.get({ tabFailures: {} })).tabFailures;
    if (previousFailures[tabId]) {
      const tabFailures = { ...previousFailures };
      delete tabFailures[tabId];
      await api.storage.local.set({ tabFailures });
    }
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
    latestProgressState = `${Boolean(initial.busy)}|${Number(initial.percent) || 0}|${initial.detail || ''}|false`;
    const sample = async () => {
      if (progressBusy) return;
      progressBusy = true;
      try {
        const snapshot = await captureTabProgress(tabId, effectiveProfile.selectors);
        const text = String(snapshot.text || '').trim();
		const modeFamily = (value) => {
		  const words = String(value || '').toLowerCase().normalize('NFKD').replace(/[^a-z0-9äöüß]+/g, ' ').split(/\s+/);
		  return words.includes('thinking') ? 'thinking' : words.includes('pro') ? 'pro'
		    : words.includes('lite') ? 'flash-lite' : words.includes('flash') ? 'flash' : '';
		};
		const modeMismatch = effectiveProfile.name === 'gemini' && Boolean(modeFamily(work.job.model))
		  && Boolean(text && text !== baselineText)
		  && (Boolean(snapshot.model_fallback)
		    || (Boolean(snapshot.current_model) && modeFamily(snapshot.current_model) !== modeFamily(work.job.model)));
		const textChanged = Boolean(text && text !== baselineText && text !== latestProgressText && !modeMismatch
		  && Number(work.job.output?.min_images || 0) === 0);
		const progressState = `${Boolean(snapshot.busy)}|${Number(snapshot.percent) || 0}|${snapshot.detail || ''}|${modeMismatch}`;
        if (textChanged || progressState !== latestProgressState) {
          if (textChanged) latestProgressText = text;
          latestProgressState = progressState;
          progressSequence += 1;
          await reportProgress(cfg, work.job.id, {
            sequence: progressSequence,
            text: textChanged ? text.slice(0, 1024 * 1024) : '',
            phase: modeMismatch ? 'model_mismatch' : (snapshot.busy ? 'generating' : 'stabilizing'),
            detail: modeMismatch ? 'Gemini changed mode; holding answer for verification' : (snapshot.detail || ''),
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
      failureReason = classifyFailureReason(answer?.error);
      if (failureCode === 'browser_rate_limited') {
		await coolDownTab(tabId, 5 * 60 * 1000);
        progressSequence += 1;
        await reportProgress(cfg, work.job.id, { sequence: progressSequence, text: '', phase: 'rate_limited', detail: answer?.error || 'Provider rate limit', busy: false });
      }
      throw new Error(answer?.error || 'No browser response was captured');
    }
    if (answer.artifacts?.length) {
      progressSequence += 1;
      await reportProgress(cfg, work.job.id, {
        sequence: progressSequence,
        text: '',
        phase: 'transferring',
        detail: `Collecting ${answer.artifacts.length} browser file(s)`,
        busy: true
      });
      answer.artifacts = await hydrateArtifactReferences(answer.artifacts, tab.url, work.job.output || {});
    }
    if (!String(answer.text || '').trim()) {
      const files = (answer.artifacts || []).filter((artifact) => Boolean(artifact.data_base64)).length;
      const references = (answer.artifacts || []).length - files;
      answer.text = `Captured ${files} file(s) and ${references} reference(s).`;
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
    // These are the selections confirmed by the tab automation, not merely
    // the model/reasoning requested by the remote job.
    if (answer.selected_model) decision.selected_model = String(answer.selected_model).slice(0, 100);
    if (answer.selected_reasoning) decision.selected_reasoning = String(answer.selected_reasoning).slice(0, 100);
  } catch (error) {
    const mode = outputMode(work.job.output || {});
    decision = mode === 'decision'
      ? { verdict: 'review', flags: [failureCode], confidence: 0.4, model: effectiveProfile?.label || 'browser' }
      : { mode, error: failureCode, model: effectiveProfile?.label || 'browser' };
    const tabFailures = { ...(await api.storage.local.get({ tabFailures: {} })).tabFailures };
    if (tabId) tabFailures[tabId] = { code: failureCode, reason: failureReason === 'other' ? classifyFailureReason(error.message) : failureReason, at: new Date().toISOString() };
    await api.storage.local.set({ lastError: error.message || String(error), tabFailures });
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

function classifyFailureReason(message) {
  const text = String(message || '');
  if (/requested model.+not retained/i.test(text)) return 'model_not_retained';
  if (/prompt editor did not retain|prompt editor changed|gemini editor did not accept/i.test(text)) return 'prompt_not_retained';
  if (/send button stayed disabled/i.test(text)) return 'send_disabled';
  if (/send button is not visible/i.test(text)) return 'send_missing';
  if (/prompt editor contains another draft/i.test(text)) return 'composer_draft';
  if (/incompatible selected tool/i.test(text)) return 'incompatible_tool';
  return 'other';
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

async function hydrateArtifactReferences(artifacts, pageURL, spec) {
  if (!spec?.artifacts || !Array.isArray(artifacts)) return [];
  const limit = Math.max(1024, Math.min(Number(spec.max_artifact_bytes) || 12 << 20, 12 << 20));
  const pageOrigin = new URL(pageURL).origin;
  const result = [];
  let total = 0;
  for (const artifact of artifacts.slice(0, 12)) {
    if (artifact.data_base64) {
      total += Number(artifact.size) || Math.floor(artifact.data_base64.length * 3 / 4);
      result.push(artifact);
      continue;
    }
    if (!artifact.url) continue;
    try {
      const resource = new URL(artifact.url);
      if (resource.protocol !== 'https:' || resource.origin !== pageOrigin) throw new Error('Provider-hosted reference');
      const response = await fetch(resource.href, { credentials: 'include', redirect: 'error' });
      if (!response.ok || !response.body) throw new Error('Resource could not be read');
      const contentType = String(response.headers.get('content-type') || '').split(';')[0].toLowerCase();
      if (artifact.media_type?.startsWith('image/') && !contentType.startsWith('image/')) throw new Error('Image response has the wrong media type');
      const chunks = [];
      let size = 0;
      const reader = response.body.getReader();
      while (true) {
        const { done, value } = await reader.read();
        if (done) break;
        if (total + size + value.byteLength > limit) {
          await reader.cancel();
          throw new Error('Artifact exceeds transfer limit');
        }
        chunks.push(value);
        size += value.byteLength;
      }
      if (!size) throw new Error('Empty artifact');
      const bytes = new Uint8Array(size);
      let offset = 0;
      for (const chunk of chunks) {
        bytes.set(chunk, offset);
        offset += chunk.byteLength;
      }
      const digest = await crypto.subtle.digest('SHA-256', bytes);
      let binary = '';
      for (let start = 0; start < bytes.length; start += 0x8000) {
        binary += String.fromCharCode(...bytes.subarray(start, Math.min(start + 0x8000, bytes.length)));
      }
      result.push({
        ...artifact,
        url: '',
        media_type: contentType || artifact.media_type,
        size,
        sha256: [...new Uint8Array(digest)].map((value) => value.toString(16).padStart(2, '0')).join(''),
        data_base64: btoa(binary)
      });
      total += size;
    } catch (_) {
      result.push(artifact);
    }
  }
  return result;
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
  const responseText = (element) => {
    if (!element) return '';
    if (element.querySelector('[data-testid="image-gen-loading-state"], [data-testid="image-gen-loading-progress"]')) return '';
    const markdownParts = [...element.querySelectorAll('[data-message-author-role="assistant"] .markdown, message-content .markdown')];
    if (markdownParts.length) return markdownParts.map(visibleText).filter(Boolean).join('\n');
    if (element.querySelector('img') && element.matches?.('section[data-turn="assistant"], model-response')) return '';
    const assistantParts = [...element.querySelectorAll('[data-message-author-role="assistant"]')];
    if (assistantParts.length) return assistantParts.map(visibleText).filter(Boolean).join('\n');
    return visibleText(element);
  };
  const responseIdentity = (element) => {
    if (!element) return '';
    return ['data-message-id', 'data-testid', 'data-turn', 'id']
      .map((name) => element.getAttribute?.(name) || '')
      .filter(Boolean)
      .join('|');
  };
  const pageState = () => {
    let percent = 0;
    let detail = '';
    for (const indicator of document.querySelectorAll('[data-testid="image-gen-loading-progress"], [role="progressbar"][aria-valuenow]')) {
      if (!isVisible(indicator)) continue;
      percent = Math.max(percent, Math.max(0, Math.min(100, Number(indicator.getAttribute('aria-valuenow')) || 0)));
      detail = 'Image generation';
    }
    const busyReasons = [];
    for (const [reason, selector] of [
      ['aria_busy', '[aria-busy="true"]'],
      ['streaming_attribute', '[data-is-streaming="true"]'],
      ['streaming_class', '.result-streaming'],
      ['image_loading', '[data-testid="image-gen-loading-state"]'],
      ['image_loading', '[data-testid="image-gen-loading-state-frame"]'],
      ['stop_button', 'button[data-testid*="stop" i]'],
      ['stop_button', 'button[aria-label*="stop" i]'],
      ['stop_button', 'button[aria-label*="beenden" i]'],
      ['stop_button', 'button[aria-label*="abbrechen" i]']
    ]) {
      try {
        if (!busyReasons.includes(reason) && [...document.querySelectorAll(selector)].some(isVisible)) busyReasons.push(reason);
      } catch (_) {}
    }
    return {
      busy: busyReasons.length > 0,
      busyReasons,
      percent,
      detail: busyReasons.length ? (detail || 'Generating') : '',
      composerReady: Boolean(first(selectors.submit)),
      inputReady: Boolean(first(selectors.input))
    };
  };
  const pageBusy = () => pageState().busy;
  const providerError = (responseElement, changedResponse, beforeUserTurns) => {
    // ChatGPT can attach a retryable thread error to the newly sent *user*
    // turn without creating an assistant turn. Never classify an unrelated
    // older assistant answer as the error for this job.
    if (profile.name === 'chatgpt') {
      const userTurns = [...document.querySelectorAll('[data-turn="user"]')];
      const latestUser = userTurns.at(-1);
      if (userTurns.length > beforeUserTurns && latestUser) {
        const retry = latestUser.querySelector?.('button[data-testid="regenerate-thread-error-button"]');
        const banner = latestUser.querySelector?.('[class*="text-orange-600"], [data-testid="thread-error"]');
        if (isVisible(retry) && isVisible(banner)) {
          const message = visibleText(banner).slice(0, 300);
          if (message) return { code: 'browser_provider_error', message, retryable: true };
        }
      }
    }
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
	const semanticText = text || (retryVisible && changedResponse ? responseText : '');
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
      if (profile.name === 'gemini' && element.matches?.('.ql-editor[contenteditable="true"]')) {
        if (document.activeElement !== element) throw new Error('Gemini editor did not accept focus');
        document.execCommand('selectAll', false, null);
        if (!document.execCommand('insertText', false, value)) throw new Error('Gemini editor did not accept input');
      } else {
        const selection = window.getSelection();
        const range = document.createRange();
        range.selectNodeContents(element);
        selection.removeAllRanges();
        selection.addRange(range);
        document.execCommand('insertText', false, value);
        if (!visibleText(element)) element.textContent = value;
      }
    }
    element.dispatchEvent(new InputEvent('input', { bubbles: true, inputType: 'insertText', data: value }));
    element.dispatchEvent(new Event('change', { bubbles: true }));
  };
  const clearIncompatibleGeminiTools = async (job) => {
    if (profile.name !== 'gemini') return false;
    const wantsImage = Number(job.output?.min_images || 0) > 0 || Boolean(job.metadata?.contextbridge_image_tool);
    let cleared = false;
    for (let attempt = 0; attempt < 4; attempt += 1) {
      const composer = first(selectors.input)?.closest?.('[data-node-type="input-area"]');
      if (!composer) return cleared;
      const selected = [...composer.querySelectorAll('button[aria-label]')].find((button) => {
        const label = String(button.getAttribute('aria-label') || '');
        if (!/(?:auswahl von .+ aufheben|remove .+ selection|deselect .+|clear .+ tool)/i.test(label)) return false;
        return !wantsImage || !/(?:bild|image)/i.test(label);
      });
      if (!selected) return cleared;
      selected.click();
      cleared = true;
      await wait(250);
    }
    throw new Error('Gemini kept an incompatible selected tool after clearing it');
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
	const geminiCurrentMode = () => String(document.querySelector('bard-mode-switcher button[aria-haspopup]')
		?.getAttribute('aria-label')?.match(/(?:derzeit ausgewählt|currently selected|selected)\s*:\s*(.+)$/i)?.[1] || '').trim();
	const geminiModeFamily = (value) => {
		const words = normalizedWords(value);
		if (words.includes('thinking')) return 'thinking';
		if (words.includes('pro')) return 'pro';
		if (words.includes('lite')) return 'flash-lite';
		if (words.includes('flash')) return 'flash';
		return '';
	};
	const confirmGeminiMode = (requested) => {
		const observed = geminiCurrentMode();
		const expectedFamily = geminiModeFamily(requested);
		if (!observed || !expectedFamily || geminiModeFamily(observed) !== expectedFamily) {
			throw new Error(`Requested model "${requested}" was not retained by Gemini (visible: ${observed || 'unknown'})`);
		}
		return observed;
	};
	const choosePreference = async (kind, requested) => {
		if (!requested || ['auto', 'default'].includes(String(requested).toLowerCase())) return '';
		const triggerSelectors = kind === 'model'
			? ['bard-mode-switcher button[aria-haspopup]', 'button[data-testid*="model" i]', 'button[aria-label*="model" i]', 'button[aria-haspopup="menu"]']
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
		const normalizedValue = (value) => normalizedWords(value).join(' ');
		const geminiModeLabel = (element) => {
			const primary = visibleText(element.querySelector?.('.picker-primary-text, .mode-name, .model-name'));
			const secondary = visibleText(element.querySelector?.('.picker-secondary-text'));
			if (primary) return `${primary} ${secondary}`.trim();
			return String(element.getAttribute('aria-label') || '').trim() || String(element.innerText || '').split('\n').map((part) => part.trim()).filter(Boolean)[0] || '';
		};
		const current = kind === 'model' && profile.name === 'gemini'
			? triggers.find((element) => Boolean(element.closest?.('bard-mode-switcher')) && normalizedValue(element.getAttribute('aria-label')?.match(/(?:derzeit ausgewählt|currently selected|selected)\s*:\s*(.+)$/i)?.[1] || '') === normalizedValue(requested))
			: triggers.find((element) => {
				if (kind !== 'model') return preferenceMatches(element);
				const semantic = `${element.getAttribute('data-testid') || ''} ${element.getAttribute('aria-label') || ''}`;
				return (/model[-_ ]?(?:switcher|selector|picker|menu)|modellauswahl|modellmenü/i.test(semantic)
					|| /^(?:gpt[\s._-]*\d|astra\b|sol\b|terra\b|luna\b)/i.test(visibleText(element))) && preferenceMatches(element);
			});
		if (current) return kind === 'model' && profile.name === 'gemini' ? confirmGeminiMode(requested) : (visibleText(current) || requested);
		const trigger = triggers.find((element) => {
			const label = `${visibleText(element)} ${element.getAttribute('aria-label') || ''}`.toLowerCase();
			return kind === 'model' ? (profile.name === 'gemini' && Boolean(element.closest?.('bard-mode-switcher'))) || /gpt|gemini|model|modell|astra|sol|terra|luna/.test(label) : /reason|denk|effort|thinking|sofort|instant|hoch|high|pro|max/.test(label);
		});
		if (!trigger) throw new Error(`The ${kind} selector is not visible in this provider UI`);
		trigger.click();
		await wait(350);
		const menuID = trigger.getAttribute('aria-controls');
		const menu = kind === 'model' && profile.name === 'gemini'
			? (menuID && document.getElementById(menuID)) || [...document.querySelectorAll('[role="menu"], .cdk-overlay-pane')].filter(isVisible).at(-1)
			: null;
		const options = [...(menu || document).querySelectorAll(menu
			? 'button, [role="menuitem"], [role="option"], [role="menuitemradio"], mat-option'
			: '[role="menuitem"], [role="option"], [data-radix-collection-item], [aria-checked], [aria-selected]')].filter(isVisible);
		const match = kind === 'model' && profile.name === 'gemini'
			? options.find((element) => normalizedValue(geminiModeLabel(element)) === normalizedValue(requested))
			: options.find(preferenceMatches);
		if (!match || match.disabled || match.getAttribute('aria-disabled') === 'true') {
			document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', code: 'Escape', bubbles: true }));
			throw new Error(`Requested ${kind} "${requested}" is not available in this chat`);
		}
		match.click();
		await wait(350);
		if (profile.name === 'gemini' && kind === 'model') {
			await wait(650);
			return confirmGeminiMode(requested);
		}
		return visibleText(match) || requested;
	};
	const chooseImageTool = async (input) => {
		if (profile.name !== 'chatgpt') return;
		const label = (element) => `${visibleText(element)} ${element.getAttribute('aria-label') || ''} ${element.getAttribute('data-testid') || ''}`.trim();
		const imageChoice = (value) => /(?:ein\s+)?bild(?:er)?\s+(?:erstellen|generieren)|(?:create|generate)\s+(?:an?\s+)?image|create[-_]?image/i.test(value);
		const composer = input.closest('form');
		const direct = composer && [...composer.querySelectorAll('button, [role="button"]')].find((element) => imageChoice(label(element)));
		if (direct) {
			if (direct.disabled || direct.getAttribute('aria-disabled') === 'true') throw new Error('Image creation is rate limited in this chat');
			if (direct.getAttribute('aria-pressed') === 'true' || direct.getAttribute('data-state') === 'active') return;
			direct.click();
			await wait(450);
			return;
		}
		const trigger = first(['button[data-testid="composer-plus-btn"]', 'button[aria-label*="Dateien und mehr" i]', 'button[aria-label*="Add photos & files" i]']);
		if (!trigger || trigger.getAttribute('aria-haspopup') !== 'menu') throw new Error('Image creation tool menu is not available in this chat');
		const choiceSelector = '[role="menuitem"], [role="option"], [data-radix-collection-item], button, a, li';
		const before = new Set([...document.querySelectorAll(choiceSelector)].filter(isVisible));
		trigger.click();
		await wait(450);
		const choices = [...document.querySelectorAll(choiceSelector)].filter(isVisible).filter((element) => !before.has(element));
		const choice = choices.find((element) => imageChoice(label(element)))
			|| choices.find((element) => /^(?:bild|bilder|image|images)$/i.test(label(element)));
		if (!choice) {
			document.activeElement?.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', code: 'Escape', bubbles: true }));
			const alerts = [...document.querySelectorAll('[role="alert"]')].filter(isVisible).map(visibleText).join(' ');
			if (/rate.?limit|limit erreicht|usage.?limit|quota/i.test(alerts)) throw new Error('Image creation is rate limited in this chat');
			const available = choices.map(label).filter(Boolean).slice(0, 8).map((value) => value.slice(0, 60)).join(' / ');
			throw new Error(`Image creation tool is not available in this chat${available ? `; visible tools: ${available}` : ''}`);
		}
		choice.click();
		await wait(450);
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
			if (image.getAttribute('aria-hidden') === 'true') continue;
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
      let input = first(selectors.input);
      if (!input) throw new Error('Prompt input was not found');
      const before = all(selectors.response);
      const beforeUserTurns = document.querySelectorAll('[data-turn="user"]').length;
      const previousElement = before.length ? before[before.length - 1] : null;
      const previousText = String(job.metadata?.contextbridge_baseline_text || (before.length ? responseText(before[before.length - 1]) : ''));
      const previousIdentity = responseIdentity(previousElement);
      const resumeOnly = Boolean(job.metadata?.contextbridge_resume_only);
	  const selectedModel = !resumeOnly && job.model ? await choosePreference('model', job.model) : '';
	  const selectedReasoning = !resumeOnly && job.reasoning ? await choosePreference('reasoning', job.reasoning) : '';
	  const clearedGeminiTool = !resumeOnly && await clearIncompatibleGeminiTools(job);
	  if (!resumeOnly && (job.model || job.reasoning || clearedGeminiTool)) {
		// Switching a provider mode may replace the entire composer. Never
		// type into the detached element captured before the menu was opened.
		input = null;
		for (let attempt = 0; attempt < 12 && !input; attempt += 1) {
			input = first(selectors.input);
			if (!input) await wait(150);
		}
		if (!input) throw new Error('Prompt input disappeared after selecting the model');
	  }
	  if (!resumeOnly && job.metadata?.contextbridge_image_tool) {
		await chooseImageTool(input);
		input = first(selectors.input);
		if (!input) throw new Error('Prompt input disappeared after selecting the image tool');
	  }
      if (!resumeOnly && job.image_base64) {
        const fileInput = (selectors.file_input || []).flatMap((selector) => {
          try { return [...document.querySelectorAll(selector)]; } catch (_) { return []; }
        }).find((element) => element.type === 'file' && (!element.accept || /image|\*/i.test(element.accept)));
        if (!fileInput) throw new Error('This job has an image, but no image input was taught');
        addImage(fileInput, job.image_base64, job.image_media_type);
        await wait(1000);
      }
      if (!resumeOnly) {
        const editorText = (element) => String(element?.value || element?.innerText || element?.textContent || '');
        const normalized = (value) => String(value || '').replace(/[\u200b-\u200d\ufeff]/g, '').replace(/\s+/g, ' ').trim();
        const expected = normalized(job.prompt);
        const draft = normalized(editorText(input));
        if (draft && draft !== expected) throw new Error('Prompt editor contains another draft');
        if (!draft) setInput(input, job.prompt);
        let retained = false;
        for (let attempt = 0; attempt < 6; attempt += 1) {
          const liveInput = first(selectors.input);
          const liveText = normalized(editorText(liveInput));
          if (liveInput && liveText === expected) {
            input = liveInput;
            retained = true;
            break;
          }
          if (liveText) throw new Error('Prompt editor changed the submitted text');
          await wait(150);
        }
        if (!retained) throw new Error('Prompt editor did not retain the submitted text');
        let submit = first(selectors.submit);
        for (let attempt = 0; (!submit || submit.disabled || submit.getAttribute('aria-disabled') === 'true') && attempt < 20; attempt += 1) {
          await wait(150);
          submit = first(selectors.submit);
        }
        if (submit && !submit.disabled && submit.getAttribute('aria-disabled') !== 'true') {
          submit.click();
        } else if (submit) {
			throw new Error('Send button stayed disabled after filling the prompt');
        } else {
          if (profile.name === 'gemini') throw new Error('Send button is not visible after filling the prompt');
          input.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', code: 'Enter', bubbles: true }));
          input.dispatchEvent(new KeyboardEvent('keyup', { key: 'Enter', code: 'Enter', bubbles: true }));
        }
      }

		const suppliedDeadline = Date.parse(jobDeadline || '');
		const defaultWait = job.output?.artifacts ? 300000 : 180000;
		const reserve = Number.isFinite(suppliedDeadline)
			? Math.min(15000, Math.max(1000, (suppliedDeadline - Date.now()) / 10)) : 0;
		const deadline = Number.isFinite(suppliedDeadline)
			? Math.min(Date.now() + defaultWait, suppliedDeadline - reserve)
			: Date.now() + defaultWait;
      let stableText = '';
      let stableSince = 0;
      let sawBusy = false;
      let lastBusyAt = Date.now();
      let lastPercent = 0;
      let progressChangedAt = Date.now();
      let lastReadyImages = 0;
      let readyImagesSince = 0;
      while (Date.now() < deadline) {
        await wait(650);
        const responses = all(selectors.response);
        const latestElement = responses.length ? responses[responses.length - 1] : null;
        const latest = responseText(latestElement);
		const readyImages = latestElement
			? [...latestElement.querySelectorAll('img')].filter((image) => image.complete && image.naturalWidth >= 128 && image.naturalHeight >= 128).length : 0;
		if (readyImages !== lastReadyImages) {
			lastReadyImages = readyImages;
			readyImagesSince = Date.now();
		}
		const latestIdentity = responseIdentity(latestElement);
		const changedResponse = responses.length > before.length || latest !== previousText
			|| Boolean(previousIdentity && latestIdentity && latestIdentity !== previousIdentity);
        const fallbackNotice = latestElement?.closest?.('.conversation-container')?.querySelector?.('peak-hour-fallback-disclaimer');
        if (profile.name === 'gemini' && geminiModeFamily(job.model) === 'pro' && changedResponse && isVisible(fallbackNotice)) {
          resolve({ ok: false, error: 'Gemini used another model during peak demand; requested Pro output was not accepted', code: 'browser_model_unavailable', retryable: true });
          return;
        }
        const state = pageState();
        // Gemini can leave aria-busy on the finished response after its Stop
        // control has vanished. Treat that one stale attribute as finished
        // only after a *new* answer has stayed unchanged for 12 seconds.
        // Any streaming, Stop, or image indicator still blocks completion.
        const requiredImages = Number(job.output?.min_images || 0);
        const imagesReady = requiredImages > 0 && readyImages >= requiredImages
          && readyImagesSince > 0 && Date.now() - readyImagesSince >= 3000;
        const stableAnswer = Boolean(latest) && latest === stableText && (requiredImages === 0 || imagesReady);
        const stableImages = imagesReady
          && stableText.startsWith('artifact:')
          && Boolean(job.output?.artifacts);
        const staleGeminiBusy = profile.name === 'gemini'
          && state.busyReasons.length === 1 && state.busyReasons[0] === 'aria_busy'
          && changedResponse && (stableAnswer || stableImages)
          && stableSince > 0 && Date.now() - stableSince >= 12000
          && state.inputReady;
        const busy = state.busy && !staleGeminiBusy;
        sawBusy = sawBusy || busy;
		if (busy) lastBusyAt = Date.now();
		if (sawBusy && !busy && !changedResponse && !resumeOnly && Date.now() - lastBusyAt > 10000) {
			resolve({ ok: false, error: 'Generation ended without a new assistant turn; reloading once to recover the conversation', code: 'missing_response_after_generation', recoverable: true });
			return;
		}
		if (state.percent !== lastPercent) {
			lastPercent = state.percent;
			progressChangedAt = Date.now();
		}
		if (busy && lastPercent >= 95 && Date.now() - progressChangedAt > 45000) {
			resolve({ ok: false, error: `Image generation stalled at ${lastPercent}%`, code: 'stalled_generation', percent: lastPercent, recoverable: !resumeOnly && job.metadata?.contextbridge_auto_reload !== false });
			return;
		}
		const providerFailure = providerError(latestElement, changedResponse, beforeUserTurns);
		if (providerFailure && !busy) {
			resolve({ ok: false, error: providerFailure.message, code: providerFailure.code, retryable: providerFailure.retryable });
			return;
		}
		if (job.output?.min_images > 0 && changedResponse && !busy && latest) {
			if (/rate.?limit|usage.?limit|too many requests|quota|limit erreicht|nutzungslimit|bild(?:er)?limit|sp[aä]ter erneut/i.test(latest)) {
				resolve({ ok: false, error: latest.slice(0, 300), code: 'browser_rate_limited', retryable: true });
				return;
			}
			if (/bildgenerator\s+nicht\s+verf[uü]gbar|(?:image|bild)(?:\s+generation|generierung)?\s+(?:is\s+)?(?:not\s+available|unavailable|nicht\s+verf[uü]gbar)|(?:cannot|can't|kann\s+(?:leider\s+)?keine)\s+(?:generate\s+)?(?:images|bilder)/i.test(latest)) {
				resolve({ ok: false, error: latest.slice(0, 300), code: 'browser_image_tool_unavailable' });
				return;
			}
		}
		const artifactCount = job.output?.artifacts && latestElement
			? [...latestElement.querySelectorAll('img')].filter((image) => (!image.naturalWidth || image.naturalWidth >= 128) && (!image.naturalHeight || image.naturalHeight >= 128)).length
				+ latestElement.querySelectorAll('a[download], pre code, [data-file-citation-primary-file-id]').length
			: 0;
		const imageCount = latestElement ? [...latestElement.querySelectorAll('img')].filter((image) => image.getAttribute('aria-hidden') !== 'true' && image.naturalWidth >= 128 && image.naturalHeight >= 128).length : 0;
		const stableValue = latest || (artifactCount ? `artifact:${artifactCount}` : '');
		if (!stableValue || !changedResponse) continue;
		if (stableValue !== stableText) {
			stableText = stableValue;
          stableSince = Date.now();
          continue;
        }
        const mode = String(job.output?.mode || 'decision').toLowerCase();
        const structured = mode === 'text' || ((latest.includes('{') && latest.includes('}')) || (latest.includes('[') && latest.includes(']')));
		// Gemini hides Send again as soon as its composer is empty. Its ready
		// textbox plus a stable new assistant turn is a valid finished state.
		const stableFor = sawBusy ? 1300 : (profile.name === 'gemini' ? 6000 : 2600);
		const composerFinished = !(selectors.submit || []).length || state.composerReady
			|| (state.inputReady && (sawBusy || profile.name === 'gemini'));
        if (Date.now() - stableSince >= stableFor && structured && !busy && composerFinished) {
		  const filesMissing = job.output?.min_artifacts > artifactCount;
		  const imagesMissing = job.output?.min_images > imageCount;
		  if ((filesMissing || imagesMissing) && Date.now() - stableSince < 15000) continue;
		  const artifacts = await collectArtifacts(latestElement, job.output || {});
		  const confirmedModel = profile.name === 'gemini' && job.model && !['auto', 'default'].includes(String(job.model).toLowerCase()) && !resumeOnly
			? confirmGeminiMode(job.model) : selectedModel;
		  resolve({ ok: true, text: latest, artifacts, selected_model: confirmedModel, selected_reasoning: selectedReasoning });
          return;
        }
      }
      resolve({ ok: false, error: 'Timed out while waiting for a stable response', code: 'browser_timeout', recoverable: false, percent: lastPercent });
      return;
    } catch (error) {
      const message = error.message || String(error);
      const code = /requested model|model selector/i.test(message)
		? 'browser_model_unavailable'
		: (/requested reasoning|reasoning selector/i.test(message) ? 'browser_reasoning_unavailable'
			: (/send button stayed disabled|send button is not visible|prompt editor did not retain|prompt editor changed|gemini editor did not accept|incompatible selected tool/i.test(message) ? 'browser_submit_unavailable'
			: (/prompt editor contains another draft/i.test(message) ? 'browser_composer_busy'
			: (/image creation is rate limited/i.test(message) ? 'browser_rate_limited'
				: (/image creation tool|image tool menu/i.test(message) ? 'browser_image_tool_unavailable' : 'browser_automation_error')))));
      resolve({ ok: false, error: message, code });
    }
  });
}

function captureProgress(selectors) {
  const visibleText = (element) => (element?.innerText || element?.textContent || '').trim();
  const responseText = (element) => {
    if (!element) return '';
    if (element.querySelector('[data-testid="image-gen-loading-state"], [data-testid="image-gen-loading-progress"]')) return '';
    const markdownParts = [...element.querySelectorAll('[data-message-author-role="assistant"] .markdown, message-content .markdown')];
    if (markdownParts.length) return markdownParts.map(visibleText).filter(Boolean).join('\n');
    // A ChatGPT assistant turn without answer markup can contain only thinking
    // chrome (such as "Pro-Denkvorgang"); it is not user-facing answer text.
    if (element.matches?.('section[data-turn="assistant"]')) return '';
    if (element.querySelector('img') && element.matches?.('section[data-turn="assistant"], model-response')) return '';
    const assistantParts = [...element.querySelectorAll('[data-message-author-role="assistant"]')];
    if (assistantParts.length) return assistantParts.map(visibleText).filter(Boolean).join('\n');
    return visibleText(element);
  };
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
  const latestText = responses.length ? responseText(responses[responses.length - 1]) : '';
	const fallbackNotice = responses.at(-1)?.closest?.('.conversation-container')?.querySelector?.('peak-hour-fallback-disclaimer');
	const modelFallback = isVisible(fallbackNotice);
	const currentModel = String(document.querySelector?.('bard-mode-switcher button[aria-haspopup]')
		?.getAttribute('aria-label')?.match(/(?:derzeit ausgewählt|currently selected|selected)\s*:\s*(.+)$/i)?.[1] || '').trim();
  const alerts = ['[role="alert"]', '[aria-live="assertive"]', '[data-testid*="error" i]', '.toast-error', '.error-message']
    .flatMap((selector) => { try { return [...document.querySelectorAll(selector)].filter(isVisible); } catch (_) { return []; } })
    .map(visibleText).filter(Boolean).join(' ');
  const retryVisible = [...document.querySelectorAll('button')].filter(isVisible)
    .some((button) => /retry|try again|regenerate|erneut|noch einmal|wiederholen/i.test(`${visibleText(button)} ${button.getAttribute('aria-label') || ''}`));
  const failureText = alerts || (retryVisible ? latestText : '');
  if (/rate.?limit|usage.?limit|quota|capacity|limit erreicht|höchstgrenze erreicht|zu viele anfragen|too many requests|try again later|später erneut|temporarily unavailable|something went wrong|etwas ist schief/i.test(failureText)) {
    return { text: '', busy: false, percent: 0, detail: 'Provider error', current_model: currentModel };
  }
  return { text: latestText, busy, percent, detail: detail || (busy ? 'Generating' : ''), current_model: currentModel, model_fallback: modelFallback };
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
  heartbeatTimer = setInterval(() => sendHeartbeat('waiting'), 5000);
}

function stopHeartbeat() {
  if (heartbeatTimer) clearInterval(heartbeatTimer);
  heartbeatTimer = 0;
}

function sendHeartbeat(state) {
  if (heartbeatInFlight) return heartbeatInFlight;
  heartbeatInFlight = sendHeartbeatOnce(state).finally(() => { heartbeatInFlight = null; });
  return heartbeatInFlight;
}

async function sendHeartbeatOnce(state) {
  const cfg = await settings();
  if (!cfg.token) return;
  const tabs = [];
  for (const tabId of configuredTabIDs(cfg)) {
    try {
      const tab = await api.tabs.get(tabId);
      const profile = profileForTab(cfg, tab);
      let capabilities = cfg.tabCapabilities?.[tabId] || {};
      let dom = null;
      if (profile?.name === 'gemini' && !busyTabs.has(tabId)
          && Date.now() - Number(cfg.tabCapabilityScans?.[tabId] || 0) > 30 * 60 * 1000) {
        try {
          const safe = await api.scripting.executeScript({ target: { tabId }, func: safeToDiscoverPageCapabilities });
          if (safe?.[0]?.result === true) {
            const scanned = await api.scripting.executeScript({ target: { tabId }, func: discoverPageCapabilities });
            capabilities = scanned?.[0]?.result || capabilities;
            const latest = await api.storage.local.get({ tabCapabilities: {}, tabCapabilityScans: {} });
            await api.storage.local.set({
              tabCapabilities: { ...latest.tabCapabilities, [tabId]: capabilities },
              tabCapabilityScans: { ...latest.tabCapabilityScans, [tabId]: Date.now() }
            });
          }
        } catch (_) {}
      }
      try {
        const report = await api.scripting.executeScript({ target: { tabId }, func: inspectPageCapabilities });
        const live = report?.[0]?.result || {};
        const validModel = profile?.name === 'gemini'
          ? (value) => Boolean(String(value || '').trim() && String(value).length <= 100)
          : (value) => acceptedModelLabel.test(value);
        capabilities = {
          ...capabilities,
          currentModel: live.currentModel || (validModel(capabilities.currentModel || '') ? capabilities.currentModel : ''),
          currentReasoning: live.currentReasoning || capabilities.currentReasoning || '',
          models: [...new Set([...(capabilities.models || []), ...(live.models || [])])].filter(validModel),
          reasoningLevels: [...new Set([...(capabilities.reasoningLevels || []), ...(live.reasoningLevels || [])])]
        };
      } catch (_) {}
      // Selector-only diagnostics are safe to sample while a job is running.
      // They let the bridge distinguish a still-streaming answer from a stale
      // busy flag without copying prompts, responses, or the complete DOM.
      try {
        const snapshot = await api.scripting.executeScript({ target: { tabId }, func: inspectPageDOM, args: [profile?.selectors || {}] });
        dom = snapshot?.[0]?.result || null;
      } catch (_) {}
      tabs.push({
        id: tabId,
        origin: tab?.url && /^https?:/i.test(tab.url) ? new URL(tab.url).origin : '',
        title: tab?.title || '', profile: profile?.name || '',
        state: busyTabs.has(tabId) ? 'working' : (Number(cfg.tabCooldowns?.[tabId] || 0) > Date.now() ? 'rate_limited' : 'waiting'),
        current_model: capabilities.currentModel || '', current_reasoning: capabilities.currentReasoning || '',
        models: capabilities.models || [], reasoning_levels: capabilities.reasoningLevels || [],
        last_failure: cfg.tabFailures?.[tabId] || null, dom
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
    autoAttachFreshTabs: true,
    autoAttachBlockedTabIds: [],
    useVisualProfile: true,
    taughtProfiles: {},
    sessionTabs: {},
    tabCapabilities: {},
    tabCapabilityScans: {},
    tabFailures: {},
    tabCooldowns: {},
    pendingCompletions: {},
    lastError: ''
  });
}

function configuredTabIDs(cfg) {
  const values = Array.isArray(cfg?.tabIds) && cfg.tabIds.length ? cfg.tabIds : [cfg?.tabId];
  return [...new Set(values.map(Number).filter((value) => Number.isInteger(value) && value > 0))].slice(0, 16);
}

function safeToDiscoverPageCapabilities() {
  const composer = document.querySelector('rich-textarea [contenteditable="true"][role="textbox"], div.ql-editor[contenteditable="true"][role="textbox"]');
  if (!composer || String(composer.innerText || composer.textContent || '').trim()) return false;
  return ![...document.querySelectorAll('[aria-busy="true"], button[data-testid*="stop" i], button[aria-label*="stop" i]')]
    .some((element) => element.offsetWidth || element.offsetHeight || element.getClientRects().length);
}

function inspectPageCapabilities() {
  const visible = (element) => Boolean(element && (element.offsetWidth || element.offsetHeight || element.getClientRects().length));
  const text = (element) => String(element?.innerText || element?.textContent || element?.getAttribute?.('aria-label') || '').replace(/\s+/g, ' ').trim().slice(0, 100);
  const unique = (values, limit) => [...new Set(values.filter(Boolean))].slice(0, limit);
  const controls = [...document.querySelectorAll('button, [role="button"]')].filter(visible);
  const options = [...document.querySelectorAll('[role="menuitem"], [role="option"], [aria-checked], [aria-selected]')].filter(visible);
  const modelPattern = /^(?:gpt[\s._-]*\d+(?:\.\d+)?(?:[\s._-]*(?:astra|sol|terra|luna|pro|mini|nano|codex|thinking|instant))?|gemini(?:[\s._-]*\d+(?:\.\d+)?(?:[\s._-]*(?:pro|flash|lite|thinking|preview|experimental))*)?|astra|sol|terra|luna|\d+(?:\.\d+)?\s+(?:pro|flash|astra|sol|terra|luna))$/i;
  const reasoningPattern = /^(?:instant|sofort|fast|schnell|low|niedrig|medium|mittel|high|hoch|very high|sehr hoch|xhigh|pro|max|maximum)$/i;
  const semantic = (element, pattern) => pattern.test(`${element.getAttribute('data-testid') || ''} ${element.getAttribute('aria-label') || ''}`);
  const modelControl = (element) => semantic(element, /model[-_ ]?(?:switcher|selector|picker|menu)|(?:choose|select|current)[-_ ]?model|modellauswahl|modellmenü|modellmodus/i);
  const currentModel = text(controls.find((element) => {
    const label = `${text(element)} ${element.getAttribute('aria-label') || ''}`;
    return modelPattern.test(text(element)) && !/modelle ergänzen|add models|preismodell|pricing model/i.test(label);
  }));
  const currentReasoning = text(controls.find((element) => semantic(element, /reason|denk|effort|thinking/i) || reasoningPattern.test(text(element))));
  const geminiPicker = document.querySelector?.('bard-mode-switcher button[aria-haspopup]');
  const geminiCurrent = geminiPicker?.getAttribute('aria-label')?.match(/(?:derzeit ausgewählt|currently selected|selected)\s*:\s*(.+)$/i)?.[1]?.trim().slice(0, 100) || '';
  const geminiMenu = geminiPicker?.getAttribute('aria-controls') && document.getElementById?.(geminiPicker.getAttribute('aria-controls'));
  const visibleGeminiModels = geminiMenu ? [...geminiMenu.querySelectorAll('button, [role="menuitem"], [role="option"], [role="menuitemradio"]')]
    .filter((item) => visible(item) && !item.disabled && item.getAttribute('aria-disabled') !== 'true')
    .map((item) => {
      const primary = text(item.querySelector?.('.picker-primary-text, .mode-name, .model-name'));
      const secondary = text(item.querySelector?.('.picker-secondary-text'));
      return primary ? `${primary} ${secondary}`.trim() : text(item);
    }) : [];
  return {
    currentModel: geminiCurrent || currentModel,
    currentReasoning: geminiCurrent ? '' : currentReasoning,
    models: geminiCurrent ? unique(visibleGeminiModels, 50) : unique(options.map(text).filter((value) => modelPattern.test(value)), 50),
    reasoningLevels: geminiCurrent ? [] : unique(options.map(text).filter((value) => reasoningPattern.test(value)), 20)
  };
}

function inspectPageDOM(selectors) {
  const visible = (element) => Boolean(element && (element.offsetWidth || element.offsetHeight || element.getClientRects().length));
  const short = (value, limit = 120) => String(value || '').replace(/\s+/g, ' ').trim().slice(0, limit);
  const describe = (element, includeText = false) => ({
    tag: short(element.tagName?.toLowerCase(), 20),
    id: short(element.id, 100),
    test_id: short(element.getAttribute('data-testid'), 100),
    role: short(element.getAttribute('role'), 40),
    aria_label: short(element.getAttribute('aria-label')),
    text: includeText ? short(element.innerText) : '',
    type: short(element.type, 40),
    accept: short(element.accept),
    has_popup: short(element.getAttribute('aria-haspopup'), 20),
    expanded: short(element.getAttribute('aria-expanded'), 10),
    visible: visible(element),
    disabled: Boolean(element.disabled || element.getAttribute('aria-disabled') === 'true'),
    multiple: Boolean(element.multiple),
    directory: Boolean(element.webkitdirectory || element.hasAttribute('webkitdirectory'))
  });
  const bySelectors = (items, limit) => {
    const matches = [];
    const seen = new Set();
    for (const selector of items || []) {
      try {
        for (const element of document.querySelectorAll(selector)) {
          if (seen.has(element)) continue;
          seen.add(element);
          matches.push(element);
          if (matches.length === limit) return matches;
        }
      } catch (_) {}
    }
    return matches;
  };
  const inputs = bySelectors(selectors.input, 8);
  const submit = bySelectors(selectors.submit, 8);
  const fileInputs = [...document.querySelectorAll('input[type="file"]')].slice(0, 12);
  const composer = inputs[0]?.closest('[data-node-type="input-area"], form') || document.querySelector('form[data-type="unified-composer"]') || document.querySelector('form');
  const tools = [...(composer?.querySelectorAll('button, [role="button"]') || [])].filter(visible);
  const seenTools = new Set(tools);
  for (const element of document.querySelectorAll('[role="menuitem"], [role="option"]')) {
    if (visible(element) && !seenTools.has(element)) {
      tools.push(element);
      seenTools.add(element);
    }
    if (tools.length >= 32) break;
  }
  const responses = bySelectors(selectors.response, 10000);
  const latest = responses.at(-1);
  const busySelectors = [
    ['aria_busy', '[aria-busy="true"]'],
    ['streaming_attribute', '[data-is-streaming="true"]'],
    ['streaming_class', '.result-streaming'],
    ['image_loading', '[data-testid="image-gen-loading-state"], [data-testid="image-gen-loading-state-frame"]'],
    ['stop_button', 'button[data-testid*="stop" i], button[aria-label*="stop" i], button[aria-label*="beenden" i], button[aria-label*="abbrechen" i]']
  ];
  const busyIndicators = busySelectors.flatMap(([name, selector]) => {
    try { return [...document.querySelectorAll(selector)].some(visible) ? [name] : []; } catch (_) { return []; }
  });
  const latestMarkdown = latest?.querySelector?.('message-content .markdown, [data-message-author-role="assistant"] .markdown');
  const latestText = String(latestMarkdown?.innerText || latestMarkdown?.textContent || latest?.innerText || '').trim();
  const latestImages = latest ? [...latest.querySelectorAll('img')] : [];
  let latestResponseBusy = false;
  try { latestResponseBusy = Boolean(latest?.querySelectorAll?.('[aria-busy="true"], [data-is-streaming="true"], .result-streaming')?.length); } catch (_) {}
  let imageProgress = 0;
  for (const element of document.querySelectorAll('[data-testid="image-gen-loading-progress"], [role="progressbar"][aria-valuenow]')) {
    if (visible(element)) imageProgress = Math.max(imageProgress, Number(element.getAttribute('aria-valuenow')) || 0);
  }
  const relevant = /bild|image|file|datei|ordner|folder|upload|attach|tool|werkzeug|auswahl von|selection of|deselect/i;
  const inputCharacters = String(inputs[0]?.value || inputs[0]?.innerText || inputs[0]?.textContent || '').trim().length;
  return {
    captured_at: new Date().toISOString(),
    inputs: inputs.map((element) => describe(element)),
    input_has_text: inputCharacters > 0,
    input_characters: Math.min(inputCharacters, 100000),
    submit: submit.map((element) => describe(element)),
    file_inputs: fileInputs.map((element) => describe(element)),
    tools: tools.slice(0, 32).map((element) => {
      const item = describe(element, true);
      if (!relevant.test(`${item.text} ${item.aria_label} ${item.test_id}`)) item.text = '';
      return item;
    }),
    assistant_turns: responses.length,
    last_response_characters: Math.min(latestText.length, 100000),
    last_response_busy: latestResponseBusy,
    busy_indicators: busyIndicators,
    last_response_images: latestImages.length,
    last_response_loaded_images: latestImages.filter((image) => image.complete && image.naturalWidth >= 128 && image.naturalHeight >= 128).length,
    image_progress: Math.max(0, Math.min(100, imageProgress))
  };
}

async function discoverPageCapabilities() {
  const visible = (element) => Boolean(element && (element.offsetWidth || element.offsetHeight || element.getClientRects().length));
  const text = (element) => String(element?.innerText || element?.textContent || element?.getAttribute?.('aria-label') || '').replace(/\s+/g, ' ').trim().slice(0, 100);
  const unique = (values, limit) => [...new Set(values.filter(Boolean))].slice(0, limit);
  const wait = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
  const modelPattern = /^(?:gpt[\s._-]*\d+(?:\.\d+)?(?:[\s._-]*(?:astra|sol|terra|luna|pro|mini|nano|codex|thinking|instant))?|gemini(?:[\s._-]*\d+(?:\.\d+)?(?:[\s._-]*(?:pro|flash|lite|thinking|preview|experimental))*)?|astra|sol|terra|luna|\d+(?:\.\d+)?\s+(?:pro|flash|astra|sol|terra|luna))$/i;
  const reasoningPattern = /^(?:instant|sofort|fast|schnell|low|niedrig|medium|mittel|high|hoch|very high|sehr hoch|xhigh|pro|max|maximum)$/i;
  const semantic = (element, pattern) => pattern.test(`${element.getAttribute('data-testid') || ''} ${element.getAttribute('aria-label') || ''}`);
  const modelControl = (element) => semantic(element, /model[-_ ]?(?:switcher|selector|picker|menu)|(?:choose|select|current)[-_ ]?model|modellauswahl|modellmenü|modellmodus/i);
  const geminiPicker = document.querySelector?.('bard-mode-switcher button[aria-haspopup]');
  if (geminiPicker && visible(geminiPicker)) {
    const currentModel = geminiPicker.getAttribute('aria-label')?.match(/(?:derzeit ausgewählt|currently selected|selected)\s*:\s*(.+)$/i)?.[1]?.trim().slice(0, 100) || text(geminiPicker);
    geminiPicker.click();
    await wait(350);
    const controlled = geminiPicker.getAttribute('aria-controls');
    const menu = (controlled && document.getElementById(controlled))
      || [...document.querySelectorAll('[role="menu"], .cdk-overlay-pane')].filter(visible).at(-1);
    const modelLabel = (element) => {
      const primary = text(element.querySelector?.('.picker-primary-text, .mode-name, .model-name'));
      const secondary = text(element.querySelector?.('.picker-secondary-text'));
      if (primary) return `${primary} ${secondary}`.trim().slice(0, 100);
      const label = String(element.getAttribute?.('aria-label') || '').trim();
      return (label || String(element.innerText || '').split('\n').map((part) => part.trim()).filter(Boolean)[0] || '').slice(0, 100);
    };
    const choices = menu ? [...menu.querySelectorAll('button, [role="menuitem"], [role="option"], [role="menuitemradio"], mat-option')]
      .filter((element) => visible(element) && !element.disabled && element.getAttribute('aria-disabled') !== 'true') : [];
    const models = unique(choices.map(modelLabel).filter((value) => value && value.length <= 100 && !/^(?:close|schließen|back|zurück|help|hilfe|upgrade|upgraden)$/i.test(value)), 50);
    geminiPicker.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', code: 'Escape', bubbles: true }));
    await wait(150);
    return { currentModel, currentReasoning: '', models, reasoningLevels: [] };
  }
  const scan = async (kind) => {
    const pattern = kind === 'model' ? modelPattern : reasoningPattern;
    const triggers = [...document.querySelectorAll('button, [role="button"]')].filter(visible).filter((element) => {
      const label = `${text(element)} ${element.getAttribute('aria-label') || ''}`;
      if (/modelle ergänzen|add models|preismodell|pricing model/i.test(label)) return false;
      if (kind === 'model') return modelControl(element) || modelPattern.test(text(element));
      return semantic(element, /reason|denk|effort|thinking/i) || reasoningPattern.test(text(element));
    });
    if (!triggers.length) return { current: '', values: [] };
    const current = modelPattern.test(text(triggers[0])) ? text(triggers[0]) : '';
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
