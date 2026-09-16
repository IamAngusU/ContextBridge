if (!globalThis.ContextBridgeProfiles && typeof importScripts === 'function') {
  importScripts('profiles.js');
}

const api = globalThis.browser || globalThis.chrome;

let stopRequested = false;
let heartbeatTimer = 0;
let heartbeatInFlight = null;
let heartbeatFailures = 0;
let nextHeartbeatAt = 0;
let heartbeatRequestId = 0;
let lastAppliedHeartbeatId = 0;
const HEARTBEAT_ALARM = 'contextbridge-heartbeat';
let heartbeatAlarmRegistered = false;
let heartbeatAlarmWanted = false;
let heartbeatAlarmSync = Promise.resolve(false);
let finishedTabCleanup = null;
let pairingInFlight = null;
let lifecycleGeneration = 0;
let lifecycleWrite = Promise.resolve();
const diagnosticsInFlight = new Map();
const diagnosticScriptsInFlight = new Map();
const progressScriptsInFlight = new Map();
const tabDOMDiagnostics = new Map();
const capabilityWatchInstalled = new Set();
const capabilityInteractionTimers = new Map();
const capabilityForcePending = new Set();
let connectionStartedAt = 0;
let connectionPhase = '';
let connectionPhaseStartedAt = 0;
const pollers = new Map();
const pollAbortControllers = new Map();
const busyTabs = new Set();
const freshTabChecks = new Map();
let freshTabWrite = Promise.resolve();
let sessionWrite = Promise.resolve();
let capabilityWrite = Promise.resolve();
let browserClaimWrite = Promise.resolve();
const activeBrowserLeases = new Map();
const recoveredTabReservations = new Map();
const rejectedBrowserClaims = new Set();
const leaseRecoveryTasks = new Set();
const PENDING_COMPLETION_MAX_BYTES = 512 * 1024;
const acceptedModelLabel = /^(?:gpt[\s._-]*\d+(?:\.\d+)?(?:[\s._-]*(?:astra|sol|terra|luna|pro|mini|nano|codex|thinking|instant))?|gemini(?:[\s._-]*\d+(?:\.\d+)?(?:[\s._-]*(?:pro|flash|lite|thinking|preview|experimental))*)?|astra|sol|terra|luna|\d+(?:\.\d+)?\s+(?:pro|flash|astra|sol|terra|luna))$/i;

// Do not resume eagerly while this module is being evaluated. On Chromium an
// MV3 worker is loaded before the event that woke it is dispatched; an eager
// normal resume can therefore race the stricter restart policy and briefly
// reconnect even when the user disabled automatic reconnect. Each wake path
// below supplies the lifecycle intent that applies to it.
api.runtime.onInstalled.addListener(() => resume(true));
api.runtime.onStartup.addListener(() => resume(true));
// Chromium MV3 may suspend the service worker despite an interval or a
// pending long poll. An alarm wakes a fresh worker so it can re-register the
// tabs and restart pollers without requiring the user to reopen the popup.
api.alarms?.onAlarm?.addListener(async (alarm) => {
  if (alarm.name === HEARTBEAT_ALARM) {
    // A delivered periodic alarm is known to exist in this worker instance.
    // resume() still verifies it through alarms.get(), because Chromium may
    // discard alarms when the browser restarts.
    heartbeatAlarmRegistered = true;
    await resume();
  }
});
api.tabs.onUpdated?.addListener((tabId, changeInfo, tab) => {
  if (changeInfo.status !== 'complete') return;
  tabDOMDiagnostics.delete(tabId);
  diagnosticScriptsInFlight.delete(tabId);
  progressScriptsInFlight.delete(tabId);
  capabilityWatchInstalled.delete(tabId);
  scheduleFreshTabCheck(tabId, tab?.url || changeInfo.url || '');
  void invalidateTabCapabilities(tabId).then(() => settings()).then((cfg) => {
    if (cfg.running && configuredTabIDs(cfg).includes(tabId)) return sendHeartbeat('waiting');
  }).catch(() => {});
});
api.tabs.onRemoved?.addListener((tabId) => {
  if (freshTabChecks.has(tabId)) clearTimeout(freshTabChecks.get(tabId));
  freshTabChecks.delete(tabId);
  tabDOMDiagnostics.delete(tabId);
  diagnosticScriptsInFlight.delete(tabId);
  progressScriptsInFlight.delete(tabId);
  capabilityWatchInstalled.delete(tabId);
  capabilityForcePending.delete(tabId);
  if (capabilityInteractionTimers.has(tabId)) clearTimeout(capabilityInteractionTimers.get(tabId));
  capabilityInteractionTimers.delete(tabId);
  for (const lease of [...activeBrowserLeases.values()]) {
    if (lease.tabId === tabId) cancelRecoveredBrowserLease(lease, true);
  }
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
    case 'release-session-tab':
      return releaseSessionTab(Number(message.tabId));
    case 'set-tab-edit-mode':
      return setTabEditMode(Number(message.tabId), message.enabled === true);
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
    case 'page-capability-interaction':
      return notePageCapabilityInteraction(sender);
    case 'authorize-browser-job-action':
      return authorizeBrowserJobAction(message, sender);
    case 'remove-profile':
      return removeTaughtProfile(String(message.origin || ''));
    default:
      return { ok: false, error: 'Unknown extension message' };
  }
}

function startPairing() {
  if (pairingInFlight) return pairingInFlight;
  pairingInFlight = startPairingOnce().finally(() => { pairingInFlight = null; });
  return pairingInFlight;
}

async function startPairingOnce() {
  const recoveryTask = beginBrowserLeaseRecovery();
  const pendingGeneration = lifecycleGeneration;
  try {
    const cfg = await settings();
    if (!cfg.token) throw new Error('Enter the pairing token once in Advanced settings');
    const tabIds = configuredTabIDs(cfg);
    if (!tabIds.length) throw new Error('Select at least one AI tab first');
    connectionStartedAt = Date.now();
    await reportConnectionProgress('Checking selected tabs', 0, tabIds.length);
    for (let index = 0; index < tabIds.length; index += 1) {
      const tab = await withDeadline(api.tabs.get(tabIds[index]), 2000);
      if (!tab?.url || !/^https?:/i.test(tab.url)) throw new Error('One selected tab is not a supported web page');
      if (cfg.useVisualProfile && !profileForTab(cfg, tab)) throw new Error(`Detect or customize ${tab.title || 'the selected page'} before starting the bridge`);
      await reportConnectionProgress('Checking selected tabs', index + 1, tabIds.length);
    }
    await reportConnectionProgress('Contacting local service', 0, 1);
    const ready = await testBridge();
    if (!ready.ok) throw new Error(ready.error || 'Local ContextBridge service is unavailable');

    if (pendingGeneration !== lifecycleGeneration) throw new Error('The connection attempt was cancelled');
    const generation = ++lifecycleGeneration;
    stopRequested = false;
    if (!await persistLifecycleState(generation, { running: true, relayConnected: false, teachingTabId: 0, lastError: '' })) {
      throw new Error('The connection attempt was cancelled');
    }
    const heartbeatReady = await sendHeartbeat('waiting', true);
    if (!heartbeatReady) {
      if (generation !== lifecycleGeneration) throw new Error('The connection attempt was cancelled');
      stopRequested = true;
      const stopGeneration = ++lifecycleGeneration;
      await persistLifecycleState(stopGeneration, { running: false, relayConnected: false });
      const reason = (await api.storage.local.get({ connectionError: '' })).connectionError;
      throw new Error(reason || 'Local ContextBridge did not accept the browser connection; check the service status');
    }
    if (generation !== lifecycleGeneration) throw new Error('The connection attempt was cancelled');
    await restorePersistedBrowserLeaseReservations(generation);
    if (generation !== lifecycleGeneration) throw new Error('The connection attempt was cancelled');
    await startHeartbeat();
    if (generation !== lifecycleGeneration) throw new Error('The connection attempt was cancelled');
    poll();
    void sendHeartbeat('waiting'); // Fill in model and DOM diagnostics after the confirmed handshake.
    void discoverFreshTabs();
    return { ok: true };
  } finally {
    finishBrowserLeaseRecovery(recoveryTask);
    connectionStartedAt = 0;
    await api.storage.local.set({ connectionProgress: null });
  }
}

async function reportConnectionProgress(phase, done, total) {
  if (phase !== connectionPhase) { connectionPhase = phase; connectionPhaseStartedAt = Date.now(); }
  const elapsed = connectionPhaseStartedAt ? Date.now() - connectionPhaseStartedAt : 0;
  const etaSeconds = done > 0 && done < total ? Math.ceil(elapsed * (total - done) / done / 1000) : null;
  await api.storage.local.set({ connectionProgress: { phase, done, total, etaSeconds } });
}

function persistLifecycleState(generation, changes) {
  // chrome.storage has no compare-and-swap and promises may settle out of
  // order. Serialize lifecycle writes, then discard an operation whose intent
  // was superseded before it reached storage. A newer queued lifecycle write
  // therefore always becomes the final persisted running/relay state.
  const operation = lifecycleWrite.catch(() => {}).then(async () => {
    if (generation !== lifecycleGeneration) return false;
    await api.storage.local.set(changes);
    return generation === lifecycleGeneration;
  });
  lifecycleWrite = operation.catch(() => false);
  return operation;
}

function beginBrowserLeaseRecovery() {
  let finish;
  const task = new Promise((resolve) => { finish = resolve; });
  task.finish = finish;
  leaseRecoveryTasks.add(task);
  // An alarm can wake recovery while an older /next request is pending in the
  // same worker. Abort it synchronously so it cannot lease a second job for a
  // tab whose durable claim is about to be restored.
  for (const controller of pollAbortControllers.values()) controller.abort();
  return task;
}

function finishBrowserLeaseRecovery(task) {
  if (!task || !leaseRecoveryTasks.has(task)) return;
  leaseRecoveryTasks.delete(task);
  task.finish();
}

async function waitForBrowserLeaseRecovery() {
  // New alarm/startup work can begin while an older gate settles. Repeat
  // until no recovery task remains so a poll never slips between two gates.
  while (leaseRecoveryTasks.size) {
    await Promise.allSettled([...leaseRecoveryTasks]);
  }
}

async function stopPairing() {
  const generation = ++lifecycleGeneration;
  stopRequested = true;
  for (const lease of [...activeBrowserLeases.values()]) cancelRecoveredBrowserLease(lease, true);
  recoveredTabReservations.clear();
  busyTabs.clear();
  if (!await persistLifecycleState(generation, { running: false, relayConnected: false, teachingTabId: 0 })) return { ok: true };
  await stopHeartbeat();
  if (generation !== lifecycleGeneration) return { ok: true };
  await sendHeartbeat('paused');
  return { ok: true };
}

async function resume(afterExtensionOrBrowserRestart = false) {
  const recoveryTask = beginBrowserLeaseRecovery();
  const generation = lifecycleGeneration;
  try {
    const { running, autoReconnect } = await api.storage.local.get({ running: false, autoReconnect: true });
    if (generation !== lifecycleGeneration) return;
    if (!running) {
      await stopHeartbeat();
      if (generation !== lifecycleGeneration) return;
      await api.storage.local.set({ connectionProgress: null });
      return;
    }
    if (afterExtensionOrBrowserRestart && !autoReconnect) {
      const stopGeneration = ++lifecycleGeneration;
      stopRequested = true;
      if (!await persistLifecycleState(stopGeneration, { running: false, relayConnected: false, connectionProgress: null,
        connectionError: 'Automatic reconnect is off. Click Connect when you are ready.' })) return;
      await stopHeartbeat();
      if (stopGeneration !== lifecycleGeneration) return;
      await sendHeartbeat('paused');
      return;
    }
    stopRequested = false;
    await restorePersistedBrowserLeaseReservations(generation);
    if (generation !== lifecycleGeneration) return;
    await startHeartbeat();
    if (generation !== lifecycleGeneration) return;
    poll();
    await sendHeartbeat(busyTabs.size ? 'working' : 'waiting');
    if (generation !== lifecycleGeneration) return;
    void discoverFreshTabs();
  } finally {
    finishBrowserLeaseRecovery(recoveryTask);
  }
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
  const tabEditModes = { ...cfg.tabEditModes };
  delete tabCapabilities[tabId];
  delete tabCapabilityScans[tabId];
  delete tabFailures[tabId];
  delete tabEditModes[tabId];
  await serializeSessionWrite(async () => {
    const latest = await settings();
    const sessionBindings = { ...latest.sessionBindings };
    for (const [key, binding] of Object.entries(sessionBindings)) {
      if (Number(binding?.tabId) !== tabId) continue;
      // A per-job binding has no durable conversation identity. Once its tab
      // is gone, retaining the entry can only grow storage.local and can never
      // make a later routing decision safer.
      if (binding.autoCreated && binding.perJob) { delete sessionBindings[key]; continue; }
      if (binding.url && !isFreshChatURL(binding.url) && !binding.legacy) sessionBindings[key] = { ...binding, tabId: 0 };
      else delete sessionBindings[key];
    }
    await api.storage.local.set({ sessionBindings });
  });
  await removeOwnedDraft(tabId);
  if (!configuredTabIDs(cfg).includes(tabId)) {
    await api.storage.local.set({ autoAttachBlockedTabIds, tabCapabilities, tabCapabilityScans, tabFailures, tabEditModes });
    return;
  }
  const tabIds = configuredTabIDs(cfg).filter((id) => id !== tabId);
  await api.storage.local.set({ tabId: tabIds[0] || 0, tabIds, tabCapabilities, tabCapabilityScans, tabFailures, tabEditModes, autoAttachBlockedTabIds });
  if (cfg.running) await sendHeartbeat('waiting');
}

async function testBridge() {
  const cfg = await settings();
  try {
    if (!cfg.token) return { ok: false, error: 'Enter the pairing token once in Advanced settings' };
    const response = await fetchWithTimeout(`${cfg.bridgeUrl}/v1/status`, {
      headers: { Authorization: `Bearer ${cfg.token}` }, cache: 'no-store'
    }, 5000);
    if (response.status === 401) return { ok: false, error: 'Pairing token is invalid; check Advanced settings' };
    if (!response.ok) return { ok: false, error: `Local ContextBridge service returned HTTP ${response.status}` };
    const data = await response.json();
    return { ok: Boolean(data.ok), version: data.version || '', error: data.ok ? '' : 'Local ContextBridge service is not ready' };
  } catch (error) {
    return { ok: false, error: 'Local ContextBridge service is unreachable; check that the desktop worker is running' };
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
      const pageHealth = summarizePageHealth(tab, tabDOMDiagnostics.get(tabId));
      const capabilities = capabilitiesForTab(cfg, tab, profileForTab(cfg, tab));
      tabs.push({
        ...tabSummary(tab),
        busy,
        state: busy ? 'working' : (coolingDown ? 'rate_limited' : 'waiting'),
        profile: profileForTab(cfg, tab)?.name || '',
        currentModel: capabilities.currentModel || '',
        lastFailure: cfg.tabFailures?.[tabId] || null,
        pageHealth
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

// This summary is deliberately content-free. It lets the popup explain a
// sleeping or unusually large provider page without exposing any prompt or
// answer text and without changing provider-owned DOM.
function summarizePageHealth(tab, diagnostic) {
  const dom = diagnostic?.dom || {};
  const visibility = ['visible', 'hidden', 'prerender'].includes(dom.page_visibility)
    ? dom.page_visibility : 'unknown';
  return {
    domStatus: ['ready', 'pending', 'timeout', 'unavailable', 'error'].includes(diagnostic?.status)
      ? diagnostic.status : 'pending',
    diagnosticMs: Math.max(0, Math.min(10000, Number(diagnostic?.latencyMs) || 0)),
    slowScans: Math.max(0, Math.min(10, Number(diagnostic?.slowScans) || 0)),
    assistantTurns: Math.max(0, Math.min(10000, Number(dom.assistant_turns) || 0)),
    userTurns: Math.max(0, Math.min(10000, Number(dom.gemini_user_turns) || 0)),
    visibility,
    discarded: Boolean(tab?.discarded || dom.was_discarded)
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
  const generation = ++lifecycleGeneration;
  stopRequested = true;
  if (!await persistLifecycleState(generation, { tabId, tabIds: [tabId], teachingTabId: tabId, running: false })) {
    return { ok: true, tab: tabSummary(tab), existing: Boolean(existing) };
  }
  await stopHeartbeat();
  if (generation !== lifecycleGeneration) return { ok: true, tab: tabSummary(tab), existing: Boolean(existing) };
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
  const cfg = await settings();
  const tab = await api.tabs.get(tabId);
  if (!['chatgpt', 'gemini'].includes(profileForTab(cfg, tab)?.name)) {
    throw new Error('Scan model choices works only on a ChatGPT or Gemini tab');
  }
  const results = await api.scripting.executeScript({ target: { tabId }, func: discoverPageCapabilities });
  const capabilities = { ...(results?.[0]?.result || {}), pageContext: capabilityPageContext(tab, profileForTab(cfg, tab)) };
  capabilities.scanDiagnostic = { ...(capabilities.scanDiagnostic || {}), version: api.runtime.getManifest().version };
  const tabCapabilities = { ...(cfg.tabCapabilities || {}), [tabId]: capabilities };
  const tabCapabilityScans = { ...(cfg.tabCapabilityScans || {}), [tabId]: Date.now() };
  await api.storage.local.set({ tabCapabilities, tabCapabilityScans });
  return { ok: true, capabilities };
}

async function notePageCapabilityInteraction(sender) {
  const tabId = Number(sender?.tab?.id || 0);
  if (!Number.isInteger(tabId) || tabId <= 0) return { ok: false };
  const cfg = await settings();
  if (!cfg.running || !configuredTabIDs(cfg).includes(tabId) || busyTabs.has(tabId)) return { ok: false };
  const tab = await api.tabs.get(tabId).catch(() => null);
  if (!tab || (sender.tab.url && sender.tab.url !== tab.url)
      || !['chatgpt', 'gemini'].includes(profileForTab(cfg, tab)?.name)) return { ok: false };
  if (capabilityInteractionTimers.has(tabId)) clearTimeout(capabilityInteractionTimers.get(tabId));
  capabilityInteractionTimers.set(tabId, setTimeout(() => {
    capabilityInteractionTimers.delete(tabId);
    scheduleTabDiagnostics(tabId, true);
  }, 900));
  return { ok: true };
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
    return cfg.taughtProfiles?.[new URL(tab.url).origin] || globalThis.ContextBridgeProfiles?.forURL(tab.url) || null;
  } catch (_) {
    return null;
  }
}

function routingProfileName(cfg, tab) {
  if (!cfg.useVisualProfile) return String(cfg.profile || '').trim().slice(0, 50);
  return String(profileForTab(cfg, tab)?.name || cfg.profile || '').trim().slice(0, 50);
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
      await waitForBrowserLeaseRecovery();
      const cfg = await settings();
      if (!cfg.running || !cfg.token || !configuredTabIDs(cfg).includes(tabId)) break;
      if (cfg.relayConnected === false) {
        await delay(2000);
        continue;
      }
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
		try { requestedProfile = routingProfileName(cfg, await api.tabs.get(tabId)) || requestedProfile; } catch (_) {}
        const pollController = new AbortController();
        pollAbortControllers.set(tabId, pollController);
        let response;
        try {
          response = await fetchWithTimeout(`${cfg.bridgeUrl}/v1/browser/jobs/next?wait=25&profile=${encodeURIComponent(requestedProfile)}&tab_id=${encodeURIComponent(tabId)}`, {
          headers: { Authorization: `Bearer ${cfg.token}` },
          cache: 'no-store', signal: pollController.signal
          }, 35000);
        } finally {
          if (pollAbortControllers.get(tabId) === pollController) pollAbortControllers.delete(tabId);
        }
        if (response.status === 204) continue;
        if (!response.ok) throw new Error(`Bridge returned ${response.status}`);
        const work = await response.json();
		const requiredTabId = Number(work?.job?.contextbridge_browser_tab_id || 0);
		if (requiredTabId > 0 && requiredTabId !== tabId) {
			await releaseBrowserLease(await settings(), work.job.id, Number(work.lease_generation || 0));
			throw new Error('The local bridge leased this job to a different browser tab');
		}
        await waitForBrowserLeaseRecovery();
        if (stopRequested) break;
        if (busyTabs.has(tabId)) {
          // A startup/alarm recovery may have begun just after /next returned.
          // Return this untouched lease instead of holding a second job behind
          // the recovered provider action on the same tab.
          await releaseBrowserLease(await settings(), work.job.id, Number(work.lease_generation || 0));
          continue;
        }
        const pending = cfg.pendingCompletions[work?.job?.id];
        if (pending) {
          const pendingGeneration = Number(pending.lease_generation || 0);
          if (pendingGeneration === Number(work.lease_generation || 0)) {
            await completeWork(cfg, work.job.id, pendingCompletionDecision(pending), pendingGeneration);
            continue;
          }
          const pendingCompletions = { ...cfg.pendingCompletions };
          delete pendingCompletions[work.job.id];
          await api.storage.local.set({ pendingCompletions });
        }
        const latest = await settings();
        if (!latest.running || !configuredTabIDs(latest).includes(tabId)) {
          await completeWork(latest, work.job.id, { mode: outputMode(work.job.output || {}), error: 'browser_tab_detached', model: 'browser' }, Number(work.lease_generation || 0));
          continue;
        }
        await processWork(cfg, work, tabId);
      } catch (error) {
        if (error?.name === 'AbortError' && leaseRecoveryTasks.size) continue;
        await api.storage.local.set({ lastError: error.message || String(error) });
        await delay(2000);
      }
  }
}

async function processWork(cfg, work, claimedTabId) {
  if (work?.job?.metadata?.contextbridge_new_chat_per_job === true
      || (cfg.sessionMode === 'new_chat_per_job' && work?.job?.metadata?.contextbridge_new_chat_per_job !== false)) {
    work.job.metadata = { ...(work.job.metadata || {}), contextbridge_new_chat_per_job: true, contextbridge_new_chat: true };
  }
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
  let jobCompleted = false;
  let submittedTurn = null;
  const leaseGeneration = Number(work?.lease_generation || 0);
  const observationOnly = work?.observation_only === true;
  let leaseState = null;
  let failureCode = 'browser_session_unavailable';
  let failureReason = 'other';
  await api.storage.local.set({ lastError: '' });
  try {
    if (!Number.isSafeInteger(leaseGeneration) || leaseGeneration <= 0) {
      failureCode = 'browser_lease_lost';
      throw new Error('The browser job did not include a valid lease generation');
    }
    // Own and renew the lease before a fresh background tab is created. A
    // provider page can take tens of seconds to mount, and that setup time is
    // part of this claimed job rather than an unleased pre-processing phase.
    // The routing tab is not yet the owned execution tab when per-job mode
    // creates a new page, so do not bind removal cancellation until resolve.
    leaseState = { jobId: String(work.job.id || ''), generation: leaseGeneration, tabId: 0, cancelled: false,
      sessionKey: workSessionKey(work), prompt: String(work.job.prompt || ''),
      profileName: String(work.profile?.name || ''), sentUnknown: observationOnly };
    activeBrowserLeases.set(leaseState.jobId, leaseState);
    failureCode = 'browser_lease_lost';
    await requireActiveBrowserLease(cfg, leaseState);
    const initialLeaseMilliseconds = Math.max(3000, (Date.parse(work.lease_expires_at || '') || Date.now() + 75000) - Date.now());
    leaseTimer = setInterval(() => {
      if (leaseState.cancelled || leaseState.renewing) return;
      leaseState.renewing = true;
      renewLease(cfg, work.job.id, leaseGeneration)
        .then((ok) => {
          if (!ok) {
            leaseState.cancelReason = 'renewal_rejected';
            leaseState.cancelled = true;
          }
        })
        // A transport failure is not authoritative lease loss. The next
        // renewal or action gate retries, while a real HTTP 409 cancels.
        .catch((error) => { leaseState.lastRenewalError = error?.message || String(error); })
        .finally(() => { leaseState.renewing = false; });
    }, Math.max(1000, Math.min(25000, Math.floor(initialLeaseMilliseconds / 3))));
    tabId = await resolveWorkTab(cfg, work, claimedTabId);
    leaseState.tabId = tabId;
    await waitForTabSlot(tabId, work.deadline);
    tabSlotHeld = true;
    await assertSessionTab(workSessionKey(work), tabId);
    const liveSettings = await settings();
    const binding = liveSettings.sessionBindings?.[workSessionKey(work)];
    const editEnabled = liveSettings.tabEditModes?.[tabId] === true;
    let editTarget = null;
    if (editEnabled) {
      failureCode = 'browser_edit_unavailable';
      if (!supportsTabEditJob(work.job)) {
        throw new Error('Edit mode currently supports text-only jobs; files and media need a new message');
      }
      if (binding?.ownedTurn) editTarget = binding.ownedTurn;
      else if (!isFreshChatURL(binding?.url)) throw new Error('Edit mode needs a new empty chat for its first ContextBridge message; no existing user message was changed');
    }
    failureCode = 'browser_automation_error';
    const previousFailures = (await api.storage.local.get({ tabFailures: {} })).tabFailures;
    if (previousFailures[tabId]) {
      const tabFailures = { ...previousFailures };
      delete tabFailures[tabId];
      await api.storage.local.set({ tabFailures });
    }
    tab = await api.tabs.get(tabId);
    leaseState.expectedURL = String(tab?.url || '');
    const taught = cfg.useVisualProfile ? profileForTab(cfg, tab) : null;
    if (taught) effectiveProfile = taught;
    leaseState.profileName = String(effectiveProfile?.name || leaseState.profileName || '');
    if (!effectiveProfile?.selectors) throw new Error('No usable page profile is available');
    if (!matches(tab.url || '', effectiveProfile.match_url || '')) throw new Error('The selected tab no longer matches its taught page');
    await sendHeartbeat('working');
    const rejectVisibleRateLimit = async () => {
      const snapshot = await captureTabProgress(tabId, effectiveProfile.selectors, leaseState);
      if (snapshot.blocking_provider_error_code !== 'browser_rate_limited') return;
      failureCode = 'browser_rate_limited';
      failureReason = 'provider_rate_limit_modal';
      await coolDownTab(tabId, 5 * 60 * 1000, effectiveProfile.name);
      progressSequence += 1;
      await reportProgress(cfg, work.job.id, { sequence: progressSequence, text: '', phase: 'rate_limited',
        detail: 'Provider rate-limit dialog is visible; no prompt was sent', busy: false });
      throw new Error('Provider rate-limit dialog is visible; no prompt was sent');
    };
    // A blocking provider dialog can still leave the composer mounted below
    // it. Check before guarded recovery, draft handling, or model selection.
    await rejectVisibleRateLimit();
    if (!observationOnly && effectiveProfile.name === 'chatgpt') {
      failureCode = 'browser_provider_busy';
      await requireActiveBrowserLease(cfg, leaseState);
      await recoverPriorStall(work, tabId, effectiveProfile, binding?.ownedTurn || null, cfg, leaseState);
      failureCode = 'browser_automation_error';
      tab = await api.tabs.get(tabId);
      await assertSessionTab(workSessionKey(work), tabId);
    }
    // A tab can be registered before its provider UI mounts. Never type into
    // that tab until a real prompt control is visible; opening it manually is
    // not required, and an unready page fails without touching a draft.
    await waitForTabReady(tabId, effectiveProfile.selectors, 30000, 0);
    await rejectVisibleRateLimit();
    if (!observationOnly && (cfg.preserveDrafts || cfg.ownedDrafts?.[tabId])) {
      try {
        await requireActiveBrowserLease(cfg, leaseState);
        await preserveAndClearDraft(cfg, tabId, tab, effectiveProfile, work.job, leaseState);
      } catch (error) {
        failureCode = /ChatGPT still shows Stop/i.test(error.message || '')
          ? 'browser_provider_busy' : 'browser_draft_preservation_failed';
        throw error;
      }
    }
    const initial = await captureTabProgress(tabId, effectiveProfile.selectors, leaseState);
    baselineText = initial.text || '';
    latestProgressText = baselineText;
    latestProgressState = `${Boolean(initial.busy)}|${Number(initial.percent) || 0}|${initial.detail || ''}|false`;
    const sample = async () => {
      if (progressBusy) return;
      progressBusy = true;
      try {
        const snapshot = await captureTabProgress(tabId, effectiveProfile.selectors, leaseState);
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
		// aria-busy can survive on an older Gemini turn. Only a live Stop or
		// streaming signal may put provisional assistant text on the relay.
		const newAssistantTurn = isNewAssistantTurn(initial, snapshot)
		  || Boolean(editTarget && isEditAssistantTurn(initial, snapshot, text, baselineText));
		const textChanged = Boolean(newAssistantTurn && snapshot.active_generation && text && text !== baselineText && text !== latestProgressText && !modeMismatch
		  && !work.job.image_base64
		  && Number(work.job.output?.min_images || 0) === 0);
		const progressState = `${Boolean(snapshot.busy)}|${Number(snapshot.percent) || 0}|${snapshot.detail || ''}|${modeMismatch}`;
        if (textChanged || progressState !== latestProgressState) {
          if (textChanged) latestProgressText = text;
          latestProgressState = progressState;
          progressSequence += 1;
          await reportProgress(cfg, work.job.id, {
            sequence: progressSequence,
            text: textChanged ? boundedUTF8(text, outputByteLimit(work.job.output || {})).text : '',
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
    let automationJob = work.job;
    if (observationOnly) {
      const persistedClaim = (await settings()).browserJobClaims?.[work.job.id];
      if (!persistedClaim || persistedClaim.sessionKey !== workSessionKey(work)) {
        failureCode = 'browser_observation_unavailable';
        throw new Error('This job may already have been sent, but its durable observation claim is unavailable; it was not sent again');
      }
      automationJob = { ...work.job, metadata: { ...(work.job.metadata || {}), contextbridge_resume_only: true,
        contextbridge_baseline_response_count: Number(persistedClaim.baselineCount) || 0,
        contextbridge_baseline_response_identity: String(persistedClaim.baselineIdentity || ''),
        contextbridge_baseline_text_digest: String(persistedClaim.baselineDigest || '') } };
    } else {
      await rememberBrowserJobClaim(work, tabId, (await assertSessionTab(workSessionKey(work), tabId)).tab.url, initial, effectiveProfile.name);
    }
    try {
      await requireActiveBrowserLease(cfg, leaseState);
      const guarded = await assertSessionTab(workSessionKey(work), tabId);
      leaseState.expectedURL = guarded.tab.url;
      if (!observationOnly && !editTarget) await markOwnedDraft(tabId, guarded.tab, work.job);
      const results = await api.scripting.executeScript({ target: { tabId }, func: automate,
        args: [automationJob, effectiveProfile, work.deadline, editTarget, guarded.tab.url,
          { jobId: work.job.id, generation: leaseGeneration }] });
      answer = results?.[0]?.result;
    } catch (error) {
      // Lease authority failures are not navigation. Preserve their exact,
      // fail-closed diagnosis instead of entering reload recovery.
      if (error?.code === 'browser_lease_lost' || error?.code === 'browser_bridge_unavailable') throw error;
      failureCode = 'browser_navigation_interrupted';
      progressSequence += 1;
      await reportProgress(cfg, work.job.id, { sequence: progressSequence, text: '', phase: 'recovering', detail: 'The tab navigated; reattaching to the conversation', busy: true });
      await waitForTabReady(tabId, effectiveProfile.selectors, 30000);
      failureCode = 'browser_recovery_unsafe';
      await assertRecoveryTab(workSessionKey(work), tabId);
      await requireSafeReloadState(tabId, effectiveProfile, { prompt: work.job.prompt }, false);
      failureCode = 'browser_navigation_interrupted';
      const resumeJob = { ...work.job, metadata: { ...(work.job.metadata || {}), contextbridge_resume_only: true, contextbridge_baseline_text: baselineText } };
      const recoveryTab = await assertRecoveryTab(workSessionKey(work), tabId);
      const resumed = await api.scripting.executeScript({ target: { tabId }, func: automate,
        args: [resumeJob, effectiveProfile, work.deadline, null, recoveryTab.url, { jobId: work.job.id, generation: leaseGeneration }] });
      answer = resumed?.[0]?.result;
    }
    // The periodic progress observer is supporting evidence only. Stop it as
    // soon as the authoritative provider automation returns so a late sample
    // cannot race final ownership/lease validation.
    if (progressTimer) {
      clearInterval(progressTimer);
      progressTimer = 0;
    }
    for (let attempt = 0; progressBusy && attempt < 200; attempt += 1) await delay(25);
    if (shouldForegroundStalledTab(binding, effectiveProfile, answer)) {
      // An automatically created background tab can be throttled by the
      // browser. Foreground it only after proving this is our submitted turn,
      // then observe without sending again. Keep the tab visible if it wakes.
      try {
        await assertRecoveryTab(workSessionKey(work), tabId);
        const proof = await api.scripting.executeScript({ target: { tabId }, func: inspectLatestOwnedTurn,
          args: [work.job.prompt, effectiveProfile.name] });
        const remaining = (Date.parse(work.deadline || '') || Date.now() + 45000) - Date.now() - 5000;
        if (proof?.[0]?.result && remaining >= 10000 && !(await api.tabs.get(tabId)).active) {
          progressSequence += 1;
          await reportProgress(cfg, work.job.id, { sequence: progressSequence, text: '', phase: 'recovering',
            detail: 'Foregrounding a stalled, ContextBridge-created tab without resending', busy: true });
	          await requireActiveBrowserLease(cfg, leaseState);
	          await api.tabs.update(tabId, { active: true });
	          const resumeJob = { ...work.job, metadata: { ...(work.job.metadata || {}), contextbridge_resume_only: true, contextbridge_baseline_text: baselineText } };
	          const observationTab = await assertRecoveryTab(workSessionKey(work), tabId);
          const observed = await api.scripting.executeScript({ target: { tabId }, func: automate,
            args: [resumeJob, effectiveProfile, new Date(Date.now() + Math.min(35000, remaining)).toISOString(), null,
              observationTab.url, { jobId: work.job.id, generation: leaseGeneration }] });
          if (observed?.[0]?.result?.ok) answer = observed[0].result;
        }
      } catch (_) { /* Existing ownership-checked recovery still applies. */ }
    }
    if (!answer?.ok && answer?.recoverable) {
      failureCode = 'browser_recovery_unsafe';
      await assertRecoveryTab(workSessionKey(work), tabId);
      const proofResult = await api.scripting.executeScript({ target: { tabId }, func: inspectLatestOwnedTurn,
        args: [work.job.prompt, effectiveProfile.name] });
      const recoveryTurn = proofResult?.[0]?.result;
      if (!recoveryTurn) throw new Error('The submitted ContextBridge turn could not be verified; no reload was performed');
      await rememberSessionURL(workSessionKey(work), tabId);
      await rememberOwnedTurn(workSessionKey(work), tabId, recoveryTurn);
      const expectedTurn = { ownedTurn: recoveryTurn };
      const firstSafeState = await requireSafeReloadState(tabId, effectiveProfile, expectedTurn, false);
      failureCode = 'browser_recovery_failed';
      progressSequence += 1;
      await reportProgress(cfg, work.job.id, { sequence: progressSequence, text: '', phase: 'recovering', detail: answer.error || 'Recovering browser tab', percent: Number(answer.percent) || 0, busy: true });
      await delay(2500);
      failureCode = 'browser_recovery_unsafe';
      await assertRecoveryTab(workSessionKey(work), tabId);
      const secondSafeState = await requireSafeReloadState(tabId, effectiveProfile, expectedTurn, false);
      if (firstSafeState.fingerprint !== secondSafeState.fingerprint) {
        throw new Error('Automatic reload skipped: the response changed during verification; the tab was left untouched');
      }
      failureCode = 'browser_recovery_failed';
      await requireActiveBrowserLease(cfg, leaseState);
      await api.tabs.reload(tabId);
      await waitForTabReady(tabId, effectiveProfile.selectors, 30000);
      await assertRecoveryTab(workSessionKey(work), tabId);
      failureCode = 'browser_recovery_unsafe';
      await requireSafeReloadState(tabId, effectiveProfile, expectedTurn, false);
      failureCode = 'browser_recovery_failed';
      const resumeJob = { ...work.job, metadata: { ...(work.job.metadata || {}), contextbridge_resume_only: true, contextbridge_baseline_text: baselineText } };
      const recoveryTab = await assertRecoveryTab(workSessionKey(work), tabId);
      const resumed = await api.scripting.executeScript({ target: { tabId }, func: automate,
        args: [resumeJob, effectiveProfile, work.deadline, null, recoveryTab.url, { jobId: work.job.id, generation: leaseGeneration }] });
      answer = resumed?.[0]?.result;
    }
    if (!answer?.ok) {
      failureCode = /^browser_[a-z_]+$/.test(String(answer?.code || '')) ? answer.code : failureCode;
      failureReason = classifyFailureReason(answer?.error);
      if (failureCode === 'browser_rate_limited') {
		await coolDownTab(tabId, 5 * 60 * 1000, effectiveProfile.name);
        progressSequence += 1;
        await reportProgress(cfg, work.job.id, { sequence: progressSequence, text: '', phase: 'rate_limited', detail: answer?.error || 'Provider rate limit', busy: false });
      }
      throw new Error(answer?.error || 'No browser response was captured');
    }
    failureCode = 'browser_lease_lost';
    await requireActiveBrowserLease(cfg, leaseState);
    failureCode = 'browser_session_changed';
    tab = await assertActiveLeaseConversation(leaseState);
    await assertSessionTab(leaseState.sessionKey, tabId);
    try {
      const owned = await api.scripting.executeScript({ target: { tabId }, func: inspectLatestOwnedTurn,
        args: [work.job.prompt, effectiveProfile.name, true, effectiveProfile.selectors] });
      submittedTurn = owned?.[0]?.result || null;
    } catch (_) { /* Some text-only providers lack a stable turn identifier. */ }
    if (['chatgpt', 'gemini'].includes(effectiveProfile.name) && !submittedTurn
        && !(effectiveProfile.name === 'gemini' && work.job.image_base64 && answer.submitted_prompt_verified === true)) {
      failureCode = 'browser_submit_unavailable';
      failureReason = 'submitted_prompt_unverified';
      throw new Error(`${effectiveProfile.name} did not expose the exact ContextBridge user turn followed by this answer; the answer was not accepted`);
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
      tab = await assertActiveLeaseConversation(leaseState);
      await assertSessionTab(leaseState.sessionKey, tabId);
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
        text: boundedUTF8(answer.text, outputByteLimit(work.job.output || {})).text,
        phase: 'final',
        busy: false
      });
    }
    decision = parseOutput(answer.text, work.job.output || {}, answer.selected_model || work.job.model || effectiveProfile.label || 'browser', answer.artifacts || []);
    // These are the selections confirmed by the tab automation, not merely
    // the model/reasoning requested by the remote job.
	if (answer.selected_model) decision.selected_model = String(answer.selected_model).slice(0, 100);
	if (answer.selected_reasoning) decision.selected_reasoning = String(answer.selected_reasoning).slice(0, 100);
    tab = await assertActiveLeaseConversation(leaseState);
    await assertSessionTab(leaseState.sessionKey, tabId);
    if (!editTarget) await releaseOwnedDraftIfEmpty(tabId, effectiveProfile);
    jobCompleted = true;
  } catch (error) {
    if (/^browser_[a-z_]+$/.test(String(error?.code || ''))) failureCode = error.code;
    const mode = outputMode(work.job.output || {});
    decision = mode === 'decision'
      ? { verdict: 'review', flags: [failureCode], confidence: 0.4, model: effectiveProfile?.label || 'browser' }
      : { mode, error: failureCode, model: effectiveProfile?.label || 'browser' };
    const tabFailures = { ...(await api.storage.local.get({ tabFailures: {} })).tabFailures };
    if (tabId) tabFailures[tabId] = { code: failureCode,
      reason: failureReason === 'other' ? classifyFailureReason(error.message) : failureReason,
      lease_reason: String(leaseState?.cancelReason || '').slice(0, 60), at: new Date().toISOString() };
    // A failed website job is visible on its tab and in its job result; it is
    // not a failure of the local browser-bridge connection itself.
    await api.storage.local.set({ tabFailures });
  } finally {
    if (leaseTimer) clearInterval(leaseTimer);
    if (progressTimer) clearInterval(progressTimer);
    if (jobCompleted && tabSlotHeld) {
      try { await rememberSessionURL(workSessionKey(work), tabId, leaseState?.expectedURL || ''); } catch (_) {}
      if (submittedTurn) {
        try { await rememberOwnedTurn(workSessionKey(work), tabId, submittedTurn); } catch (_) {}
      }
    }
    if (tabSlotHeld) busyTabs.delete(tabId);
    const activeLease = activeBrowserLeases.get(String(work?.job?.id || ''));
    if (activeLease?.generation === leaseGeneration) {
      if (activeLease.recoveryTimer) clearTimeout(activeLease.recoveryTimer);
      activeBrowserLeases.delete(String(work.job.id || ''));
    }
  }
	if (Number(leaseState?.tabId || 0) > 0 && decision) {
	  decision.contextbridge_browser_tab_id = Number(leaseState.tabId);
	  if (work?.job?.metadata?.contextbridge_new_chat_per_job === true) decision.contextbridge_ephemeral_browser_tab = true;
	}

  let acknowledged;
  try {
    acknowledged = await completeWork(await settings(), work.job.id, decision, leaseGeneration);
  } catch (error) {
    // Large artifacts can exceed Chromium's storage.local quota after Base64
    // expansion. Deliver them directly first; on a network failure retain only
    // a bounded completion. The durable sent-unknown claim lets an oversized
    // result be observed and collected again without resending the prompt.
    await rememberPendingCompletion(work.job.id, decision, leaseGeneration);
    throw error;
  }
  if (acknowledged && jobCompleted && !decision?.error && decision?.verdict !== 'review') {
    try { await markFinishedTabForClose(work, tabId, decision); } catch (_) { /* Completion is authoritative; tab cleanup is optional. */ }
  }
  if (acknowledged) await forgetBrowserJobClaim(work.job.id);
  await sendHeartbeat('waiting');
}

function shouldForegroundStalledTab(binding, profile, answer) {
  return binding?.autoCreated === true && profile?.name === 'chatgpt' && !answer?.ok
    && answer?.recoverable === true && ['stalled_response', 'missing_response_after_generation'].includes(answer.code);
}

function classifyFailureReason(message) {
  const text = String(message || '');
  if (/local ContextBridge service could not verify/i.test(text)) return 'local_bridge_unavailable';
  if (/ChatGPT still shows Stop/i.test(text)) return 'provider_busy';
  if (/model selector is not visible/i.test(text)) return 'model_selector_missing';
  if (/Requested model.+is not available in this chat \(0 candidates, 0 model choices, pill trigger/i.test(text)) return 'model_candidates_empty_pill';
  if (/Requested model.+is not available in this chat \(0 candidates, 0 model choices, form trigger/i.test(text)) return 'model_candidates_empty_form';
  if (/Requested model.+is not available in this chat \(0 candidates/i.test(text)) return 'model_candidates_empty';
  if (/Requested model.+is not available in this chat \(\d+ candidates, 0 model choices/i.test(text)) return 'model_choices_empty';
  if (/Requested model.+is disabled in this chat/i.test(text)) return 'model_choice_disabled';
  if (/Requested model.+is not available in this chat/i.test(text)) return 'model_choice_missing';
  if (/requested model.+not retained/i.test(text)) return 'model_not_retained';
  if (/submitted ContextBridge turn could not be verified/i.test(text)) return 'recovery_turn_unverified';
  if (/Automatic reload skipped: the latest user message/i.test(text)) return 'recovery_turn_mismatch';
  if (/Automatic reload skipped: an unsent draft/i.test(text)) return 'recovery_draft';
  if (/Automatic reload skipped: an unsent attachment/i.test(text)) return 'recovery_attachment';
  if (/Automatic reload skipped: the previous answer has no visible completion controls/i.test(text)) return 'recovery_answer_unfinished';
  if (/Automatic reload skipped: the prompt editor is unavailable/i.test(text)) return 'recovery_input_missing';
  if (/Automatic reload skipped: a message editor or dialog is open/i.test(text)) return 'recovery_editor_open';
  if (/Automatic reload skipped: image generation is still visible/i.test(text)) return 'recovery_image_busy';
  if (/Automatic reload skipped: the response changed during verification/i.test(text)) return 'recovery_response_changed';
  if (/prompt editor did not retain|prompt editor changed|gemini editor did not accept/i.test(text)) return 'prompt_not_retained';
  if (/send button stayed disabled/i.test(text)) return 'send_disabled';
  if (/send button is not visible/i.test(text)) return 'send_missing';
  if (/compatible (?:image upload|file) input/i.test(text)) return 'upload_input_missing';
  if (/did not show the uploaded image/i.test(text)) return 'upload_preview_missing';
  if (/did not show the image-job prompt|did not expose a verifiable user turn containing the image-job prompt/i.test(text)) return 'submitted_prompt_unverified';
  if (/unsent attachment is already present/i.test(text)) return 'attachment_busy';
  if (/prompt editor contains another draft/i.test(text)) return 'composer_draft';
  if (/incompatible selected tool/i.test(text)) return 'incompatible_tool';
  return 'other';
}

function supportsTabEditJob(job) {
  return !job?.image_base64 && !job?.metadata?.contextbridge_input_file && !job?.metadata?.contextbridge_image_tool && !job?.metadata?.contextbridge_music_tool
    && !job?.output?.artifacts && Number(job?.output?.min_artifacts || 0) === 0
    && Number(job?.output?.min_images || 0) === 0 && Number(job?.output?.min_media || 0) === 0;
}

async function coolDownTab(tabId, duration, providerName = '') {
  const cfg = await settings();
  const until = Date.now() + duration;
  const tabCooldowns = { ...(cfg.tabCooldowns || {}), [tabId]: Math.max(Number(cfg.tabCooldowns?.[tabId] || 0), until) };
  // ChatGPT's conversation-access limit applies to the signed-in browser
  // account, not just the tab that happened to display the dialog. Avoid
  // immediately claiming the next job in another attached ChatGPT tab.
  const host = providerName === 'chatgpt' ? 'chatgpt.com' : '';
  if (host) {
    for (const candidate of configuredTabIDs(cfg)) {
      try {
        const tab = await api.tabs.get(candidate);
        if (new URL(tab.url).hostname === host) tabCooldowns[candidate] = Math.max(Number(tabCooldowns[candidate] || 0), until);
      } catch (_) { /* A closed or inaccessible tab cannot be cooled. */ }
    }
  }
  await api.storage.local.set({ tabCooldowns });
}

function workSessionKey(work) {
  const session = String(work?.job?.contextbridge_session_key || work?.job?.session_id || 'local-default').trim().slice(0, 200);
  const profile = String(work?.profile?.name || work?.job?.browser_profile || '').trim().slice(0, 50);
  const job = work?.job?.metadata?.contextbridge_new_chat_per_job === true ? String(work?.job?.id || '').slice(0, 100) : '';
  // One terminal session can explicitly switch providers, but ChatGPT and
  // Gemini must never be treated as the same browser conversation.
  return job ? JSON.stringify([session, profile, job]) : JSON.stringify([session, profile]);
}

function serializeSessionWrite(operation) {
  const result = sessionWrite.catch(() => {}).then(operation);
  sessionWrite = result.then(() => {}, () => {});
  return result;
}

async function sessionBindingState() {
  const cfg = await settings();
  const bindings = { ...cfg.sessionBindings };
  if (!cfg.sessionBindingsMigrated) {
    // Old releases could put multiple sessions into one conversation. Their
    // history cannot be separated retroactively, so quarantine those tabs.
    for (const tabId of Object.values(cfg.sessionTabs || {}).map(Number)) {
      if (!tabId || Object.values(bindings).some((entry) => Number(entry?.tabId) === tabId)) continue;
      bindings[`legacy-tab:${tabId}`] = { tabId, url: '', legacy: true };
    }
    await api.storage.local.set({ sessionBindings: bindings, sessionBindingsMigrated: true, sessionTabs: {} });
  }
  return { cfg, bindings };
}

async function resolveWorkTab(_cfg, work, claimedTabId) {
  return serializeSessionWrite(async () => {
    const { cfg, bindings } = await sessionBindingState();
    const key = workSessionKey(work);
    const requested = String(work?.profile?.name || '');
	const requiredTabId = Number(work?.job?.contextbridge_browser_tab_id || 0);
	if (requiredTabId > 0 && (!Number.isSafeInteger(requiredTabId) || requiredTabId !== claimedTabId)) {
	  throw new Error('The browser lease was claimed by a tab other than the relay-selected tab');
	}
    const compatible = async (tabId) => {
      try {
        const tab = await api.tabs.get(tabId);
        return tab && (!requested || profileForTab(cfg, tab)?.name === requested) ? tab : null;
      } catch (_) { return null; }
    };
    if (bindings[key]) {
      const binding = bindings[key];
      const mapped = Number(binding.tabId);
      if (configuredTabIDs(cfg).includes(mapped)) {
        const tab = await compatible(mapped);
		if (tab && (requiredTabId <= 0 || mapped === requiredTabId) && (!binding.url || tab.url === binding.url)) return mapped;
        // A fresh chat may gain its permanent conversation URL only after the
        // first job has completed. Keep that exact tab bound, but only after
        // the latest user turn still matches ContextBridge's stored proof.
        if (tab && binding.ownedTurn?.id && binding.url && isFreshChatURL(binding.url)
            && tab.url && !isFreshChatURL(tab.url)) {
          try {
            const before = new URL(binding.url);
            const after = new URL(tab.url);
            const profile = profileForTab(cfg, tab);
            if (before.origin === after.origin && profile?.selectors && profile.name === binding.ownedTurn.provider) {
              const proof = await api.scripting.executeScript({ target: { tabId: mapped }, func: inspectRecoveryState,
                args: [profile.selectors, profile.name, { ownedTurn: binding.ownedTurn }] });
              if (proof?.[0]?.result?.owned_turn_matches) {
                bindings[key] = { ...binding, url: tab.url };
                await api.storage.local.set({ sessionBindings: bindings });
                return mapped;
              }
            }
          } catch (_) { /* Never reclaim an uncertain conversation. */ }
        }
        // A lease can expire after the first send changed a fresh-chat URL but
        // before the original worker stored its turn proof. A sent-unknown
        // claimant may recover that transition only by proving the exact prompt
        // is now the latest user turn; it remains observation-only afterward.
        if (tab && work?.observation_only === true && binding.url && isFreshChatURL(binding.url)
            && tab.url && !isFreshChatURL(tab.url)) {
          try {
            const before = new URL(binding.url);
            const after = new URL(tab.url);
            const profile = profileForTab(cfg, tab);
            if (before.origin === after.origin && profile?.name) {
              const proof = await api.scripting.executeScript({ target: { tabId: mapped }, func: inspectLatestOwnedTurn,
                args: [work.job.prompt, profile.name] });
              if (proof?.[0]?.result) {
                bindings[key] = { ...binding, url: tab.url, ownedTurn: proof[0].result };
                await api.storage.local.set({ sessionBindings: bindings });
                return mapped;
              }
            }
          } catch (_) { /* Unknown delivery must never become permission to resend. */ }
        }
      }
      // The user may manually switch back to a known conversation, including
      // after closing its original tab. Match the exact saved URL, never title
      // or visible text, and park the previous occupant before reassigning.
      if (binding.url && !isFreshChatURL(binding.url)) {
        for (const id of configuredTabIDs(cfg)) {
		  if (requiredTabId > 0 && id !== requiredTabId) continue;
          if (busyTabs.has(id)) continue;
          const tab = await compatible(id);
          if (!tab || tab.url !== binding.url) continue;
          const occupants = Object.entries(bindings).filter(([otherKey, entry]) => otherKey !== key && Number(entry?.tabId) === id);
          if (occupants.some(([, entry]) => entry.legacy || entry.url === tab.url || isFreshChatURL(entry.url))) continue;
          for (const [otherKey, entry] of occupants) bindings[otherKey] = { ...entry, tabId: 0 };
          bindings[key] = { ...binding, tabId: id };
          await api.storage.local.set({ sessionBindings: bindings });
          return id;
        }
      }
	  if (requiredTabId > 0 && mapped !== requiredTabId) {
		throw new Error('The relay-selected tab does not own this ContextBridge session or its saved conversation URL');
	  }
      throw new Error('The session tab moved or closed. Open its original chat in an attached tab before sending another turn');
    }
    const occupied = new Set(Object.values(bindings).map((entry) => Number(entry?.tabId)));
    for (const [oldKey, entry] of Object.entries(bindings)) {
      if (!entry.legacy || !configuredTabIDs(cfg).includes(Number(entry.tabId))) continue;
      const oldTab = await compatible(Number(entry.tabId));
      if (oldTab && isFreshChatURL(oldTab.url) && await checkFreshTab(oldTab.id)) {
        occupied.delete(oldTab.id);
        delete bindings[oldKey];
      }
    }
	const order = requiredTabId > 0 ? [requiredTabId] : [claimedTabId, ...configuredTabIDs(cfg).filter((id) => id !== claimedTabId)];
    const requestedMode = work?.job?.metadata?.contextbridge_new_chat === true ? 'new_chat' : cfg.sessionMode;
    let tab = null;
    if (requestedMode !== 'new_chat') {
      for (const id of order) {
        if (occupied.has(id)) continue;
        tab = await compatible(id);
        if (!tab) continue;
        // A parked session still owns its exact conversation URL.
        if (Object.values(bindings).some((entry) => entry.url && !isFreshChatURL(entry.url) && entry.url === tab.url)) {
          tab = null;
          continue;
        }
        break;
      }
    }
    let autoCreated = false;
    if (!tab && requestedMode === 'new_chat') { tab = await createFreshSessionTab(cfg, work, claimedTabId); autoCreated = true; }
    if (!tab) throw new Error('No unassigned AI tab is available for this session. Open a fresh chat and attach it, or enable New chat per session');
    if (isFreshChatURL(tab.url) && !await checkFreshTab(tab.id)) {
      throw new Error('The new AI chat is not empty; no prompt was sent');
    }
    bindings[key] = { tabId: tab.id, url: tab.url || '', label: String(work?.job?.session_id || 'default').slice(0, 80),
      autoCreated, perJob: work?.job?.metadata?.contextbridge_new_chat_per_job === true };
    await api.storage.local.set({ sessionBindings: bindings });
    return tab.id;
  });
}

async function createFreshSessionTab(cfg, work, claimedTabId) {
  const profile = String(work?.profile?.name || profileForTab(cfg, await api.tabs.get(claimedTabId))?.name || '');
  const url = freshChatURLForProfile(profile);
  if (!url) throw new Error('Automatic new chats are supported only for ChatGPT and Gemini');
  if (configuredTabIDs(cfg).length >= 16) throw new Error('The 16-tab safety limit is reached; close or detach a session tab first');
  if (!await api.permissions.contains({ origins: [new URL(url).origin + '/*'] })) throw new Error('Page access for the new AI chat is not granted');
  const created = await api.tabs.create({ url, active: foregroundFreshChatForJob(work?.job) });
  await waitForTabReady(created.id, work.profile?.selectors || {}, 30000);
  if (!await checkFreshTab(created.id)) throw new Error('The new AI chat was not confirmed empty; no prompt was sent');
  const tabIds = [...configuredTabIDs(cfg), created.id];
  await api.storage.local.set({ tabId: tabIds[0], tabIds });
  void poll();
  void sendHeartbeat('waiting');
  return api.tabs.get(created.id);
}

function freshChatURLForProfile(profile) {
  return profile === 'chatgpt' ? 'https://chatgpt.com/' : profile === 'gemini' ? 'https://gemini.google.com/app' : '';
}

function foregroundFreshChatForJob(job) {
  // Inactive Opera tabs can suspend Gemini's image-upload callback indefinitely.
  // Bring only a newly created upload chat to the foreground; ordinary text
  // jobs remain in the background and manually attached tabs are untouched.
  return job?.metadata?.contextbridge_foreground_new_chat === true || Boolean(job?.image_base64);
}

async function rememberSessionURL(key, tabId, expectedURL = '') {
  await serializeSessionWrite(async () => {
    const { bindings } = await sessionBindingState();
    const binding = bindings[key];
    if (!binding || Number(binding.tabId) !== tabId || !isFreshChatURL(binding.url)) return;
    const tab = await api.tabs.get(tabId);
    if (expectedURL && tab?.url !== expectedURL) return;
    if (!tab?.url || isFreshChatURL(tab.url)) return;
    const before = new URL(binding.url);
    const after = new URL(tab.url);
    if (before.origin !== after.origin) return;
    bindings[key] = { ...binding, url: tab.url };
    await api.storage.local.set({ sessionBindings: bindings });
  });
}

async function rememberOwnedTurn(key, tabId, ownedTurn) {
  if (!ownedTurn?.id || !ownedTurn?.digest || !['chatgpt', 'gemini'].includes(ownedTurn.provider)) return;
  await serializeSessionWrite(async () => {
    const { bindings } = await sessionBindingState();
    const binding = bindings[key];
    const tab = await api.tabs.get(tabId);
    if (!binding || Number(binding.tabId) !== tabId || binding.url !== tab?.url) return;
    bindings[key] = { ...binding, ownedTurn: { id: ownedTurn.id, digest: ownedTurn.digest, provider: ownedTurn.provider } };
    await api.storage.local.set({ sessionBindings: bindings });
  });
}

async function inspectLatestOwnedTurn(expectedPrompt, provider, requireResponseAfter = false, selectors = null, expectedProof = null) {
  const proofNonce = String(expectedProof?.nonce || '');
  const proofDigest = String(expectedProof?.digest || '');
  const hasProof = /^[a-f0-9]{32}$/.test(proofNonce) && /^[a-f0-9]{64}$/.test(proofDigest);
  if (!['chatgpt', 'gemini'].includes(provider) || (!expectedPrompt && !hasProof)) return null;
  const turns = document.querySelectorAll(provider === 'chatgpt' ? 'section[data-turn="user"]' : 'user-query');
  const turn = turns.length ? (turns.item ? turns.item(turns.length - 1) : turns[turns.length - 1]) : null;
  const content = provider === 'chatgpt'
    ? turn?.querySelector('[data-message-author-role="user"]')
    : turn?.querySelector('[id^="user-query-content-"]');
  const id = provider === 'chatgpt' ? turn?.getAttribute('data-turn-id') : content?.id;
  const normalize = (value) => String(value || '').normalize('NFKC').replace(/\s+/g, ' ').trim();
  const expected = normalize(expectedPrompt);
  const digestText = async (value) => {
    const input = hasProof ? `${proofNonce}\u0000${normalize(value)}` : normalize(value);
    const digest = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(input));
    return [...new Uint8Array(digest)].map((part) => part.toString(16).padStart(2, '0')).join('');
  };
  const matchesExpected = async (value) => expected ? normalize(value) === expected : await digestText(value) === proofDigest;
  let promptContent = content;
  if (provider === 'chatgpt' && content?.querySelectorAll) {
    const parts = content.querySelectorAll('[data-message-content-part], .whitespace-pre-wrap, .markdown');
    const attachmentSelector = '[data-testid*="attachment" i], [data-test-id*="attachment" i], [data-file-citation-primary-file-id], [role="group"][aria-label]';
    for (let index = 0; index < Math.min(parts.length, 32); index += 1) {
      const part = parts.item ? parts.item(index) : parts[index];
      if (part?.closest?.(attachmentSelector) || part?.querySelector?.('img, video, audio, [data-file-citation-primary-file-id]')) continue;
      if (await matchesExpected(part?.textContent)) { promptContent = part; break; }
    }
  }
  const text = normalize(promptContent?.textContent);
  if (!id || !text || !await matchesExpected(text)) return null;
  if (requireResponseAfter) {
    let responseAfter = false;
    for (const selector of selectors?.response || []) {
      let responses;
      try { responses = document.querySelectorAll(selector); } catch (_) { responses = null; }
      if (!responses?.length) continue;
      for (let index = responses.length - 1; index >= Math.max(0, responses.length - 64); index -= 1) {
        const response = responses.item ? responses.item(index) : responses[index];
        if (typeof turn.compareDocumentPosition === 'function' && Boolean(turn.compareDocumentPosition(response) & 4)) {
          responseAfter = true;
          break;
        }
      }
      break;
    }
    if (!responseAfter) return null;
  }
  const digest = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(text));
  return { id, digest: [...new Uint8Array(digest)].map((value) => value.toString(16).padStart(2, '0')).join(''), provider };
}

// This probe returns only ownership and UI-state evidence. Chat content and
// draft values stay inside the provider tab, including during stall sampling.
async function inspectRecoveryState(selectors, provider, expected) {
  const visible = (element) => Boolean(element && (element.offsetWidth || element.offsetHeight || element.getClientRects().length));
  const firstVisible = (items, limit = 64) => {
    for (const selector of items || []) {
      let nodes;
      try { nodes = document.querySelectorAll(selector); } catch (_) { continue; }
      for (let index = nodes.length - 1; index >= Math.max(0, nodes.length - limit); index -= 1) {
        const node = nodes.item ? nodes.item(index) : nodes[index];
        if (visible(node)) return node;
      }
    }
    return null;
  };
  const normalize = (value) => String(value || '').normalize('NFKC').replace(/\s+/g, ' ').trim();
  const digest = async (value) => {
    const bytes = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(value));
    return [...new Uint8Array(bytes)].map((part) => part.toString(16).padStart(2, '0')).join('');
  };
  const stop = firstVisible(provider === 'chatgpt'
    ? ['button[data-testid="stop-button"]', 'button[aria-label="Antwort stoppen"]', 'button[aria-label="Stop generating"]']
    : ['button[data-testid*="stop" i]', 'button[aria-label*="stop" i]',
      'button[aria-label*="beenden" i]', 'button[aria-label*="abbrechen" i]']);
  const input = firstVisible(selectors?.input);
  const scope = input?.closest?.('form, [data-node-type="input-area"]');
  const draft = String(typeof input?.value === 'string' ? input.value : (input?.innerText || input?.textContent || ''));
  let selectedFile = false;
  const fileInputs = scope?.querySelectorAll?.('input[type="file"]') || [];
  for (let index = 0; index < Math.min(fileInputs.length, 16); index += 1) {
    const field = fileInputs.item ? fileInputs.item(index) : fileInputs[index];
    if (field?.files?.length) { selectedFile = true; break; }
  }
  const hasAttachments = Boolean(scope && (selectedFile
    || scope.querySelector('[data-testid*="attachment-chip" i], [data-testid*="attached-file" i], [data-testid*="file-thumbnail" i], [data-test-id*="attachment" i]')));
  const editOpen = Boolean(firstVisible(['section[data-turn="user"] textarea, user-query textarea, [role="dialog"] textarea, [role="dialog"] [contenteditable="true"]']));
  const turns = document.querySelectorAll(provider === 'chatgpt' ? 'section[data-turn="user"]' : 'user-query');
  const turn = turns.length ? (turns.item ? turns.item(turns.length - 1) : turns[turns.length - 1]) : null;
  const content = provider === 'chatgpt'
    ? turn?.querySelector?.('[data-message-author-role="user"]')
    : turn?.querySelector?.('[id^="user-query-content-"]');
  const turnID = provider === 'chatgpt' ? turn?.getAttribute?.('data-turn-id') : content?.id;
  const expectedPrompt = normalize(expected?.prompt);
  const expectedDigest = expected?.ownedTurn?.provider === provider ? String(expected.ownedTurn.digest || '') : '';
  let turnText = '';
  let ownedTurnMatches = false;
  const considerPromptText = async (value) => {
    const text = normalize(value);
    if (!text) return false;
    const matchesOwned = Boolean(expectedDigest && turnID === expected?.ownedTurn?.id && await digest(text) === expectedDigest);
    const matchesPrompt = Boolean(!expectedDigest && expectedPrompt && text === expectedPrompt);
    if (!matchesOwned && !matchesPrompt) return false;
    turnText = text;
    ownedTurnMatches = true;
    return true;
  };
  if (turnID && provider === 'chatgpt' && content?.querySelectorAll) {
    const parts = content.querySelectorAll('[data-message-content-part], .whitespace-pre-wrap, .markdown');
    const attachmentSelector = '[data-testid*="attachment" i], [data-test-id*="attachment" i], [data-file-citation-primary-file-id], [role="group"][aria-label]';
    for (let index = 0; index < Math.min(parts.length, 32); index += 1) {
      const part = parts.item ? parts.item(index) : parts[index];
      if (part?.closest?.(attachmentSelector) || part?.querySelector?.('img, video, audio, [data-file-citation-primary-file-id]')) continue;
      if (await considerPromptText(part?.textContent)) break;
    }
  }
  if (!ownedTurnMatches && turnID) await considerPromptText(content?.textContent);
  let response = null;
  let responseCount = 0;
  for (const selector of selectors?.response || []) {
    let responses;
    try { responses = document.querySelectorAll(selector); } catch (_) { responses = null; }
    if (!responses?.length) continue;
    responseCount = responses.length;
    for (let index = responses.length - 1; index >= Math.max(0, responses.length - 64); index -= 1) {
      const candidate = responses.item ? responses.item(index) : responses[index];
      if (visible(candidate)) { response = candidate; break; }
    }
    break;
  }
  const markdown = [];
  if (response?.querySelectorAll) {
    const nodes = response.querySelectorAll('message-content .markdown, [data-message-author-role="assistant"] .markdown');
    for (let index = Math.max(0, nodes.length - 64); index < nodes.length; index += 1) {
      markdown.push(nodes.item ? nodes.item(index) : nodes[index]);
    }
  }
  const responseText = normalize(markdown.length
    ? markdown.map((part) => String(part.textContent || '').slice(0, 64 * 1024)).join('\n').slice(0, 1024 * 1024)
    : String(response?.textContent || '').slice(0, 1024 * 1024));
  const responseAfterTurn = Boolean(turn && response && typeof turn.compareDocumentPosition === 'function'
    && (turn.compareDocumentPosition(response) & 4));
  let responseActionsReady = false;
  if (response?.querySelectorAll) {
    const buttons = response.querySelectorAll('button');
    for (let index = buttons.length - 1; index >= Math.max(0, buttons.length - 128); index -= 1) {
      const button = buttons.item ? buttons.item(index) : buttons[index];
      if (visible(button) && /copy|kopieren/i.test(`${button.getAttribute('data-testid') || ''} ${button.getAttribute('aria-label') || ''}`)) {
        responseActionsReady = true;
        break;
      }
    }
  }
  const imageLoading = Boolean(firstVisible(['[data-testid="image-gen-loading-state"], [data-testid="image-gen-loading-state-frame"], [data-testid="image-gen-loading-progress"]']));
  const fingerprint = await digest(`${responseCount}|${response?.getAttribute?.('data-turn-id') || response?.id || ''}|${responseText}`);
  return {
    stop_visible: Boolean(stop),
    stop_disabled: Boolean(stop && (stop.disabled || stop.getAttribute?.('aria-disabled') === 'true')),
    stop_spinner: Boolean(stop?.querySelector?.('[class*="spin" i], [role="progressbar"], svg[aria-label*="loading" i]')),
    input_ready: Boolean(input),
    draft_empty: !draft.trim(),
    attachments_empty: !hasAttachments,
    edit_closed: !editOpen,
    owned_turn_matches: ownedTurnMatches,
    response_ready: Boolean(responseText && responseAfterTurn && responseActionsReady),
    image_idle: !imageLoading,
    fingerprint
  };
}

function unsafeReloadReason(state, requireFinishedAnswer) {
  if (!state?.input_ready) return 'the prompt editor is unavailable';
  if (!state.owned_turn_matches) return 'the latest user message is not the expected ContextBridge turn';
  if (!state.draft_empty) return 'an unsent draft is present';
  if (!state.attachments_empty) return 'an unsent attachment is present';
  if (!state.edit_closed) return 'a message editor or dialog is open';
  if (!state.image_idle) return 'image generation is still visible';
  if (requireFinishedAnswer && !state.response_ready) return 'the previous answer has no visible completion controls';
  return '';
}

async function requireSafeReloadState(tabId, profile, expected, requireFinishedAnswer) {
  const result = await api.scripting.executeScript({ target: { tabId }, func: inspectRecoveryState,
    args: [profile.selectors, profile.name, expected] });
  const state = result?.[0]?.result;
  const reason = unsafeReloadReason(state, requireFinishedAnswer);
  if (reason) throw new Error(`Automatic reload skipped: ${reason}; the tab was left untouched`);
  return state;
}

async function recoverPriorStall(work, tabId, profile, ownedTurn, cfg = null, lease = null) {
  const probe = async () => {
    const result = await api.scripting.executeScript({ target: { tabId }, func: inspectRecoveryState,
      args: [profile.selectors, profile.name, { ownedTurn }] });
    return result?.[0]?.result;
  };
  let state = await probe();
  if (!state?.stop_visible) return false;
  if (work.job.metadata?.contextbridge_auto_reload === false) {
    throw new Error('ChatGPT still shows Stop; automatic reload is disabled for this job');
  }
  const reason = unsafeReloadReason(state, true);
  if (reason || !state.stop_disabled) {
    throw new Error(`ChatGPT still shows Stop; automatic reload is unsafe (${reason || 'Stop is still clickable'})`);
  }
  let fingerprint = state.fingerprint;
  let stableSince = Date.now();
  const waitUntil = Math.min(Date.now() + 60000, (Date.parse(work.deadline || '') || Date.now() + 60000) - 45000);
  while (Date.now() < waitUntil) {
    await delay(2500);
    state = await probe();
    if (!state?.stop_visible) return false;
    const changedReason = unsafeReloadReason(state, true);
    if (changedReason || !state.stop_disabled) {
      throw new Error(`ChatGPT still shows Stop; automatic reload is unsafe (${changedReason || 'Stop became clickable'})`);
    }
    if (state.fingerprint !== fingerprint) {
      fingerprint = state.fingerprint;
      stableSince = Date.now();
    }
    if (Date.now() - stableSince < 30000) continue;
    await assertSessionTab(workSessionKey(work), tabId);
    const finalState = await requireSafeReloadState(tabId, profile, { ownedTurn }, true);
    if (!finalState.stop_visible || !finalState.stop_disabled || finalState.fingerprint !== fingerprint) continue;
    if (cfg && lease) await requireActiveBrowserLease(cfg, lease);
    await api.tabs.reload(tabId);
    await waitForTabReady(tabId, profile.selectors, 30000);
    await assertSessionTab(workSessionKey(work), tabId);
    const after = await probe();
    if (after?.stop_visible || unsafeReloadReason(after, false)) {
      throw new Error('ChatGPT remained busy or changed after the guarded reload; no prompt was sent');
    }
    return true;
  }
  throw new Error('ChatGPT still shows Stop; there was not enough stable idle evidence before the job deadline');
}

async function assertSessionTab(key, tabId) {
  const cfg = await settings();
  const binding = cfg.sessionBindings?.[key];
  const tab = await api.tabs.get(tabId);
  if (!binding || Number(binding.tabId) !== tabId || (binding.url && binding.url !== tab?.url)) {
    throw new Error('The session tab changed or was released while the job was waiting; no prompt was sent');
  }
  if (isFreshChatURL(binding.url) && !await checkFreshTab(tabId)) {
    throw new Error('The session chat was no longer empty before Send; no prompt was sent');
  }
  return { binding, tab };
}

async function assertRecoveryTab(key, tabId) {
  const cfg = await settings();
  const binding = cfg.sessionBindings?.[key];
  const tab = await api.tabs.get(tabId);
  if (!binding || Number(binding.tabId) !== tabId || !tab?.url
    || (binding.url !== tab.url && (!isFreshChatURL(binding.url)
      || new URL(binding.url).origin !== new URL(tab.url).origin))) {
    throw new Error('The session tab changed or was released during recovery; no reload was performed');
  }
  return tab;
}

async function releaseSessionTab(tabId) {
  if (!tabId || busyTabs.has(tabId)) throw new Error('Wait for this tab to finish its job first');
  const cfg = await settings();
  if (!configuredTabIDs(cfg).includes(tabId)) throw new Error('Attach this AI tab first');
  if (!await checkFreshTab(tabId)) throw new Error('Open a new, empty ChatGPT or Gemini chat in this tab first; no conversation was released');
  return serializeSessionWrite(async () => {
    if (busyTabs.has(tabId) || !isFreshChatURL((await api.tabs.get(tabId))?.url)) {
      throw new Error('The tab changed while it was being checked; no conversation was released');
    }
    const { bindings } = await sessionBindingState();
    for (const [key, binding] of Object.entries(bindings)) {
      if (Number(binding?.tabId) !== tabId) continue;
      if (binding.url && !isFreshChatURL(binding.url) && !binding.legacy) bindings[key] = { ...binding, tabId: 0 };
      else delete bindings[key];
    }
    await api.storage.local.set({ sessionBindings: bindings });
    return { ok: true };
  });
}

async function setTabEditMode(tabId, enabled) {
  if (!tabId || busyTabs.has(tabId)) throw new Error('Wait until this tab finishes its job');
  const cfg = await settings();
  if (!configuredTabIDs(cfg).includes(tabId)) throw new Error('Attach this AI tab first');
  const tab = await api.tabs.get(tabId);
  const profile = profileForTab(cfg, tab);
  if (!['chatgpt', 'gemini'].includes(profile?.name)) throw new Error('Prompt editing is supported only on recognized ChatGPT or Gemini pages');
  const tabEditModes = { ...cfg.tabEditModes };
  if (enabled) tabEditModes[tabId] = true;
  else delete tabEditModes[tabId];
  await api.storage.local.set({ tabEditModes });
  return { ok: true, enabled };
}

async function waitForTabSlot(tabId, deadlineValue) {
  const deadline = Math.min(Date.now() + 300000, Date.parse(deadlineValue || '') || Date.now() + 300000);
  while (busyTabs.has(tabId) && Date.now() < deadline) await delay(250);
  if (busyTabs.has(tabId)) throw new Error('The session tab stayed busy until the job deadline');
  busyTabs.add(tabId);
}

async function waitForTabReady(tabId, selectors, timeout, initialDelay = 600) {
  const deadline = Date.now() + timeout;
  let missingTabChecks = 0;
  if (initialDelay > 0) await delay(initialDelay);
  while (Date.now() < deadline) {
    try {
      const tab = await withDeadline(api.tabs.get(tabId), 2500);
      missingTabChecks = 0;
      if (tab.url && /^https?:/i.test(tab.url)) {
        try {
          const result = await withDeadline(api.scripting.executeScript({ target: { tabId }, func: inspectSelectors, args: [selectors] }), 2500);
          if (Number(result?.[0]?.result?.input || 0) > 0) return;
        } catch (_) { /* The new page may not be scriptable yet. */ }
      }
    } catch (_) {
      if (++missingTabChecks >= 3) throw new Error('The browser tab was closed during job recovery');
    }
    await delay(500);
  }
  throw new Error('The AI prompt did not become ready after tab navigation');
}

function isPendingLeaseConversation(lease, tab) {
  if (!lease?.sentUnknown || !lease.expectedURL || !lease.sessionKey || (!lease.prompt && !lease.promptProof)
      || !['chatgpt', 'gemini'].includes(lease.profileName) || !tab?.url) return false;
  try {
    const before = new URL(lease.expectedURL);
    const after = new URL(tab.url);
    return before.origin === after.origin && isFreshChatURL(before.href) && !isFreshChatURL(after.href);
  } catch (_) { return false; }
}

async function promoteActiveLeaseConversation(lease, tab) {
  if (!lease || lease.cancelled || stopRequested) return false;
  if (tab?.url === lease.expectedURL) return true;
  if (!isPendingLeaseConversation(lease, tab)) return false;
  // Progress sampling and the injected observer can see the first chat URL
  // change together. Share one proof/CAS instead of letting one caller cancel
  // a lease while the other is still committing the same verified transition.
  if (lease.conversationPromotion) return lease.conversationPromotion.targetURL === tab.url
    ? lease.conversationPromotion.promise : false;
  const sourceURL = lease.expectedURL;
  const targetURL = tab.url;
  const promotion = { targetURL, promise: null };
  lease.conversationPromotion = promotion;
  promotion.promise = commitActiveLeaseConversation(lease, sourceURL, targetURL);
  try { return await promotion.promise; }
  finally { if (lease.conversationPromotion === promotion) delete lease.conversationPromotion; }
}

async function commitActiveLeaseConversation(lease, sourceURL, targetURL) {
  const proof = await withDeadline(api.scripting.executeScript({ target: { tabId: lease.tabId },
    func: inspectLatestOwnedTurn, args: [lease.prompt || '', lease.profileName, false, null, lease.promptProof || null] }), 5000).catch(() => null);
  const ownedTurn = proof?.[0]?.result;
  if (!ownedTurn) return false;
  const verifiedTab = await withDeadline(api.tabs.get(lease.tabId), 2500).catch(() => null);
  if (lease.cancelled || stopRequested || verifiedTab?.url !== targetURL) return false;
  const updated = await serializeSessionWrite(() => serializeBrowserClaimWrite(async () => {
    const { cfg, bindings } = await sessionBindingState();
    const binding = bindings[lease.sessionKey];
    const existing = cfg.browserJobClaims?.[lease.jobId];
    if (lease.cancelled || stopRequested || lease.expectedURL !== sourceURL
        || !binding || Number(binding.tabId) !== lease.tabId || binding.url !== sourceURL
        || !existing || Number(existing.generation) !== lease.generation || Number(existing.tabId) !== lease.tabId
        || existing.sessionKey !== lease.sessionKey || existing.expectedURL !== sourceURL) return false;
    bindings[lease.sessionKey] = { ...binding, url: targetURL, ownedTurn };
    const browserJobClaims = { ...cfg.browserJobClaims,
      [lease.jobId]: { ...existing, expectedURL: targetURL, ownedTurn, at: Date.now() } };
    await api.storage.local.set({ sessionBindings: bindings, browserJobClaims });
    lease.expectedURL = targetURL;
    lease.ownedTurn = ownedTurn;
    return true;
  }));
  return updated;
}

async function assertActiveLeaseConversation(lease, cancelOnMismatch = true) {
  if (!lease || lease.cancelled || stopRequested) throw new Error('The browser job lease was cancelled');
  const tab = await readActiveLeaseTab(lease);
  if (lease.cancelled || stopRequested) throw new Error('The browser job lease was cancelled');
  if (tab?.url === lease.expectedURL) return tab;
  if (tab && await promoteActiveLeaseConversation(lease, tab)) {
    if (lease.cancelled || stopRequested) throw new Error('The browser job lease was cancelled');
    return tab;
  }
  // A provider can publish its conversation URL before the corresponding
  // user turn mounts. Skip this progress sample without poisoning the live
  // lease. No response content is read until the ownership proof succeeds.
  if (isPendingLeaseConversation(lease, tab)) {
    const error = new Error('The submitted ContextBridge turn could not be verified after the conversation URL changed; no provider content was observed');
    error.code = 'browser_session_changed';
    throw error;
  }
  if (cancelOnMismatch) {
    lease.cancelReason = 'conversation_changed';
    lease.cancelled = true;
  }
  throw new Error('The conversation URL changed; no provider content was observed');
}

async function readActiveLeaseTab(lease) {
  const expectedURL = lease.expectedURL;
  let tab = await withDeadline(api.tabs.get(lease.tabId), 2500).catch(() => null);
  // tabs.get can return its earlier fresh-page snapshot after another caller
  // has committed the owned conversation. Refresh that stale snapshot before
  // treating it as navigation away from the newly verified URL.
  if (lease.expectedURL !== expectedURL && tab?.url !== lease.expectedURL) {
    tab = await withDeadline(api.tabs.get(lease.tabId), 2500).catch(() => null);
  }
  return tab;
}

async function captureTabProgress(tabId, selectors, lease = null) {
  // Progress is intentionally non-authoritative. A provider can expose a
  // transient URL while canonicalizing a newly-created conversation. Skip the
  // sample, but leave cancellation to the injected action gate or final
  // ownership check, both of which have the full job context.
  if (lease) await assertActiveLeaseConversation(lease, false);
  if (progressScriptsInFlight.has(tabId)) throw new Error('The prior browser progress scan is still pending');
  const raw = api.scripting.executeScript({ target: { tabId }, func: captureProgress, args: [selectors, lease?.expectedURL || ''] });
  progressScriptsInFlight.set(tabId, raw);
  raw.then(() => {
    if (progressScriptsInFlight.get(tabId) === raw) progressScriptsInFlight.delete(tabId);
  }, () => {
    if (progressScriptsInFlight.get(tabId) === raw) progressScriptsInFlight.delete(tabId);
  });
  const results = await withDeadline(raw, 5000);
  if (lease) await assertActiveLeaseConversation(lease, false);
  return results?.[0]?.result || { text: '', busy: false };
}

async function reportProgress(cfg, jobId, progress) {
  try {
    const generation = activeBrowserLeases.get(String(jobId || ''))?.generation;
    if (!generation) return false;
    const response = await fetch(`${cfg.bridgeUrl}/v1/browser/jobs/${encodeURIComponent(jobId)}/progress`, {
      method: 'POST',
      headers: {
        Authorization: `Bearer ${cfg.token}`,
        'Content-Type': 'application/json',
        'X-ContextBridge-Lease-Generation': String(generation)
      },
      body: JSON.stringify(progress)
    });
    if (!response.ok && response.status !== 409) throw new Error(`Could not stream browser progress: ${response.status}`);
  } catch (_) {}
}

function browserBridgeUnavailable(message = 'The local ContextBridge service could not verify the browser job lease') {
  const error = new Error(message);
  error.code = 'browser_bridge_unavailable';
  return error;
}

function browserLeaseLost(message) {
  const error = new Error(message);
  error.code = 'browser_lease_lost';
  return error;
}

async function browserLeaseControlRequest(cfg, path, options) {
  let lastFailure = '';
  for (let attempt = 0; attempt < 3; attempt += 1) {
    try {
      // A reconnect can rotate the saved endpoint or token while a provider
      // tab is still finishing. Read the current connection on every retry
      // instead of using only the long-poll's earlier settings snapshot.
      const latest = await settings().catch(() => cfg);
      const bridgeUrl = String(latest?.bridgeUrl || cfg.bridgeUrl || '').replace(/\/$/, '');
      const token = String(latest?.token || cfg.token || '');
      const response = await fetchWithTimeout(`${bridgeUrl}${path}`, {
        ...options,
        headers: { ...(options.headers || {}), Authorization: `Bearer ${token}` }
      }, 3000);
      if (response.ok || response.status === 409) return response;
      lastFailure = `HTTP ${response.status}`;
      // Authentication, malformed requests, and missing endpoints require an
      // operator/update fix. Server and throttling failures may be transient.
      if (response.status < 500 && response.status !== 408 && response.status !== 429) break;
    } catch (error) {
      lastFailure = error?.message || String(error);
    }
    if (attempt < 2) await delay(150 * (2 ** attempt));
  }
  throw browserBridgeUnavailable(lastFailure
    ? `The local ContextBridge service could not verify the browser job lease (${lastFailure})`
    : undefined);
}

async function renewLease(cfg, jobId, generation) {
  if (!Number.isSafeInteger(generation) || generation <= 0) return false;
  const response = await browserLeaseControlRequest(cfg, `/v1/browser/jobs/${encodeURIComponent(jobId)}/lease`, {
    method: 'POST',
    headers: { 'X-ContextBridge-Lease-Generation': String(generation) },
    cache: 'no-store'
  });
  return response.ok;
}

async function releaseBrowserLease(cfg, jobId, generation) {
  if (!Number.isSafeInteger(generation) || generation <= 0) return false;
  const response = await browserLeaseControlRequest(cfg, `/v1/browser/jobs/${encodeURIComponent(jobId)}/release`, {
    method: 'POST',
    headers: { 'X-ContextBridge-Lease-Generation': String(generation) },
    cache: 'no-store'
  });
  return response.ok;
}

async function browserLeaseStatus(cfg, jobId, generation) {
  if (!Number.isSafeInteger(generation) || generation <= 0) return { active: false, expiresAt: 0 };
  const response = await browserLeaseControlRequest(cfg, `/v1/browser/jobs/${encodeURIComponent(jobId)}/lease`, {
    method: 'GET',
    headers: { 'X-ContextBridge-Lease-Generation': String(generation) },
    cache: 'no-store'
  });
  if (!response.ok) return { active: false, expiresAt: 0 };
  let body = null;
  try { body = await response.json(); } catch (_) {}
  return { active: true, expiresAt: Date.parse(body?.lease_expires_at || '') || 0 };
}

async function requireActiveBrowserLease(cfg, lease) {
  if (!lease || lease.cancelled || stopRequested) {
    const reason = stopRequested ? 'connection_stopped' : String(lease?.cancelReason || 'cancelled');
    throw browserLeaseLost(`The browser job lease was cancelled (${reason})`);
  }
  if (!await renewLease(cfg, lease.jobId, lease.generation)) {
    lease.cancelReason = 'renewal_rejected';
    lease.cancelled = true;
    throw browserLeaseLost('The browser job lease was lost');
  }
}

async function claimBrowserAction(cfg, jobId, generation, action) {
  const response = await browserLeaseControlRequest(cfg, `/v1/browser/jobs/${encodeURIComponent(jobId)}/claim`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json',
      'X-ContextBridge-Lease-Generation': String(generation) },
    cache: 'no-store',
    body: JSON.stringify({ action })
  });
  return response.ok;
}

async function authorizeBrowserJobAction(message, sender) {
  const jobId = String(message?.jobId || '');
  const generation = Number(message?.generation || 0);
  const action = String(message?.action || '');
  let lease = activeBrowserLeases.get(jobId);
  if (!lease) lease = await recoverPersistedBrowserLease(jobId, generation, action, message, sender);
  if (!lease || lease.cancelled || lease.generation !== generation || lease.tabId !== Number(sender?.tab?.id || 0) || stopRequested) {
    return { ok: false, error: 'browser_job_lease_lost' };
  }
  touchRecoveredBrowserLease(lease);
  const reservation = recoveredTabReservations.get(lease.tabId);
  if (lease.recovered && (!reservation || reservation.token !== lease.reservationToken || reservation.quarantined)) {
    return { ok: false, error: 'browser_job_lease_lost' };
  }
  const cfg = await settings();
  if (activeBrowserLeases.get(jobId) !== lease || lease.cancelled) return { ok: false, error: 'browser_job_lease_lost' };
  if (!cfg.running || !configuredTabIDs(cfg).includes(lease.tabId)) {
    lease.cancelReason = 'tab_detached';
    lease.cancelled = true;
    return { ok: false, error: 'browser_job_cancelled' };
  }
  if (!['observe', 'upload', 'edit', 'send'].includes(action)) return { ok: false, error: 'invalid_browser_job_action' };
  const tab = await readActiveLeaseTab(lease);
  if (activeBrowserLeases.get(jobId) !== lease || lease.cancelled) return { ok: false, error: 'browser_job_lease_lost' };
  const expectedURL = String(message?.expectedURL || '');
  let staleFreshObserver = false;
  try {
    staleFreshObserver = action === 'observe' && expectedURL !== lease.expectedURL && tab?.url === lease.expectedURL
      && isFreshChatURL(expectedURL) && !isFreshChatURL(lease.expectedURL)
      && new URL(expectedURL).origin === new URL(lease.expectedURL).origin;
  } catch (_) {}
  if (!tab?.url || !expectedURL || (expectedURL !== lease.expectedURL && !staleFreshObserver)) {
    lease.cancelReason = 'conversation_changed';
    lease.cancelled = true;
    return { ok: false, error: 'browser_conversation_changed' };
  }
  if (tab.url !== lease.expectedURL) {
    let promoted = false;
    if (action === 'observe') {
      for (let attempt = 0; attempt < 3 && !promoted; attempt += 1) {
        try { promoted = await promoteActiveLeaseConversation(lease, tab); } catch (_) {}
        if (promoted || !isPendingLeaseConversation(lease, tab) || lease.cancelled) break;
        if (attempt < 2) await delay(250);
      }
    }
    if (!promoted) {
      if (action === 'observe' && isPendingLeaseConversation(lease, tab) && !lease.cancelled) {
        return { ok: false, error: 'browser_conversation_unverified' };
      }
      lease.cancelReason = 'conversation_changed';
      lease.cancelled = true;
      return { ok: false, error: 'browser_conversation_changed' };
    }
    if (activeBrowserLeases.get(jobId) !== lease || lease.cancelled) return { ok: false, error: 'browser_job_lease_lost' };
  }
  if (action === 'observe') {
    let renewed;
    try { renewed = await renewLease(cfg, jobId, generation); }
    catch (error) { return { ok: false, error: error?.code || 'browser_bridge_unavailable' }; }
    if (activeBrowserLeases.get(jobId) !== lease || lease.cancelled) return { ok: false, error: 'browser_job_lease_lost' };
    if (!renewed) {
      if (lease.recovered) await rejectRecoveredBrowserLease(lease);
      else {
        lease.cancelReason = 'renewal_rejected';
        lease.cancelled = true;
      }
      return { ok: false, error: 'browser_job_lease_lost' };
    }
    return { ok: true, expectedURL: lease.expectedURL };
  }
  let claimed;
  try { claimed = await claimBrowserAction(cfg, jobId, generation, action); }
  catch (error) { return { ok: false, error: error?.code || 'browser_bridge_unavailable' }; }
  if (activeBrowserLeases.get(jobId) !== lease || lease.cancelled) return { ok: false, error: 'browser_job_lease_lost' };
  if (!claimed) {
    if (lease.recovered) await rejectRecoveredBrowserLease(lease);
    else {
      lease.cancelReason = 'action_rejected';
      lease.cancelled = true;
    }
    return { ok: false, error: 'browser_job_lease_lost' };
  }
  lease.sentUnknown = true;
  if (!await markBrowserJobSentUnknown(jobId, generation, action)) {
    lease.cancelReason = 'claim_persistence_failed';
    lease.cancelled = true;
    return { ok: false, error: 'browser_claim_persistence_failed' };
  }
  if (activeBrowserLeases.get(jobId) !== lease || lease.cancelled) return { ok: false, error: 'browser_job_lease_lost' };
  return { ok: true, sentUnknown: true, expectedURL: lease.expectedURL };
}

function browserClaimKey(jobId, generation) {
  return `${String(jobId || '')}:${Number(generation || 0)}`;
}

function recoveredLeaseDeadline(claim) {
  const absolute = Date.parse(String(claim?.deadline || ''));
  if (Number.isFinite(absolute) && absolute > 0) return absolute;
  // Pre-deadline releases can exist for one upgrade. Preserve their previous
  // fail-closed 24-hour age bound rather than guessing a shorter job timeout.
  return Number(claim?.at || 0) + 24 * 60 * 60 * 1000;
}

function reserveRecoveredBrowserLease(lease) {
  if (!lease?.recovered || lease.cancelled) return null;
  let reservation = recoveredTabReservations.get(lease.tabId);
  if (!reservation) {
    reservation = { token: Symbol(`tab-${lease.tabId}`), claims: new Map(), quarantined: false };
    recoveredTabReservations.set(lease.tabId, reservation);
  }
  const key = browserClaimKey(lease.jobId, lease.generation);
  reservation.claims.set(key, lease);
  reservation.quarantined = reservation.claims.size > 1;
  lease.reservationToken = reservation.token;
  busyTabs.add(lease.tabId);
  return reservation;
}

function releaseRecoveredBrowserLease(lease) {
  if (!lease?.recovered) return;
  if (lease.recoveryTimer) clearTimeout(lease.recoveryTimer);
  lease.recoveryTimer = 0;
  const current = activeBrowserLeases.get(lease.jobId);
  if (current === lease) activeBrowserLeases.delete(lease.jobId);
  const reservation = recoveredTabReservations.get(lease.tabId);
  if (!reservation || reservation.token !== lease.reservationToken) return;
  const key = browserClaimKey(lease.jobId, lease.generation);
  if (reservation.claims.get(key) === lease) reservation.claims.delete(key);
  if (reservation.claims.size === 0) {
    recoveredTabReservations.delete(lease.tabId);
    const anotherLeaseOwnsTab = [...activeBrowserLeases.values()].some((candidate) => candidate !== lease
      && !candidate.cancelled && candidate.tabId === lease.tabId);
    if (!anotherLeaseOwnsTab) busyTabs.delete(lease.tabId);
  } else {
    reservation.quarantined = reservation.claims.size > 1;
  }
}

function cancelRecoveredBrowserLease(lease, removeActive = false) {
  if (!lease) return;
  lease.cancelled = true;
  if (lease.recovered) releaseRecoveredBrowserLease(lease);
  else if (removeActive && activeBrowserLeases.get(lease.jobId) === lease) activeBrowserLeases.delete(lease.jobId);
}

function scheduleRecoveredBrowserLeaseCheck(lease, delayMilliseconds = 1000) {
  if (!lease?.recovered || lease.cancelled || lease.recoveryTimer) return;
  const delayWithJitter = Math.max(250, Math.floor(delayMilliseconds + Math.random() * Math.min(1000, delayMilliseconds / 4)));
  lease.recoveryTimer = setTimeout(() => {
    lease.recoveryTimer = 0;
    void checkRecoveredBrowserLease(lease);
  }, delayWithJitter);
}

async function rejectRecoveredBrowserLease(lease) {
  const current = activeBrowserLeases.get(lease?.jobId);
  const reservation = recoveredTabReservations.get(lease?.tabId);
  if (current !== lease || !reservation || reservation.token !== lease.reservationToken) return;
  const key = browserClaimKey(lease?.jobId, lease?.generation);
  if (rejectedBrowserClaims.size >= 256) rejectedBrowserClaims.delete(rejectedBrowserClaims.values().next().value);
  rejectedBrowserClaims.add(key);
  if (lease) lease.cancelled = true;
  // Keep the tab reserved until the durable claim is gone. A late isolated
  // script cannot rehydrate it in the storage-write window because the
  // in-memory tombstone above is already authoritative for this worker.
  try {
    await forgetBrowserJobClaim(lease.jobId, {
      generation: lease.generation, tabId: lease.tabId, sessionKey: lease.sessionKey
    });
  } catch (_) {}
  releaseRecoveredBrowserLease(lease);
  void sendHeartbeat(busyTabs.size ? 'working' : 'waiting');
}

async function checkRecoveredBrowserLease(lease) {
  const current = activeBrowserLeases.get(lease?.jobId);
  const reservation = recoveredTabReservations.get(lease?.tabId);
  if (!lease?.recovered || current !== lease || !reservation || reservation.token !== lease.reservationToken) return;
  if (lease.cancelled || stopRequested || lease.lifecycleGeneration !== lifecycleGeneration) {
    cancelRecoveredBrowserLease(lease);
    return;
  }
  if (Date.now() >= lease.recoveryDeadline) {
    await rejectRecoveredBrowserLease(lease);
    return;
  }
  try {
    const status = await browserLeaseStatus(await settings(), lease.jobId, lease.generation);
    const latest = activeBrowserLeases.get(lease.jobId);
    const latestReservation = recoveredTabReservations.get(lease.tabId);
    if (latest !== lease || !latestReservation || latestReservation.token !== lease.reservationToken
        || lease.cancelled || stopRequested || lease.lifecycleGeneration !== lifecycleGeneration) {
      releaseRecoveredBrowserLease(lease);
      return;
    }
    if (!status.active) {
      await rejectRecoveredBrowserLease(lease);
      return;
    }
    lease.recoveryFailures = 0;
    const untilExpiry = status.expiresAt > Date.now() ? status.expiresAt - Date.now() : 5000;
    scheduleRecoveredBrowserLeaseCheck(lease, Math.max(1000, Math.min(5000, Math.floor(untilExpiry / 2))));
  } catch (_) {
    // Transport uncertainty is not lease loss. Hold the tab until the saved
    // absolute job deadline and retry with bounded exponential backoff.
    lease.recoveryFailures = Math.min(6, Number(lease.recoveryFailures || 0) + 1);
    scheduleRecoveredBrowserLeaseCheck(lease, Math.min(30000, 1000 * (2 ** lease.recoveryFailures)));
  }
}

function touchRecoveredBrowserLease(lease) {
  if (!lease?.recovered || lease.cancelled) return;
  reserveRecoveredBrowserLease(lease);
  scheduleRecoveredBrowserLeaseCheck(lease);
}

function persistedClaimsForTab(cfg, tabId) {
  const now = Date.now();
  return Object.entries(cfg.browserJobClaims || {}).filter(([jobId, claim]) =>
    !rejectedBrowserClaims.has(browserClaimKey(jobId, claim?.generation))
    && Number(claim?.tabId) === tabId
    && ['leased', 'sent_unknown'].includes(String(claim?.state || ''))
    && now - Number(claim?.at || 0) <= 24 * 60 * 60 * 1000
    && recoveredLeaseDeadline(claim) > now);
}

function recoveredLeaseFromClaim(cfg, jobId, claim, tab) {
  const generation = Number(claim?.generation || 0);
  const tabId = Number(claim?.tabId || 0);
  if (!jobId || !Number.isSafeInteger(generation) || generation <= 0 || !Number.isInteger(tabId) || tabId <= 0
      || rejectedBrowserClaims.has(browserClaimKey(jobId, generation))
      || !configuredTabIDs(cfg).includes(tabId) || !claim?.sessionKey || !claim?.expectedURL
      || !['leased', 'sent_unknown'].includes(String(claim?.state || ''))
      || Date.now() - Number(claim?.at || 0) > 24 * 60 * 60 * 1000
      || recoveredLeaseDeadline(claim) <= Date.now()) return null;
  const binding = cfg.sessionBindings?.[claim.sessionKey];
  if (!binding || Number(binding.tabId) !== tabId || binding.url !== claim.expectedURL || !tab?.url) return null;
  let validConversation = tab.url === claim.expectedURL;
  try {
    validConversation ||= claim.state === 'sent_unknown' && isFreshChatURL(claim.expectedURL) && !isFreshChatURL(tab.url)
      && new URL(claim.expectedURL).origin === new URL(tab.url).origin && Boolean(claim.promptProof);
  } catch (_) {}
  if (!validConversation) return null;
  const profileName = String(claim.profileName || profileForTab(cfg, tab)?.name || '');
  if (!['chatgpt', 'gemini'].includes(profileName)) return null;
  return { jobId, generation, tabId, expectedURL: String(claim.expectedURL), sessionKey: String(claim.sessionKey),
    profileName, sentUnknown: claim.state === 'sent_unknown', prompt: '', promptProof: claim.promptProof || null,
    ownedTurn: claim.ownedTurn || null, cancelled: false, recovered: true, lifecycleGeneration,
    recoveryDeadline: recoveredLeaseDeadline(claim), recoveryFailures: 0 };
}

async function restorePersistedBrowserLeaseReservations(generation = lifecycleGeneration) {
  const cfg = await settings();
  if (!cfg.running || stopRequested || generation !== lifecycleGeneration) return;
  const tabIDs = [...new Set(Object.values(cfg.browserJobClaims || {}).map((claim) => Number(claim?.tabId || 0)).filter(Boolean))];
  for (const tabId of tabIDs) {
    if (generation !== lifecycleGeneration || stopRequested) return;
    const tab = await api.tabs.get(tabId).catch(() => null);
    const claims = persistedClaimsForTab(cfg, tabId);
    for (const [jobId, claim] of claims) {
      if (generation !== lifecycleGeneration || stopRequested) return;
      const existing = activeBrowserLeases.get(jobId);
      if (existing && !existing.cancelled) {
        if (existing.generation === Number(claim.generation) && existing.recovered) touchRecoveredBrowserLease(existing);
        continue;
      }
      // tabs.get() and earlier claims may have yielded while a provider-script
      // action recovered a newer generation. Re-read storage and require the
      // exact durable identity before installing this snapshot.
      const latestCfg = await settings();
      const latestClaim = latestCfg.browserJobClaims?.[jobId];
      if (!latestCfg.running || generation !== lifecycleGeneration || stopRequested || !latestClaim
          || Number(latestClaim.generation) !== Number(claim.generation)
          || Number(latestClaim.tabId) !== Number(claim.tabId)
          || String(latestClaim.sessionKey || '') !== String(claim.sessionKey || '')
          || String(latestClaim.expectedURL || '') !== String(claim.expectedURL || '')) continue;
      const current = activeBrowserLeases.get(jobId);
      if (current && !current.cancelled) continue;
      const lease = recoveredLeaseFromClaim(latestCfg, jobId, latestClaim, tab);
      if (!lease) continue;
      activeBrowserLeases.set(jobId, lease);
      reserveRecoveredBrowserLease(lease);
      scheduleRecoveredBrowserLeaseCheck(lease, 250);
    }
  }
}

// MV3 may suspend a background worker while the isolated provider-tab script
// is awaiting an answer. Rebuild only the minimal action gate from an exact,
// durable claim; the relay claim/lease endpoint remains authoritative before
// every provider side effect or observation. No prompt text is persisted.
async function recoverPersistedBrowserLease(jobId, generation, action, message, sender) {
  if (!jobId || !Number.isSafeInteger(generation) || generation <= 0 || stopRequested
      || !['observe', 'upload', 'edit', 'send'].includes(action)) return null;
  const tabId = Number(sender?.tab?.id || 0);
  if (!Number.isInteger(tabId) || tabId <= 0) return null;
  let cfg = await settings();
  let claim = cfg.browserJobClaims?.[jobId];
  if (rejectedBrowserClaims.has(browserClaimKey(jobId, generation))) return null;
  if (!cfg.running || !configuredTabIDs(cfg).includes(tabId) || !claim
      || Number(claim.generation) !== generation || Number(claim.tabId) !== tabId
      || !claim.sessionKey || !claim.expectedURL
      || !['leased', 'sent_unknown'].includes(String(claim.state || ''))
      || Date.now() - Number(claim.at || 0) > 24 * 60 * 60 * 1000) return null;
  const tab = await api.tabs.get(tabId).catch(() => null);
  if (!tab?.url) return null;
  // tabs.get() yields. Another provider-script message may have recovered a
  // newer generation in that window, so refresh the durable identity and
  // never overwrite an existing live lease from this stale snapshot.
  cfg = await settings();
  claim = cfg.browserJobClaims?.[jobId];
  if (!claim || Number(claim.generation) !== generation || Number(claim.tabId) !== tabId
      || !claim.sessionKey || !claim.expectedURL || !['leased', 'sent_unknown'].includes(String(claim.state || ''))
      || rejectedBrowserClaims.has(browserClaimKey(jobId, generation))) return null;
  const concurrent = activeBrowserLeases.get(jobId);
  if (concurrent && !concurrent.cancelled) {
    return concurrent.generation === generation && concurrent.tabId === tabId
      && concurrent.sessionKey === String(claim.sessionKey) && concurrent.expectedURL === String(claim.expectedURL)
      ? concurrent : null;
  }
  const competingClaims = persistedClaimsForTab(cfg, tabId);
  if (competingClaims.length > 1) return null;
  const requestedURL = String(message?.expectedURL || '');
  let exactConversation = tab.url === claim.expectedURL && requestedURL === claim.expectedURL;
  let freshConversationPromotion = false;
  try {
    freshConversationPromotion = action === 'observe' && requestedURL === claim.expectedURL
      && isFreshChatURL(claim.expectedURL) && !isFreshChatURL(tab.url)
      && new URL(claim.expectedURL).origin === new URL(tab.url).origin;
  } catch (_) {}
  if (!exactConversation && !freshConversationPromotion) return null;
  const lease = recoveredLeaseFromClaim(cfg, jobId, claim, tab);
  if (!lease) return null;
  activeBrowserLeases.set(jobId, lease);
  touchRecoveredBrowserLease(lease);
  return lease;
}

function serializeBrowserClaimWrite(operation) {
  const result = browserClaimWrite.catch(() => {}).then(operation);
  browserClaimWrite = result.then(() => {}, () => {});
  return result;
}

async function rememberBrowserJobClaim(work, tabId, expectedURL, baseline, profileName = '') {
  const baselineDigest = await sha256Text(String(baseline?.text || ''));
  const proofNonce = [...crypto.getRandomValues(new Uint8Array(16))]
    .map((byte) => byte.toString(16).padStart(2, '0')).join('');
  const normalizedPrompt = String(work?.job?.prompt || '').normalize('NFKC').replace(/\s+/g, ' ').trim();
  const promptProof = { nonce: proofNonce, digest: await sha256Text(`${proofNonce}\u0000${normalizedPrompt}`) };
  return serializeBrowserClaimWrite(async () => {
    const cfg = await settings();
    const now = Date.now();
    const claims = Object.fromEntries(Object.entries(cfg.browserJobClaims || {})
      .filter(([, value]) => now - Number(value?.at || 0) < 24 * 60 * 60 * 1000)
      .sort((a, b) => Number(b[1]?.at || 0) - Number(a[1]?.at || 0)).slice(0, 63));
    claims[work.job.id] = { generation: Number(work.lease_generation), sessionKey: workSessionKey(work), tabId,
      expectedURL, baselineCount: Number(baseline?.response_count) || 0,
      baselineIdentity: String(baseline?.response_identity || ''), baselineDigest, state: 'leased', at: now,
      profileName: String(profileName || '').slice(0, 100), promptProof,
      deadline: new Date(Date.parse(work?.deadline || '') || now + 24 * 60 * 60 * 1000).toISOString() };
    await api.storage.local.set({ browserJobClaims: claims });
  });
}

async function markBrowserJobSentUnknown(jobId, generation, action) {
  return serializeBrowserClaimWrite(async () => {
    const cfg = await settings();
    const existing = cfg.browserJobClaims?.[jobId];
    if (!existing || Number(existing.generation) !== generation) return false;
    const browserJobClaims = { ...cfg.browserJobClaims,
      [jobId]: { ...existing, state: 'sent_unknown', action, at: Date.now() } };
    await api.storage.local.set({ browserJobClaims });
    return true;
  });
}

async function forgetBrowserJobClaim(jobId, expected = null) {
  return serializeBrowserClaimWrite(async () => {
    const cfg = await settings();
    const existing = cfg.browserJobClaims?.[jobId];
    if (expected && (!existing || Number(existing.generation) !== Number(expected.generation)
        || Number(existing.tabId) !== Number(expected.tabId) || String(existing.sessionKey || '') !== String(expected.sessionKey || ''))) {
      return false;
    }
    const browserJobClaims = { ...cfg.browserJobClaims };
    delete browserJobClaims[jobId];
    await api.storage.local.set({ browserJobClaims });
    return true;
  });
}

async function completeWork(cfg, jobId, decision, generation, timeoutMilliseconds = 30000) {
  const response = await fetchWithTimeout(`${cfg.bridgeUrl}/v1/browser/jobs/${encodeURIComponent(jobId)}/complete`, {
    method: 'POST',
    headers: {
      Authorization: `Bearer ${cfg.token}`,
      'Content-Type': 'application/json',
      'X-ContextBridge-Lease-Generation': String(generation)
    },
    body: JSON.stringify(decision)
  }, Math.max(1, Math.min(30000, Number(timeoutMilliseconds) || 30000)));
  if (!response.ok && response.status !== 409) throw new Error(`Could not return browser result: ${response.status}`);
  try {
    const latest = await settings();
    const pendingCompletions = { ...latest.pendingCompletions };
    delete pendingCompletions[jobId];
    await api.storage.local.set({ pendingCompletions });
  } catch (_) {
    // The relay acknowledgement is authoritative. Local retry-cache cleanup
    // must never turn a completed job back into an apparent failure.
  }
  return response.ok;
}

async function markFinishedTabForClose(work, tabId, decision) {
  const byJob = work?.job?.metadata?.contextbridge_close_tab_after_job === true;
  const perJob = work?.job?.metadata?.contextbridge_new_chat_per_job === true;
  const cfg = await settings();
  if (!byJob && !(perJob && cfg.autoCloseFinishedChats)) return;
  if ((decision.artifacts || []).some((artifact) => artifact?.url && !artifact?.data_base64)) return;
  await serializeSessionWrite(async () => {
    const latest = await settings();
    const key = workSessionKey(work);
    const bindings = { ...latest.sessionBindings };
    const binding = bindings[key];
    if (!binding?.autoCreated || !binding.ownedTurn?.id || Number(binding.tabId) !== tabId) return;
    bindings[key] = { ...binding, closeEligibleAt: Date.now() + 2 * 60 * 1000, closeRequestedByJob: byJob };
    await api.storage.local.set({ sessionBindings: bindings });
  });
}

async function closeFinishedOwnedTabs() {
  const cfg = await settings();
  if (!cfg.running) return;
  for (const [key, binding] of Object.entries(cfg.sessionBindings || {})) {
    const tabId = Number(binding?.tabId);
    if (!binding?.autoCreated || !binding?.ownedTurn?.id || !tabId
        || Number(binding.closeEligibleAt || 0) > Date.now() || !binding.closeEligibleAt
        || (!binding.closeRequestedByJob && !cfg.autoCloseFinishedChats)
        || busyTabs.has(tabId) || cfg.ownedDrafts?.[tabId]) continue;
    try {
      const tab = await api.tabs.get(tabId);
      if (!tab || tab.active || tab.url !== binding.url || !configuredTabIDs(cfg).includes(tabId)) continue;
      const profile = profileForTab(cfg, tab);
      if (!profile?.selectors) continue;
      const first = await api.scripting.executeScript({ target: { tabId }, func: inspectOwnedDraft,
        args: [profile.selectors, profile.name] });
      if (!safeToCloseOwnedTab(first?.[0]?.result)) continue;
      await delay(500);
      const latest = await settings();
      const currentBinding = latest.sessionBindings?.[key];
      const currentTab = await api.tabs.get(tabId);
      if (currentBinding?.url !== binding.url || Number(currentBinding?.tabId) !== tabId
          || currentTab.active || currentTab.url !== binding.url || busyTabs.has(tabId) || latest.ownedDrafts?.[tabId]) continue;
      const second = await api.scripting.executeScript({ target: { tabId }, func: inspectOwnedDraft,
        args: [profile.selectors, profile.name] });
      if (!safeToCloseOwnedTab(second?.[0]?.result) || (await api.tabs.get(tabId)).active) continue;
      await api.tabs.remove(tabId);
    } catch (_) { /* Never force-close on an uncertain page state. */ }
  }
}

function safeToCloseOwnedTab(state) {
  return Boolean(state?.empty && !state.provider_busy && !state.has_attachments && !state.focused);
}

async function preserveAndClearDraft(cfg, tabId, tab, profile, job, lease = null) {
  const selectors = profile.selectors || {};
  const owned = cfg.ownedDrafts?.[tabId];
  if (owned) {
    if (!owned.nonce || !owned.digest || !owned.jobId) throw new Error('The ContextBridge draft ownership record is incomplete; no draft was saved or cleared');
    const probe = await api.scripting.executeScript({ target: { tabId }, func: inspectOwnedDraft, args: [selectors, profile.name, owned.nonce] });
    const current = probe?.[0]?.result;
    if (!current || current.unavailable) throw new Error('The ContextBridge draft could not be inspected; it was not saved as a user draft or cleared');
    if (current.empty && !current.provider_busy && !current.has_attachments) {
      const forgotten = await api.scripting.executeScript({ target: { tabId }, func: forgetEmptyOwnedDraft, args: [selectors] });
      if (forgotten?.[0]?.result === true) await removeOwnedDraft(tabId);
      return;
    }
    const matching = owned.origin === new URL(tab.url).origin && owned.digest === current?.digest;
    if (matching) {
      if (current.owner_job !== owned.jobId) throw new Error('The draft matches a ContextBridge prompt, but its page ownership marker is missing; it was not saved or cleared');
      if (current.provider_busy || current.has_attachments || current.focused) {
        throw new Error('A ContextBridge prompt is still in the editor, but the page is busy or being edited; it was not saved as a user draft or cleared');
      }
      if (lease) await requireActiveBrowserLease(cfg, lease);
      const cleared = await api.scripting.executeScript({ target: { tabId }, func: clearCurrentDraft,
        args: [selectors, '', profile.name, owned.digest, owned.nonce] });
      if (!cleared?.[0]?.result) throw new Error('The ContextBridge prompt changed before clearing; it was not saved as a user draft');
      await removeOwnedDraft(tabId);
      return;
    }
    if (current.owner_job) throw new Error('The ContextBridge-marked editor changed; it was not saved as a user draft or cleared');
    await removeOwnedDraft(tabId);
    if (current?.empty) return;
  }
  const captured = await api.scripting.executeScript({ target: { tabId }, func: captureCurrentDraft, args: [selectors, profile.name] });
  const draft = captured?.[0]?.result;
  if (draft?.owner_job) throw new Error('A ContextBridge-marked editor has no matching ownership record; it was not saved as a user draft');
  if (draft?.provider_busy) throw new Error('ChatGPT still shows Stop; the previous generation may be active and the existing draft was left untouched');
  if (draft?.has_attachments) throw new Error('An existing file attachment cannot be preserved as text history; editor was left untouched');
  if (!cfg.preserveDrafts) return;
  if (!draft?.text && !draft?.too_large) return;
  if (draft.too_large) throw new Error('Existing draft exceeds the 16 KiB local history limit; editor was left untouched');
  const response = await fetch(`${cfg.bridgeUrl}/v1/browser/drafts`, {
    method: 'POST',
    headers: { Authorization: `Bearer ${cfg.token}`, 'Content-Type': 'application/json' },
    body: JSON.stringify({ profile: profile.name || 'visual', tab_id: tabId,
      tab_title: String(tab.title || '').slice(0, 200), origin: new URL(tab.url).origin,
      session_id: String(job.session_id || '').slice(0, 100), text: draft.text })
  });
  if (!response.ok) throw new Error('Could not save the existing draft locally; editor was left untouched');
  if (lease) await requireActiveBrowserLease(cfg, lease);
  const cleared = await api.scripting.executeScript({ target: { tabId }, func: clearCurrentDraft, args: [selectors, draft.text, profile.name] });
  if (!cleared?.[0]?.result) throw new Error('The draft changed while being saved or the editor rejected clearing; job was not sent');
}

async function sha256Text(value) {
  const bytes = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(String(value || '')));
  return [...new Uint8Array(bytes)].map((byte) => byte.toString(16).padStart(2, '0')).join('');
}

async function markOwnedDraft(tabId, tab, job) {
  const nonce = [...crypto.getRandomValues(new Uint8Array(16))].map((byte) => byte.toString(16).padStart(2, '0')).join('');
  const digest = await sha256Text(`${nonce}\u0000${job.prompt || ''}`);
  await serializeSessionWrite(async () => {
    const latest = await settings();
    const ownedDrafts = { ...latest.ownedDrafts, [tabId]: {
      jobId: String(job.id || ''), origin: new URL(tab.url).origin, digest, nonce, at: Date.now()
    } };
    await api.storage.local.set({ ownedDrafts });
  });
}

async function removeOwnedDraft(tabId) {
  await serializeSessionWrite(async () => {
    const latest = await settings();
    const ownedDrafts = { ...latest.ownedDrafts };
    delete ownedDrafts[tabId];
    await api.storage.local.set({ ownedDrafts });
  });
}

async function releaseOwnedDraftIfEmpty(tabId, profile) {
  try {
    const result = await api.scripting.executeScript({ target: { tabId }, func: forgetEmptyOwnedDraft,
      args: [profile.selectors || {}, profile.name] });
    if (result?.[0]?.result === true) await removeOwnedDraft(tabId);
  } catch (_) { /* Keep ownership proof if the page is unavailable. */ }
}

function forgetEmptyOwnedDraft(selectors) {
  const visible = (element) => Boolean(element && (element.offsetWidth || element.offsetHeight || element.getClientRects().length));
  let input = null;
  for (const selector of selectors?.input || []) {
    let nodes;
    try { nodes = document.querySelectorAll(selector); } catch (_) { continue; }
    for (let index = nodes.length - 1; index >= Math.max(0, nodes.length - 64); index -= 1) {
      const candidate = nodes.item ? nodes.item(index) : nodes[index];
      if (visible(candidate)) { input = candidate; break; }
    }
    if (input) break;
  }
  if (!input || String(typeof input.value === 'string' ? input.value : (input.innerText || input.textContent || '')).trim()) return false;
  input.removeAttribute?.('data-contextbridge-owned-job');
  return true;
}

async function inspectOwnedDraft(selectors, profileName = '', nonce = '') {
  const visible = (element) => Boolean(element && (element.offsetWidth || element.offsetHeight || element.getClientRects().length));
  const lastVisible = (items, limit = 64) => {
    for (const selector of items || []) {
      let nodes;
      try { nodes = document.querySelectorAll(selector); } catch (_) { continue; }
      for (let index = nodes.length - 1; index >= Math.max(0, nodes.length - limit); index -= 1) {
        const candidate = nodes.item ? nodes.item(index) : nodes[index];
        if (visible(candidate)) return candidate;
      }
    }
    return null;
  };
  const busy = profileName === 'chatgpt' && Boolean(lastVisible(['button[data-testid="stop-button"], button[aria-label="Antwort stoppen"], button[aria-label="Stop generating"]']));
  const input = lastVisible(selectors?.input);
  if (!input) return { empty: false, unavailable: true, provider_busy: busy };
  const scope = input.closest?.('form, [data-node-type="input-area"]');
  let selectedFile = false;
  const fileInputs = scope?.querySelectorAll?.('input[type="file"]') || [];
  for (let index = 0; index < Math.min(fileInputs.length, 16); index += 1) {
    const field = fileInputs.item ? fileInputs.item(index) : fileInputs[index];
    if (field?.files?.length) { selectedFile = true; break; }
  }
  const hasAttachments = Boolean(scope && (selectedFile
    || scope.querySelector('[data-testid*="attachment-chip" i], [data-testid*="attached-file" i], [data-testid*="file-thumbnail" i], [data-test-id*="attachment" i]')));
  const value = String(typeof input.value === 'string' ? input.value : (input.innerText || input.textContent || ''));
  const bytes = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(`${nonce}\u0000${value}`));
  return { digest: [...new Uint8Array(bytes)].map((byte) => byte.toString(16).padStart(2, '0')).join(''),
    empty: !value.trim(), has_attachments: hasAttachments, provider_busy: busy,
    owner_job: String(input.getAttribute?.('data-contextbridge-owned-job') || ''),
    focused: Boolean(document.hasFocus?.() && (document.activeElement === input || input.contains?.(document.activeElement))) };
}

function captureCurrentDraft(selectors, profileName = '') {
  if (profileName === 'chatgpt' && [...document.querySelectorAll('button[data-testid="stop-button"], button[aria-label="Antwort stoppen"], button[aria-label="Stop generating"]')]
    .some((element) => element.offsetWidth || element.offsetHeight || element.getClientRects().length)) return { provider_busy: true };
  const input = (selectors?.input || []).flatMap((selector) => {
    try { return [...document.querySelectorAll(selector)]; } catch (_) { return []; }
  }).find((element) => element && (element.offsetWidth || element.offsetHeight || element.getClientRects().length));
  if (!input) return { text: '' };
  const scope = input.closest?.('form, [data-node-type="input-area"]');
  if (scope && ([...scope.querySelectorAll('input[type="file"]')].some((field) => field.files?.length)
    || scope.querySelector('[data-testid*="attachment-chip" i], [data-testid*="attached-file" i], [data-testid*="file-thumbnail" i], [data-test-id*="attachment" i]'))) {
    return { has_attachments: true };
  }
  const text = String(typeof input.value === 'string' ? input.value : (input.innerText || input.textContent || ''));
  if (!text.trim()) return { text: '' };
  if (new TextEncoder().encode(text).length > 16 * 1024) return { too_large: true, owner_job: String(input.getAttribute?.('data-contextbridge-owned-job') || '') };
  return { text, owner_job: String(input.getAttribute?.('data-contextbridge-owned-job') || '') };
}

async function clearCurrentDraft(selectors, expected, profileName = '', expectedDigest = '', nonce = '') {
  // A generation can start after capture but before the saved draft is cleared.
  if (profileName === 'chatgpt' && [...document.querySelectorAll('button[data-testid="stop-button"], button[aria-label="Antwort stoppen"], button[aria-label="Stop generating"]')]
    .some((element) => element.offsetWidth || element.offsetHeight || element.getClientRects().length)) return false;
  const input = (selectors?.input || []).flatMap((selector) => {
    try { return [...document.querySelectorAll(selector)]; } catch (_) { return []; }
  }).find((element) => element && (element.offsetWidth || element.offsetHeight || element.getClientRects().length));
  if (!input) return false;
  const scope = input.closest?.('form, [data-node-type="input-area"]');
  if (scope && ([...scope.querySelectorAll('input[type="file"]')].some((field) => field.files?.length)
    || scope.querySelector('[data-testid*="attachment-chip" i], [data-testid*="attached-file" i], [data-testid*="file-thumbnail" i], [data-test-id*="attachment" i]'))) return false;
  const current = () => String(typeof input.value === 'string' ? input.value : (input.innerText || input.textContent || ''));
  const matches = async () => expectedDigest
    ? [...new Uint8Array(await crypto.subtle.digest('SHA-256', new TextEncoder().encode(`${nonce}\u0000${current()}`)))].map((byte) => byte.toString(16).padStart(2, '0')).join('') === expectedDigest
    : current() === expected;
  if (!current().trim()) { input.removeAttribute?.('data-contextbridge-owned-job'); return true; }
  if (!await matches()) return false;
  input.focus();
  if (input.isConnected === false || !await matches()) return false;
  if (typeof input.value === 'string') {
    const setter = Object.getOwnPropertyDescriptor(Object.getPrototypeOf(input), 'value')?.set;
    if (setter) setter.call(input, '');
    else input.value = '';
    input.dispatchEvent(new Event('input', { bubbles: true }));
  } else {
    const selection = window.getSelection();
    const range = document.createRange();
    range.selectNodeContents(input);
    selection.removeAllRanges();
    selection.addRange(range);
    document.execCommand('delete');
    selection.removeAllRanges();
    input.dispatchEvent(new InputEvent('input', { bubbles: true, inputType: 'deleteContentBackward' }));
  }
  await new Promise((resolve) => setTimeout(resolve, 120));
  const latest = (selectors?.input || []).flatMap((selector) => {
    try { return [...document.querySelectorAll(selector)]; } catch (_) { return []; }
  }).find((element) => element && (element.offsetWidth || element.offsetHeight || element.getClientRects().length));
  const empty = Boolean(latest && !String(typeof latest.value === 'string' ? latest.value : (latest.innerText || latest.textContent || '')).trim());
  if (empty) latest.removeAttribute?.('data-contextbridge-owned-job');
  return empty;
}

async function hydrateArtifactReferences(artifacts, pageURL, spec) {
  if (!spec?.artifacts || !Array.isArray(artifacts)) return [];
  const limit = Math.max(1024, Math.min(Number(spec.max_artifact_bytes) || 12 << 20, 12 << 20));
  const page = new URL(pageURL);
  const pageOrigin = page.origin;
  const cleanArtifact = (artifact) => {
    const clean = { ...artifact };
    delete clean.contextbridge_provenance;
    return clean;
  };
  const transferPolicy = (resource, provenance) => {
    const geminiMediaHost = page.hostname === 'gemini.google.com'
      && resource.origin === 'https://contribution.usercontent.google.com';
    if (resource.protocol !== 'https:' || (resource.origin !== pageOrigin && !geminiMediaHost)) {
      throw new Error('Provider-hosted reference');
    }
    const chatGPTPage = page.hostname === 'chatgpt.com';
    const citation = provenance === 'chatgpt_file_citation'
      && chatGPTPage && resource.origin === pageOrigin
      && /^\/backend-api\/files\/file_[a-z0-9]+\/download$/i.test(resource.pathname);
    const artifactPreview = provenance === 'chatgpt_artifact_preview'
      && chatGPTPage && resource.origin === pageOrigin;
    if (String(provenance || '').startsWith('chatgpt_') && !citation && !artifactPreview) {
      throw new Error('Invalid authenticated artifact provenance');
    }
    return { credentials: citation || artifactPreview ? 'include' : 'omit', geminiMediaHost };
  };
  const result = [];
  let total = 0;
	  for (const artifact of artifacts.slice(0, 12)) {
    if (artifact.data_base64) {
      total += Number(artifact.size) || Math.floor(artifact.data_base64.length * 3 / 4);
      result.push(cleanArtifact(artifact));
      continue;
    }
    if (!artifact.url) continue;
	    if (artifact.contextbridge_provenance === 'generic_link') {
	      // A download-looking anchor is not proof that its target is an AI
	      // artifact. Preserve the reference for display, but never request it.
	      result.push(cleanArtifact(artifact));
	      continue;
	    }
	    let transferTimeout = 0;
	    try {
	      const resource = new URL(artifact.url);
	      const policy = transferPolicy(resource, artifact.contextbridge_provenance);
	      const controller = new AbortController();
	      transferTimeout = setTimeout(() => controller.abort(), 15000);
	      const response = await fetch(resource.href, { credentials: policy.credentials,
	        redirect: 'error', signal: controller.signal });
	      if (response.url) {
	        const finalURL = new URL(response.url);
	        const finalPolicy = transferPolicy(finalURL, artifact.contextbridge_provenance);
	        if (finalPolicy.credentials !== policy.credentials) throw new Error('Artifact redirect changed credential policy');
	      }
	      if (!response.ok || !response.body) throw new Error('Resource could not be read');
	      const declared = Number(response.headers.get('content-length'));
	      if (Number.isFinite(declared) && declared > limit - total) throw new Error('Artifact exceeds transfer limit');
      const contentType = String(response.headers.get('content-type') || '').split(';')[0].toLowerCase();
      if (artifact.media_type?.startsWith('image/') && !contentType.startsWith('image/') && contentType !== 'application/octet-stream') throw new Error('Image response has the wrong media type');
      if (/^(audio|video)\//.test(artifact.media_type || '') && !/^(audio|video)\//.test(contentType) && contentType !== 'application/octet-stream') throw new Error('Media response has the wrong media type');
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
        ...cleanArtifact(artifact),
        url: '',
        media_type: contentType && contentType !== 'application/octet-stream' ? contentType : artifact.media_type,
        size,
        sha256: [...new Uint8Array(digest)].map((value) => value.toString(16).padStart(2, '0')).join(''),
        data_base64: btoa(binary)
      });
      total += size;
	    } catch (_) {
	      result.push(cleanArtifact(artifact));
	    } finally { if (transferTimeout) clearTimeout(transferTimeout); }
  }
  return result;
}

async function flushPendingCompletions(cfg) {
  for (const [jobId, pending] of Object.entries(cfg.pendingCompletions)) {
    try {
      const generation = Number(pending?.lease_generation || 0);
      if (!generation) {
        const latest = await settings();
        const pendingCompletions = { ...latest.pendingCompletions };
        delete pendingCompletions[jobId];
        await api.storage.local.set({ pendingCompletions });
        continue;
      }
	      await completeWork(await settings(), jobId, pendingCompletionDecision(pending), generation);
    } catch (_) {
      break;
    }
  }
}

function pendingCompletionDecision(pending) {
  if (pending?.decision) return pending.decision;
  const decision = { ...(pending || {}) };
  delete decision.lease_generation;
  return decision;
}

function boundedPendingCompletion(decision, generation) {
  const value = { ...decision, lease_generation: generation };
  try {
    return new TextEncoder().encode(JSON.stringify(value)).byteLength <= PENDING_COMPLETION_MAX_BYTES ? value : null;
  } catch (_) { return null; }
}

async function rememberPendingCompletion(jobId, decision, generation) {
  const candidate = boundedPendingCompletion(decision, generation);
  if (!candidate) return false;
  try {
    const latest = await settings();
    const retained = Object.entries(latest.pendingCompletions || {})
      .filter(([id, value]) => id !== jobId && boundedPendingCompletion(pendingCompletionDecision(value), Number(value?.lease_generation || 0)))
      .slice(-15);
    await api.storage.local.set({ pendingCompletions: { ...Object.fromEntries(retained), [jobId]: candidate } });
    return true;
  } catch (_) { return false; }
}

function automate(job, profile, jobDeadline, editTarget = null, expectedConversationURL = '', leaseContext = null) {
  const selectors = profile.selectors || {};
  let activeExpectedConversationURL = expectedConversationURL;
  const requireExpectedConversation = () => {
    if (!activeExpectedConversationURL) return;
    let expected;
    let current;
    try {
      expected = new URL(activeExpectedConversationURL).href;
      current = new URL(location.href).href;
    } catch (_) {
      throw new Error('The expected conversation URL is invalid; no provider action was performed');
    }
    if (current !== expected) throw new Error('The conversation URL changed; no provider action was performed');
  };
  const authorizeAction = async (action) => {
    if (action !== 'observe') requireExpectedConversation();
    if (!leaseContext) {
      requireExpectedConversation();
      return;
    }
    const runtime = (globalThis.browser || globalThis.chrome)?.runtime;
    if (!runtime?.sendMessage) throw new Error('The browser job lease cannot be verified; no provider action was performed');
    const verificationDeadline = Math.min(Date.now() + 15000, Date.parse(jobDeadline || '') || Infinity);
    let response;
    do {
      response = await runtime.sendMessage({ type: 'authorize-browser-job-action', action,
        jobId: String(leaseContext.jobId || ''), generation: Number(leaseContext.generation || 0),
        expectedURL: activeExpectedConversationURL });
      if (action !== 'observe' || response?.error !== 'browser_conversation_unverified'
          || Date.now() >= verificationDeadline) break;
      // Provider routing can settle well before its user-turn DOM mounts.
      // Retry only observation authority and read no response while waiting.
      await new Promise((resolve) => setTimeout(resolve, 250));
    } while (Date.now() < verificationDeadline);
    if (!response?.ok) {
      if (response?.error === 'browser_conversation_unverified') {
        throw new Error('The submitted ContextBridge turn could not be verified after the conversation URL changed; no provider content was observed');
      }
      if (response?.error === 'browser_conversation_changed') {
        throw new Error('The conversation URL changed; no provider content was observed');
      }
      if (response?.error === 'browser_bridge_unavailable') {
        throw new Error('The local ContextBridge service could not verify the browser job lease; no further provider action was performed');
      }
      throw new Error('The browser job lease was lost; no further provider action was performed');
    }
    if (response.expectedURL) activeExpectedConversationURL = String(response.expectedURL);
    requireExpectedConversation();
  };
  const isVisible = (element) => Boolean(element && (element.offsetWidth || element.offsetHeight || element.getClientRects().length));
  const first = (items, limit = 64) => {
    for (const selector of items || []) {
      let nodes;
      try { nodes = document.querySelectorAll(selector); } catch (_) { continue; }
      for (let index = nodes.length - 1; index >= Math.max(0, nodes.length - limit); index -= 1) {
        const node = nodes.item ? nodes.item(index) : nodes[index];
        if (isVisible(node)) return node;
      }
    }
    return null;
  };
  const responseTail = (items, limit = 64) => {
    for (const selector of items || []) {
      try {
        const found = document.querySelectorAll(selector);
        if (!found.length) continue;
        const values = [];
        for (let index = Math.max(0, found.length - limit); index < found.length; index += 1) {
          values.push(found.item ? found.item(index) : found[index]);
        }
        return { count: found.length, values };
      } catch (_) {}
    }
    return { count: 0, values: [] };
  };
  const boundedNodes = (root, selector, limit = 64) => {
    if (!root?.querySelectorAll) return [];
    let nodes;
    try { nodes = root.querySelectorAll(selector); } catch (_) { return []; }
    const values = [];
    for (let index = Math.max(0, nodes.length - limit); index < nodes.length; index += 1) {
      values.push(nodes.item ? nodes.item(index) : nodes[index]);
    }
    return values;
  };
  const boundedCount = (root, selector, predicate = () => true, limit = 64) => {
    if (!root?.querySelectorAll) return 0;
    let nodes;
    try { nodes = root.querySelectorAll(selector); } catch (_) { return 0; }
    let count = 0;
    for (let index = nodes.length - 1; index >= Math.max(0, nodes.length - limit); index -= 1) {
      const node = nodes.item ? nodes.item(index) : nodes[index];
      if (predicate(node)) count += 1;
    }
    return count;
  };
  const isStopControl = (element) => /stop|abbrechen|beenden/i.test(`${element?.getAttribute?.('data-testid') || ''} ${element?.getAttribute?.('aria-label') || ''}`);
  const chatGPTStopVisible = () => Boolean(first(['button[data-testid="stop-button"], button[aria-label="Antwort stoppen"], button[aria-label="Stop generating"]']));
  const sendControl = () => {
    for (const selector of selectors.submit || []) {
      let nodes;
      try { nodes = document.querySelectorAll(selector); } catch (_) { continue; }
      for (let index = nodes.length - 1; index >= Math.max(0, nodes.length - 64); index -= 1) {
        const node = nodes.item ? nodes.item(index) : nodes[index];
        if (isVisible(node) && !isStopControl(node)) return node;
      }
    }
    return null;
  };
  const visibleText = (element) => (element?.innerText || element?.textContent || '').trim();
  const joinVisibleText = (parts, limit = 1024 * 1024) => {
    let result = '';
    for (const part of parts) {
      const value = visibleText(part);
      if (!value) continue;
      const remaining = limit - result.length;
      if (remaining <= 0) break;
      result += `${result ? '\n' : ''}${value.slice(0, remaining)}`;
    }
    return result;
  };
  const responseText = (element) => {
    if (!element) return '';
    if (element.querySelector('[data-testid="image-gen-loading-state"], [data-testid="image-gen-loading-progress"]')) return '';
    let markdownParts = boundedNodes(element, '[data-message-author-role="assistant"] .markdown, message-content .markdown');
    if (!markdownParts.length) markdownParts = boundedNodes(element, '.markdown');
    if (markdownParts.length) return joinVisibleText(markdownParts);
    if (element.querySelector('img') && element.matches?.('section[data-turn="assistant"], model-response')) return '';
    const assistantParts = boundedNodes(element, '[data-message-author-role="assistant"]');
    if (assistantParts.length) return joinVisibleText(assistantParts);
    return visibleText(element).slice(0, 1024 * 1024);
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
    const indicators = document.querySelectorAll('[data-testid="image-gen-loading-progress"], [role="progressbar"][aria-valuenow]');
    for (let index = indicators.length - 1; index >= Math.max(0, indicators.length - 64); index -= 1) {
      const indicator = indicators.item ? indicators.item(index) : indicators[index];
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
        if (!busyReasons.includes(reason) && first([selector])) busyReasons.push(reason);
      } catch (_) {}
    }
    const visibleStops = [];
    const stopNodes = document.querySelectorAll('button[data-testid*="stop" i], button[aria-label*="stop" i], button[aria-label*="beenden" i], button[aria-label*="abbrechen" i]');
    for (let index = stopNodes.length - 1; index >= Math.max(0, stopNodes.length - 64); index -= 1) {
      const node = stopNodes.item ? stopNodes.item(index) : stopNodes[index];
      if (isVisible(node)) visibleStops.push(node);
    }
    return {
      busy: busyReasons.length > 0,
      busyReasons,
      stopDisabled: Boolean(visibleStops.length && visibleStops.every((button) => button.disabled || button.getAttribute?.('aria-disabled') === 'true')),
      percent,
      detail: busyReasons.length ? (detail || 'Generating') : '',
      composerReady: Boolean(sendControl()),
      inputReady: Boolean(first(selectors.input))
    };
  };
  const pageBusy = () => pageState().busy;
  const providerError = (responseElement, changedResponse, beforeUserTurns) => {
    // ChatGPT's account-level "Too many requests" notice is a Radix dialog,
    // not an alert. It can overlay a still-visible composer and an older
    // successful answer, so it must be checked independently of turn changes.
    const dialogs = document.querySelectorAll('[role="dialog"], [role="alertdialog"]');
    for (let index = dialogs.length - 1; index >= Math.max(0, dialogs.length - 64); index -= 1) {
      const dialog = dialogs.item ? dialogs.item(index) : dialogs[index];
      if (!isVisible(dialog)) continue;
      const message = visibleText(dialog).slice(0, 400);
      if (/rate.?limit|usage.?limit|too many (?:requests|messages)|quota|limit erreicht|nutzungslimit|zu viele anfragen|sp[aä]ter erneut|try again later|temporarily restricted/i.test(message)) {
        return { code: 'browser_rate_limited', message: 'Provider rate-limit dialog is visible; wait before trying again', retryable: true, blocking: true };
      }
    }
    // ChatGPT can attach a retryable thread error to the newly sent *user*
    // turn without creating an assistant turn. Never classify an unrelated
    // older assistant answer as the error for this job.
    if (profile.name === 'chatgpt') {
      const userTurns = document.querySelectorAll('[data-turn="user"]');
      const latestUser = userTurns.length ? (userTurns.item ? userTurns.item(userTurns.length - 1) : userTurns[userTurns.length - 1]) : null;
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
      let nodes;
      try { nodes = document.querySelectorAll(selector); } catch (_) { continue; }
      for (let index = nodes.length - 1; index >= Math.max(0, nodes.length - (64 - containers.length)); index -= 1) {
        const node = nodes.item ? nodes.item(index) : nodes[index];
        if (isVisible(node)) containers.push(node);
      }
      if (containers.length >= 64) break;
    }
	if (responseElement) {
		try {
			const nodes = responseElement.querySelectorAll('[data-testid*="error" i], [data-testid*="rate" i], [class*="error" i]');
			for (let index = nodes.length - 1; index >= Math.max(0, nodes.length - (64 - containers.length)); index -= 1) {
				const node = nodes.item ? nodes.item(index) : nodes[index];
				if (isVisible(node)) containers.push(node);
			}
		} catch (_) {}
	}
    const text = containers.map((node) => visibleText(node).slice(0, 400)).filter(Boolean).join('\n').slice(0, 4000);
	let retryVisible = false;
	const buttons = document.querySelectorAll('button');
	for (let index = buttons.length - 1; index >= Math.max(0, buttons.length - 128); index -= 1) {
		const button = buttons.item ? buttons.item(index) : buttons[index];
		if (isVisible(button) && /retry|try again|regenerate|erneut|noch einmal|wiederholen/.test(`${visibleText(button)} ${button.getAttribute('aria-label') || ''}`.toLowerCase())) {
			retryVisible = true;
			break;
		}
	}
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
        const selection = window.getSelection();
        const range = document.createRange();
        // Never use execCommand('selectAll') here: after a draft clear it can
        // select the whole Gemini page instead of just this Quill editor.
        range.selectNodeContents(element);
        selection.removeAllRanges();
        selection.addRange(range);
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
  const editOwnedMessage = async (target) => {
    if (!target || target.provider !== profile.name || !/^[a-f0-9]{64}$/.test(String(target.digest || ''))) {
      throw new Error('Edit mode has no verified ContextBridge-owned message on this provider');
    }
    if (job.image_base64) throw new Error('Edit mode cannot safely replace file attachments');
    const turns = document.querySelectorAll(profile.name === 'chatgpt' ? 'section[data-turn="user"]' : 'user-query');
    const turn = turns.length ? (turns.item ? turns.item(turns.length - 1) : turns[turns.length - 1]) : null;
    const content = profile.name === 'chatgpt'
      ? turn?.querySelector('[data-message-author-role="user"]')
      : turn?.querySelector('[id^="user-query-content-"]');
    const id = profile.name === 'chatgpt' ? turn?.getAttribute('data-turn-id') : content?.id;
    if (!turn || !content || id !== target.id) throw new Error('The previous ContextBridge message is no longer the last user turn; nothing was edited');
    if (content.querySelector(profile.name === 'chatgpt'
      ? 'img, [role="group"][aria-label], [data-testid*="attachment" i], [data-file-citation-primary-file-id]'
      : 'user-query-file-carousel, [data-test-id="uploaded-file"]')) {
      throw new Error('The previous message has files that cannot be safely removed in this editor; nothing was edited');
    }
    const normalize = (value) => String(value || '').normalize('NFKC').replace(/\s+/g, ' ').trim();
    const digestFor = async (value) => {
      const digest = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(normalize(value)));
      return [...new Uint8Array(digest)].map((part) => part.toString(16).padStart(2, '0')).join('');
    };
    let hex = '';
    if (profile.name === 'chatgpt' && content.querySelectorAll) {
      const parts = content.querySelectorAll('[data-message-content-part], .whitespace-pre-wrap, .markdown');
      for (let index = 0; index < Math.min(parts.length, 32); index += 1) {
        const part = parts.item ? parts.item(index) : parts[index];
        hex = await digestFor(part?.textContent);
        if (hex === target.digest) break;
      }
    }
    if (hex !== target.digest) hex = await digestFor(content.textContent);
    if (hex !== target.digest) throw new Error('The previous ContextBridge message changed since it was sent; nothing was edited');
    if ([...document.querySelectorAll('textarea[aria-label="Nachricht bearbeiten"], textarea[aria-label="Edit message"], textarea[aria-label="Prompt bearbeiten"], textarea[aria-label="Edit prompt"]')].some(isVisible)) {
      throw new Error('A message is already being edited by the user; nothing was changed');
    }
    if ([...document.querySelectorAll('button[data-testid*="stop" i], button[aria-label*="stop" i], button[aria-label*="beenden" i], button[aria-label="Antwort stoppen"]')].some(isVisible)) {
      throw new Error('The provider still shows Stop; wait for it to finish before editing');
    }
    const editButton = profile.name === 'chatgpt'
      ? turn.querySelector('button[aria-label="Nachricht bearbeiten"], button[aria-label="Edit message"]')
      : turn.querySelector('[data-test-id="prompt-edit-button"] button, button[aria-label="Bearbeiten"], button[aria-label="Edit"]');
    if (!editButton || !isVisible(editButton)) throw new Error('The previous message has no available Edit control');
    await authorizeAction('edit');
    editButton.click();
    const editorSelector = profile.name === 'chatgpt'
      ? 'textarea[aria-label="Nachricht bearbeiten"], textarea[aria-label="Edit message"]'
      : 'textarea[aria-label="Prompt bearbeiten"], textarea[aria-label="Edit prompt"]';
    let editor = null;
    for (let attempt = 0; attempt < 20 && !editor; attempt += 1) {
      await wait(150);
      editor = turn.querySelector(editorSelector);
    }
    if (!editor || !isVisible(editor)) throw new Error('The Edit dialog did not open; no update was sent');
    setInput(editor, job.prompt);
    let update = null;
    const label = profile.name === 'chatgpt' ? /^(?:Senden|Send|Save)$/i : /^(?:Aktualisieren|Update)$/i;
    for (let attempt = 0; attempt < 20; attempt += 1) {
      if (String(editor.value || '').trim() !== String(job.prompt || '').trim()) throw new Error('The Edit dialog did not retain the new prompt; no update was sent');
      update = [...turn.querySelectorAll('button')].find((button) => label.test(visibleText(button)) && isVisible(button));
      if (update && !update.disabled && update.getAttribute('aria-disabled') !== 'true') break;
      await wait(150);
    }
    if (!update || update.disabled || update.getAttribute('aria-disabled') === 'true') throw new Error('The Edit update button stayed disabled; no update was sent');
    await authorizeAction('edit');
    update.click();
  };
  const clearIncompatibleGeminiTools = async (job) => {
    if (profile.name !== 'gemini') return false;
    const wantsImage = Number(job.output?.min_images || 0) > 0 || Boolean(job.metadata?.contextbridge_image_tool);
    const wantsMusic = Number(job.output?.min_media || 0) > 0 || Boolean(job.metadata?.contextbridge_music_tool);
    let cleared = false;
    for (let attempt = 0; attempt < 4; attempt += 1) {
      const composer = first(selectors.input)?.closest?.('[data-node-type="input-area"]');
      if (!composer) return cleared;
      const selected = [...composer.querySelectorAll('button[aria-label]')].find((button) => {
        const label = String(button.getAttribute('aria-label') || '');
        if (!/(?:auswahl von .+ aufheben|remove .+ selection|deselect .+|clear .+ tool)/i.test(label)) return false;
        return !((wantsImage && /(?:bild|image)/i.test(label)) || (wantsMusic && /(?:musik|music)/i.test(label)));
      });
      if (!selected) return cleared;
      selected.click();
      cleared = true;
      await wait(250);
    }
    throw new Error('Gemini kept an incompatible selected tool after clearing it');
  };
	const addFile = (element, encoded, mediaType, name) => {
    const bytes = Uint8Array.from(atob(encoded), (char) => char.charCodeAt(0));
    const extension = (mediaType || 'image/png').split('/')[1]?.replace(/[^a-z0-9]/gi, '') || 'png';
    const safeName = String(name || `contextbridge.${extension}`).split(/[\\/]/).pop().slice(0, 180) || `contextbridge.${extension}`;
    const file = new File([bytes], safeName, { type: mediaType || 'image/png' });
    const transfer = new DataTransfer();
    transfer.items.add(file);
    element.files = transfer.files;
    element.dispatchEvent(new Event('input', { bubbles: true }));
		element.dispatchEvent(new Event('change', { bubbles: true }));
		return safeName;
	};
	const compatibleFileInput = (element, mediaType, name) => {
		if (element?.type !== 'file') return false;
		const accept = String(element.accept || '').toLowerCase().trim();
		if (!accept || accept === '*' || accept === '*/*') return true;
		const mime = String(mediaType || '').toLowerCase().split(';')[0];
		const extension = String(name || '').toLowerCase().match(/\.([a-z0-9]+)$/)?.[1] || '';
		return accept.split(',').some((part) => {
			const token = part.trim();
			return token === '*' || token === '*/*' || token === mime
				|| (mime.includes('/') && token === `${mime.split('/')[0]}/*`)
				|| (extension && token === `.${extension}`)
				|| (extension === 'jpg' && token === '.jpeg')
				|| (extension === 'jpeg' && token === '.jpg');
		});
	};
	const uploadInput = async (mediaType, name) => {
		const fields = () => (selectors.file_input || []).flatMap((selector) => {
			try { return [...document.querySelectorAll(selector)]; } catch (_) { return []; }
		}).filter((element) => element?.type === 'file' && !element.webkitdirectory);
		const findCompatible = () => fields().find((element) => compatibleFileInput(element, mediaType, name));
		let field = findCompatible();
		if (field || profile.name !== 'gemini') return field;
		const image = String(mediaType || '').startsWith('image/');
		if (image && fields().length) return fields()[0];
		// Gemini mounts its hidden file fields only while Uploads & Tools is open.
		// An already-open menu must not be toggled closed.
		const trigger = [...document.querySelectorAll('button')].find((element) => isVisible(element)
			&& /uploads?\s*(?:&|and|und)\s*tools?|hochladen/i.test(String(element.getAttribute('aria-label') || '')));
		if (!trigger) return null;
		if (trigger.getAttribute('aria-expanded') !== 'true') trigger.click();
		for (let attempt = 0; attempt < 20 && !field; attempt += 1) {
			await wait(150);
			field = findCompatible();
		}
		// The Files picker in Gemini also handles images, but some builds list
		// document extensions only in its advisory HTML accept attribute.
		// Use that picker only for images and require a new composer preview below.
		return field || (image ? fields()[0] || null : null);
	};
	const composerAttachmentEvidence = (expectedName = '') => {
		const composer = first(selectors.input)?.closest?.('form, [data-node-type="input-area"]');
		if (!composer) return { count: 0, named: false, attached: false, signatures: [] };
		const attached = Boolean(composer.querySelector('[data-testid*="attachment" i], [data-test-id*="attachment" i], [data-testid*="file-thumbnail" i], .attachment-chip, .file-chip, file-preview'));
		const rawNodes = composer.querySelectorAll('img, [data-testid*="attachment" i], [data-test-id*="attachment" i], [data-testid*="file-thumbnail" i], .attachment-chip, .file-chip, file-preview');
		const normalizeName = (value) => String(value || '').split(/[\\/]/).pop().normalize('NFKC').toLowerCase().trim();
		const expected = normalizeName(expectedName);
		let evidenceText = '';
		const signatures = [];
		let count = 0;
		for (let index = Math.max(0, rawNodes.length - 64); index < rawNodes.length; index += 1) {
			const node = rawNodes.item ? rawNodes.item(index) : rawNodes[index];
			const hint = `${node?.getAttribute?.('aria-label') || ''} ${node?.getAttribute?.('alt') || ''} ${node?.getAttribute?.('title') || ''} ${node?.textContent || ''}`;
			const source = String(node?.currentSrc || node?.src || node?.getAttribute?.('src') || '');
			const wrapper = node?.matches?.('[data-testid*="attachment" i], [data-test-id*="attachment" i], [data-testid*="file-thumbnail" i], .attachment-chip, .file-chip, file-preview')
				|| node?.closest?.('[data-testid*="attachment" i], [data-test-id*="attachment" i], [data-testid*="file-thumbnail" i], .attachment-chip, .file-chip, file-preview');
			const imagePreview = node?.tagName === 'IMG' && (/^(?:blob:|data:)/i.test(source)
				|| /\.(?:png|jpe?g|webp|gif|svg)(?:\s|$)/i.test(hint));
			if (!wrapper && !imagePreview && node?.tagName) continue;
			count += 1;
			evidenceText += ` ${hint}`;
			const signature = `${node?.tagName || 'test-node'}|${node?.id || ''}|${node?.getAttribute?.('data-testid') || ''}|${node?.getAttribute?.('data-test-id') || ''}|${node?.className || ''}|${source}|${hint}`;
			if (signature) signatures.push(signature.slice(0, 1000));
		}
		return { count, named: Boolean(expected && normalizeName(evidenceText).includes(expected)), attached, signatures };
	};
  const wait = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
	const waitForComposerAttachment = async (before, expectedName, retainedEvidence = null) => {
		const supplied = Date.parse(jobDeadline || '');
		const cutoff = Math.min(Date.now() + 12000, Number.isFinite(supplied) ? supplied - 1000 : Date.now() + 12000);
		do {
			const current = composerAttachmentEvidence(expectedName);
			const retained = retainedEvidence
				? current.named || retainedEvidence.signatures?.some((signature) => current.signatures.includes(signature))
				: current.named || current.count > before.count || (!before.attached && current.attached);
			if (retained) return current;
			if (Date.now() >= cutoff) break;
			await wait(200);
		} while (Date.now() < cutoff);
		return null;
	};
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
	const geminiModeName = /^(?:gemini\s*)?(?:\d+(?:\.\d+)?\s*)?(?:flash|pro|thinking|schnell|erweitert)(?:[ -](?:lite|erweitert|advanced|preview))?$/i;
	const geminiModePicker = () => document.querySelector('bard-mode-switcher button[aria-haspopup]')
		|| [...document.querySelectorAll('button, [role="button"]')].filter(isVisible)
			.filter((element) => !element.closest?.('model-response, user-query, [role="menu"], [role="navigation"], nav'))
			.map((element) => {
				const hint = `${element.tagName || ''} ${element.id || ''} ${element.className || ''} ${element.getAttribute('aria-label') || ''} ${element.getAttribute('data-testid') || ''} ${element.parentElement?.tagName || ''} ${element.parentElement?.className || ''}`;
				const selected = /(?:derzeit ausgewählt|currently selected|selected)\s*:\s*(.+)$/i.exec(element.getAttribute('aria-label') || '')?.[1]?.trim() || '';
				const semantic = /(?:model|modell|mode|modus)[-_ ]?(?:switcher|selector|picker|menu)|(?:model|modell|mode|modus)auswahl/i.test(hint);
				return { element, score: (semantic ? 4 : 0) + (geminiModeName.test(selected) ? 4 : 0)
					+ (geminiModeName.test(visibleText(element)) ? 2 : 0) + (element.getAttribute('aria-haspopup') ? 2 : 0) };
			}).filter((item) => item.score >= 4).sort((a, b) => b.score - a.score)[0]?.element;
	const geminiCurrentMode = () => {
		const picker = geminiModePicker();
		const selected = picker?.getAttribute('aria-label')?.match(/(?:derzeit ausgewählt|currently selected|selected)\s*:\s*(.+)$/i)?.[1]?.trim() || '';
		return geminiModeName.test(selected) ? selected : geminiModeName.test(visibleText(picker)) ? visibleText(picker) : '';
	};
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
		const triggers = kind === 'model' && profile.name === 'gemini' ? [geminiModePicker()].filter(Boolean)
			: triggerSelectors.flatMap((selector) => { try { return [...document.querySelectorAll(selector)].filter(isVisible); } catch (_) { return []; } });
		const normalizedValue = (value) => normalizedWords(value).join(' ');
		const geminiModeLabel = (element) => {
			const primary = visibleText(element.querySelector?.('.picker-primary-text, .mode-name, .model-name'));
			const secondary = visibleText(element.querySelector?.('.picker-secondary-text'));
			if (primary) return `${primary} ${secondary}`.trim();
			return String(element.getAttribute('aria-label') || '').trim() || String(element.innerText || '').split('\n').map((part) => part.trim()).filter(Boolean)[0] || '';
		};
		const current = kind === 'model' && profile.name === 'gemini'
			? triggers.find(() => normalizedValue(geminiCurrentMode()) === normalizedValue(requested))
			: triggers.find((element) => {
				if (kind !== 'model') return preferenceMatches(element);
				const semantic = `${element.getAttribute('data-testid') || ''} ${element.getAttribute('aria-label') || ''}`;
				return (/model[-_ ]?(?:switcher|selector|picker|menu)|modellauswahl|modellmenü/i.test(semantic)
					|| /^(?:gpt[\s._-]*\d|astra\b|sol\b|terra\b|luna\b)/i.test(visibleText(element))) && preferenceMatches(element);
			});
		// A model label elsewhere in an older ChatGPT turn is not proof of the
		// current composer selection. Always inspect the composer menu itself.
		if (current && !(kind === 'model' && profile.name === 'chatgpt')) return kind === 'model' && profile.name === 'gemini' ? confirmGeminiMode(requested) : (visibleText(current) || requested);
		if (kind === 'model' && profile.name === 'chatgpt') {
			const composer = first(selectors.input)?.closest?.('[data-node-type="input-area"], form')
				|| document.querySelector?.('form[data-type="unified-composer"]') || document.querySelector?.('form');
			const composerMenus = [...(composer?.querySelectorAll?.('button[aria-haspopup="menu"]') || [])]
				.filter(isVisible).filter((element) => !/composer-plus|add.files|dateien.*hinzufügen/i.test(
					`${element.id || ''} ${element.getAttribute('data-testid') || ''} ${element.getAttribute('aria-label') || ''}`));
			const composerPills = [...document.querySelectorAll('button.__composer-pill[aria-haspopup="menu"]')].filter(isVisible);
			const modelControl = (element) => /reason|denk|effort|thinking|model|modell/i.test(
				`${visibleText(element)} ${element.getAttribute('aria-label') || ''} ${element.getAttribute('data-testid') || ''}`);
			const modelTrigger = composerPills.find(modelControl) || (composerPills.length === 1 ? composerPills[0] : null)
				|| composerMenus.find(modelControl) || (composerMenus.length === 1 ? composerMenus[0] : null);
			if (!modelTrigger) throw new Error('The model selector is not visible in this ChatGPT composer');
			const candidateSelector = 'button, [role="button"], [role="menuitem"], [role="menuitemradio"], [role="option"], [data-radix-collection-item], [aria-checked], [aria-selected], li';
			const alreadyOpen = modelTrigger.getAttribute('aria-expanded') === 'true';
			const before = new Set([...document.querySelectorAll(candidateSelector)].filter(isVisible));
			const candidates = () => [...document.querySelectorAll(candidateSelector)]
				.filter((element) => element !== modelTrigger && isVisible(element) && (alreadyOpen || !before.has(element)));
			const modelPattern = /^(?:GPT[\s._-]*\d+(?:\.\d+)?(?:[\s._-]*(?:Sol(?: Pro)?|Pro|Terra|Luna|Mini|Nano|Codex))?|Astra|Sol|Terra|Luna)$/i;
			const modelLabel = (element) => String(element?.innerText || element?.textContent || '')
				.split('\n').map((line) => line.replace(/\s+/g, ' ').trim())
				.find((line) => modelPattern.test(line)) || visibleText(element);
			const modelMatches = (element) => normalizedValue(modelLabel(element)) === normalizedValue(requested);
			const activate = async (element, opened) => {
				if (typeof PointerEvent === 'function') {
					element.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true, cancelable: true, button: 0, buttons: 1, pointerType: 'mouse', isPrimary: true }));
					await wait(100);
				}
				if (!opened() && typeof MouseEvent === 'function') {
					element.dispatchEvent(new MouseEvent('mousedown', { bubbles: true, cancelable: true, button: 0, buttons: 1 }));
					await wait(100);
				}
				if (!opened()) element.click();
			};
			const closeMenu = async () => {
				document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', code: 'Escape', bubbles: true }));
				await wait(100);
				if (modelTrigger.getAttribute('aria-expanded') === 'true') await activate(modelTrigger, () => modelTrigger.getAttribute('aria-expanded') !== 'true');
			};
			if (!alreadyOpen) await activate(modelTrigger, () => modelTrigger.getAttribute('aria-expanded') === 'true' || candidates().length > 0);
			let choices = [];
			for (let attempt = 0; attempt < 12; attempt++) {
				await wait(150);
				choices = candidates();
				if (choices.some(modelMatches)) break;
			}
			if (!choices.some(modelMatches)) {
				const versionAndEffort = /^(?:gpt[- ]?)?\d+(?:\.\d+)?\s+(?:sehr hoch|hoch|mittel|niedrig|sofort|very high|high|medium|low|instant|fast)(?:\s*[›>→])?$/i;
				const submenu = choices.find((element) => versionAndEffort.test(visibleText(element))
					|| (element.getAttribute('aria-haspopup') && /^(?:model|modell|modelle)$/i.test(visibleText(element))));
				if (submenu) {
					await activate(submenu, () => candidates().some((element) => modelPattern.test(modelLabel(element))));
					for (let attempt = 0; attempt < 12; attempt++) {
						await wait(150);
						choices = candidates();
						if (choices.some(modelMatches)) break;
					}
				}
			}
			const matching = choices.filter(modelMatches);
			const match = matching.find((element) => element.getAttribute('aria-checked') !== null
				|| element.getAttribute('aria-selected') !== null || /^(?:menuitemradio|menuitem|option)$/.test(element.getAttribute('role') || ''))
				|| matching[0];
			if (!match) {
				await closeMenu();
				throw new Error(`Requested model "${requested}" is not available in this chat (${choices.length} candidates, ${choices.filter((element) => modelPattern.test(modelLabel(element))).length} model choices, ${composerPills.includes(modelTrigger) ? 'pill' : 'form'} trigger)`);
			}
			if (match.disabled || match.getAttribute('aria-disabled') === 'true') {
				await closeMenu();
				throw new Error(`Requested model "${requested}" is disabled in this chat`);
			}
			const choice = match.closest?.('[role="menuitemradio"], [role="option"], [aria-checked]') || match;
			const selected = choice.getAttribute('aria-checked') === 'true' || choice.getAttribute('aria-selected') === 'true'
				|| choice.getAttribute('data-state') === 'checked'
				|| Boolean(choice.querySelector?.('svg use[href*="#check" i], svg[data-testid*="check" i], [data-state="checked"]'));
			if (selected) await closeMenu();
			else { choice.click(); await wait(350); }
			return modelLabel(match);
		}
		const trigger = triggers.find((element) => {
			const label = `${visibleText(element)} ${element.getAttribute('aria-label') || ''}`.toLowerCase();
			return kind === 'model' ? (profile.name === 'gemini' ? element === geminiModePicker() : /gpt|gemini|model|modell|astra|sol|terra|luna/.test(label)) : /reason|denk|effort|thinking|sofort|instant|hoch|high|pro|max/.test(label);
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
	const chooseMusicTool = async (input) => {
		if (profile.name !== 'gemini') throw new Error('Music creation requires a Gemini tab');
		const selectedMusic = () => first(selectors.input)?.closest?.('[data-node-type="input-area"]')?.querySelector?.(
			'button[aria-label*="Musik\u201c aufheben" i], button[aria-label*="Music selection" i]');
		if (selectedMusic()) return;
		const trigger = first(['button[aria-label*="Uploads & Tools" i]', 'button[aria-label*="Tools" i]', 'button[aria-label*="Werkzeuge" i]']);
		if (!trigger) throw new Error('Music creation tool menu is not available in this chat');
		const musicChoice = () => [...document.querySelectorAll('button[role="menuitemcheckbox"], [role="menuitem"], [role="option"]')]
			.filter(isVisible).find((element) => /(?:musik erstellen|create music|generate music)/i.test(visibleText(element)));
		// Gemini creates an animated CDK overlay. A fixed 400 ms sleep can miss
		// its entries; clicking the trigger again when already open closes it.
		let choice = musicChoice();
		if (!choice && trigger.getAttribute('aria-expanded') !== 'true') trigger.click();
		for (let attempt = 0; !choice && attempt < 14; attempt += 1) {
			await wait(250);
			choice = musicChoice();
		}
		if (!choice || choice.disabled || choice.getAttribute('aria-disabled') === 'true') {
			document.activeElement?.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', code: 'Escape', bubbles: true }));
			throw new Error('Music creation tool is not available in this chat');
		}
		if (choice.getAttribute('aria-checked') !== 'true') choice.click();
		for (let attempt = 0; attempt < 10; attempt += 1) {
			if (selectedMusic()) return;
			await wait(200);
		}
		throw new Error('Music creation tool did not remain selected in this chat');
	};
	const cleanFileName = (value, fallback) => {
		const clean = String(value || '').split(/[\\/]/).pop().replace(/[\u0000-\u001f<>:"|?*]/g, '-').trim().slice(0, 180);
		return clean && clean !== '.' ? clean : fallback;
	};
	const extensionFor = (mediaType) => ({
		'image/png': 'png', 'image/jpeg': 'jpg', 'image/webp': 'webp', 'image/gif': 'gif',
		'video/mp4': 'mp4', 'video/webm': 'webm', 'audio/mpeg': 'mp3', 'audio/wave': 'wav',
		'audio/wav': 'wav', 'audio/ogg': 'ogg', 'audio/webm': 'webm',
		'image/svg+xml': 'svg', 'application/pdf': 'pdf', 'application/zip': 'zip',
		'application/json': 'json', 'text/csv': 'csv', 'text/plain': 'txt'
	}[String(mediaType || '').toLowerCase()] || 'bin');
	const mediaTypeFor = (url, fallback) => {
		let value = String(url || '').split(/[?#]/)[0].toLowerCase();
		try { value = new URL(url, location.href).searchParams.get('filename')?.toLowerCase() || value; } catch (_) {}
		if (value.endsWith('.png')) return 'image/png';
		if (/\.jpe?g$/.test(value)) return 'image/jpeg';
		if (value.endsWith('.webp')) return 'image/webp';
		if (value.endsWith('.gif')) return 'image/gif';
		if (value.endsWith('.svg')) return 'image/svg+xml';
		if (value.endsWith('.mp4')) return 'video/mp4';
		if (value.endsWith('.webm')) return 'video/webm';
		if (value.endsWith('.mp3')) return 'audio/mpeg';
		if (value.endsWith('.wav')) return 'audio/wave';
		if (value.endsWith('.ogg')) return 'audio/ogg';
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
		const readBoundedResponse = async (response, remaining) => {
		const declared = Number(response.headers?.get?.('content-length'));
		if (Number.isFinite(declared) && declared > remaining) throw new Error('artifact exceeds transfer limit');
		if (!response.body?.getReader) throw new Error('artifact response is not streamable');
		const reader = response.body.getReader();
		const chunks = [];
		let size = 0;
		while (true) {
			const { done, value } = await reader.read();
			if (done) break;
			if (!value?.byteLength) continue;
			if (value.byteLength > remaining - size) {
				await reader.cancel().catch(() => {});
				throw new Error('artifact exceeds transfer limit');
			}
			chunks.push(value);
			size += value.byteLength;
		}
		if (!size) throw new Error('empty artifact');
		const bytes = new Uint8Array(size);
		let offset = 0;
		for (const chunk of chunks) {
			bytes.set(chunk, offset);
			offset += chunk.byteLength;
		}
			return bytes;
		};
		const artifactFetchPolicy = (rawURL, provenance = 'public_dom') => {
			const resource = new URL(rawURL, location.href);
			if (resource.protocol === 'data:') return { url: resource.href, credentials: 'omit' };
			if (resource.protocol === 'blob:') {
				if (resource.origin !== location.origin) throw new Error('cross-origin blob artifact');
				return { url: resource.href, credentials: 'omit' };
			}
			if (resource.protocol !== 'https:') throw new Error('unsupported artifact URL');
			const citation = provenance === 'chatgpt_file_citation' && profile.name === 'chatgpt'
				&& location.hostname === 'chatgpt.com' && resource.origin === location.origin
				&& /^\/backend-api\/files\/file_[a-z0-9]+\/download$/i.test(resource.pathname);
			const artifactPreview = provenance === 'chatgpt_artifact_preview' && profile.name === 'chatgpt'
				&& location.hostname === 'chatgpt.com' && resource.origin === location.origin;
			if (String(provenance || '').startsWith('chatgpt_') && !citation && !artifactPreview) {
				throw new Error('invalid authenticated artifact provenance');
			}
			if (resource.origin === location.origin) {
				return { url: resource.href, credentials: citation || artifactPreview ? 'include' : 'omit' };
			}
			const auditedGeminiMedia = profile.name === 'gemini'
				&& resource.origin === 'https://contribution.usercontent.google.com';
			if (!auditedGeminiMedia) throw new Error('cross-origin artifact URL');
			return { url: resource.href, credentials: 'omit' };
		};
	const collectArtifacts = async (responseElement, spec) => {
		if (!spec?.artifacts || !responseElement) return [];
		const maxBytes = Math.max(1024, Math.min(Number(spec.max_artifact_bytes) || 12 * 1024 * 1024, 12 * 1024 * 1024));
		const candidates = [];
		const seen = new Map();
		const provenanceRank = (value) => ['chatgpt_file_citation', 'chatgpt_artifact_preview'].includes(value) ? 2 : 1;
		const add = (url, name, mediaType, provenance = 'public_dom', transfer = true) => {
			url = String(url || '').trim();
			if (!url || !/^(https:|blob:|data:)/i.test(url)) return;
			const previous = seen.get(url);
			if (previous !== undefined) {
				if (provenanceRank(provenance) > provenanceRank(candidates[previous].provenance)) {
					candidates[previous] = { url, name, mediaType, provenance, transfer };
				}
				return;
			}
			seen.set(url, candidates.length);
			candidates.push({ url, name, mediaType, provenance, transfer });
		};
		const responseImages = responseElement.querySelectorAll('img');
		for (let index = Math.max(0, responseImages.length - 128); index < responseImages.length; index += 1) {
			const image = responseImages.item ? responseImages.item(index) : responseImages[index];
			if (image.getAttribute('aria-hidden') === 'true') continue;
			if ((image.naturalWidth && image.naturalWidth < 128) || (image.naturalHeight && image.naturalHeight < 128)) continue;
			const source = image.currentSrc || image.src;
			const mediaType = mediaTypeFor(source, 'image/png');
			let name = '';
			try { name = new URL(source, location.href).pathname.split('/').pop(); } catch (_) {}
			if (!/\.[a-z0-9]{2,5}$/i.test(name)) name = `image-${candidates.length + 1}.${extensionFor(mediaType)}`;
			add(source, cleanFileName(name, `image-${candidates.length + 1}.${extensionFor(mediaType)}`), mediaType);
		}
		const responseMedia = responseElement.querySelectorAll('video[src], audio[src], video source[src], audio source[src]');
		for (let index = Math.max(0, responseMedia.length - 128); index < responseMedia.length; index += 1) {
			const media = responseMedia.item ? responseMedia.item(index) : responseMedia[index];
			const source = media.currentSrc || media.src || media.getAttribute('src');
			if (!source) continue;
			const mediaType = mediaTypeFor(source, media.getAttribute('type') || (media.closest('audio') ? 'audio/mpeg' : 'video/mp4'));
			let name = '';
			try { const parsed = new URL(source, location.href); name = parsed.searchParams.get('filename') || parsed.pathname.split('/').pop(); } catch (_) {}
			if (!/\.[a-z0-9]{2,5}$/i.test(name)) name = `media-${candidates.length + 1}.${extensionFor(mediaType)}`;
			add(source, cleanFileName(name, `media-${candidates.length + 1}.${extensionFor(mediaType)}`), mediaType);
		}
		// ChatGPT may render a generated image as a file card with only a
		// button, not an img or href. Opening that card can expose a preview
		// image; only inspect the new preview, never unrelated page images.
		if (profile.name === 'chatgpt' && Number(spec.min_images || 0) > 0) {
			const cards = [];
			const cardNodes = responseElement.querySelectorAll('button[aria-label]');
			for (let index = Math.max(0, cardNodes.length - 128); index < cardNodes.length && cards.length < Math.min(12, Number(spec.min_images) || 1); index += 1) {
				const button = cardNodes.item ? cardNodes.item(index) : cardNodes[index];
				if (button.closest('[class*="artifact-row"]') && /\.(?:png|jpe?g|webp|gif)$/i.test(button.getAttribute('aria-label') || '')) cards.push(button);
			}
				for (const card of cards) {
					const before = new Map();
					const existingPreviews = document.querySelectorAll('dialog img, [role="dialog"] img, [data-testid*="preview" i] img');
					for (let index = Math.max(0, existingPreviews.length - 64); index < existingPreviews.length; index += 1) {
						const image = existingPreviews.item ? existingPreviews.item(index) : existingPreviews[index];
						before.set(image, image.currentSrc || image.src);
					}
					const name = cleanFileName(card.getAttribute('aria-label'), `image-${candidates.length + 1}.png`);
					await authorizeAction('observe');
					card.click();
				let previews = [];
				for (let attempt = 0; attempt < 16 && !previews.length; attempt += 1) {
					await wait(250);
					const previewNodes = document.querySelectorAll('dialog img, [role="dialog"] img, [data-testid*="preview" i] img');
					previews = [];
					for (let index = previewNodes.length - 1; index >= Math.max(0, previewNodes.length - 64); index -= 1) {
						const image = previewNodes.item ? previewNodes.item(index) : previewNodes[index];
						if (isVisible(image) && image.naturalWidth >= 128 && image.naturalHeight >= 128
								&& (!before.has(image) || before.get(image) !== (image.currentSrc || image.src))) previews.push(image);
					}
				}
				for (const preview of previews) add(preview.currentSrc || preview.src, name, mediaTypeFor(name, 'image/png'), 'chatgpt_artifact_preview');
				const dialog = previews[0]?.closest?.('dialog, [role="dialog"]')
					|| (() => {
						const dialogs = document.querySelectorAll('dialog, [role="dialog"]');
						for (let index = dialogs.length - 1; index >= Math.max(0, dialogs.length - 32); index -= 1) {
							const candidate = dialogs.item ? dialogs.item(index) : dialogs[index];
							if (isVisible(candidate)) return candidate;
						}
						return null;
					})();
				if (dialog) {
					const close = dialog?.querySelector('button[aria-label*="close" i], button[aria-label*="schlie" i]');
					if (close) close.click();
					else document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', code: 'Escape', bubbles: true }));
				}
			}
		}
		const responseAnchors = responseElement.querySelectorAll('a[href]');
		for (let index = Math.max(0, responseAnchors.length - 128); index < responseAnchors.length; index += 1) {
			const anchor = responseAnchors.item ? responseAnchors.item(index) : responseAnchors[index];
			const href = anchor.href;
			const label = `${anchor.download || ''} ${anchor.getAttribute('aria-label') || ''} ${visibleText(anchor)}`.toLowerCase();
			const path = (() => { try { return new URL(href, location.href).pathname; } catch (_) { return ''; } })();
			if (!anchor.hasAttribute('download') && !/download|herunterladen|save|speichern/.test(label) && !/\.(pdf|zip|json|csv|txt|md|docx|xlsx|pptx|png|jpe?g|webp|gif|mp4|webm|mp3|wav|ogg)(?:$|[?#])/i.test(href)) continue;
			const mediaType = mediaTypeFor(href);
			add(href, cleanFileName(anchor.download || path.split('/').pop(), `file-${candidates.length + 1}.${extensionFor(mediaType)}`), mediaType, 'generic_link', false);
		}
		const responseCitations = responseElement.querySelectorAll('[data-file-citation-primary-file-id]');
		for (let index = Math.max(0, responseCitations.length - 128); index < responseCitations.length; index += 1) {
			const citation = responseCitations.item ? responseCitations.item(index) : responseCitations[index];
			const fileId = String(citation.getAttribute('data-file-citation-primary-file-id') || '');
			if (!/^file_[a-z0-9]+$/i.test(fileId)) continue;
			const label = citation.querySelector('p')?.textContent || citation.querySelector('button')?.textContent || '';
			const endpoint = `${location.origin}/backend-api/files/${encodeURIComponent(fileId)}/download`;
			add(endpoint, cleanFileName(label, `file-${candidates.length + 1}.bin`), mediaTypeFor(label), 'chatgpt_file_citation');
		}
		const artifacts = [];
		let total = 0;
			const candidatePriority = (candidate) => candidate.provenance === 'chatgpt_file_citation' ? 4
				: candidate.provenance === 'public_dom' ? 3
					: candidate.provenance === 'chatgpt_artifact_preview' ? 2 : 1;
			const orderedCandidates = candidates.map((candidate, index) => ({ candidate, index }))
				.sort((left, right) => candidatePriority(right.candidate) - candidatePriority(left.candidate) || left.index - right.index)
				.map((entry) => entry.candidate);
			for (const candidate of orderedCandidates.slice(0, 12)) {
				// A generated-image card often opens the exact same bytes already
				// exposed by the response img. Do not transfer both representations.
				if (candidate.provenance === 'chatgpt_artifact_preview'
						&& artifacts.some((item) => item.data_base64 && String(item.media_type || '').startsWith('image/'))) continue;
				const artifact = { name: candidate.name, media_type: candidate.mediaType };
				if (!candidate.transfer) {
					artifact.url = candidate.url;
					artifact.contextbridge_provenance = candidate.provenance;
					artifacts.push(artifact);
					continue;
				}
				try {
					const policy = artifactFetchPolicy(candidate.url, candidate.provenance);
					const controller = new AbortController();
					const jobRemaining = (Date.parse(jobDeadline || '') || Date.now() + 15000) - Date.now();
					const timeout = setTimeout(() => controller.abort(), Math.max(1000, Math.min(15000, jobRemaining)));
					let response;
					try {
						response = await fetch(policy.url, { credentials: policy.credentials, redirect: 'error', signal: controller.signal });
						if (response.url) {
							const redirected = artifactFetchPolicy(response.url, candidate.provenance);
							if (redirected.credentials !== policy.credentials) throw new Error('artifact redirect changed credential policy');
						}
						if (!response.ok) throw new Error(`HTTP ${response.status}`);
						const bytes = await readBoundedResponse(response, maxBytes - total);
						const responseType = String(response.headers?.get?.('content-type') || '').split(';')[0].trim();
						artifact.media_type = mediaTypeFor(candidate.url, responseType || candidate.mediaType);
						artifact.size = bytes.length;
						artifact.sha256 = await digestHex(bytes);
						artifact.data_base64 = bytesToBase64(bytes);
						total += bytes.length;
					} finally {
						clearTimeout(timeout);
					}
				} catch (_) {
				if (/^https:/i.test(candidate.url)) {
					artifact.url = candidate.url;
					if (['chatgpt_file_citation', 'chatgpt_artifact_preview'].includes(candidate.provenance)) {
						artifact.contextbridge_provenance = candidate.provenance;
					}
				}
			}
			if (artifact.sha256 && artifacts.some((item) => item.sha256 === artifact.sha256)) {
				total = Math.max(0, total - Number(artifact.size || 0));
				continue;
			}
			if (artifact.data_base64 || artifact.url) artifacts.push(artifact);
		}
		const codeNodes = responseElement.querySelectorAll('pre code');
		for (let index = Math.max(0, codeNodes.length - Math.max(0, 12 - artifacts.length)); index < codeNodes.length; index += 1) {
			const code = codeNodes.item ? codeNodes.item(index) : codeNodes[index];
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
	      requireExpectedConversation();
	      await authorizeAction('observe');
	      let input = first(selectors.input);
      if (!input) throw new Error('Prompt input was not found');
      const beforeSnapshot = responseTail(selectors.response);
      const before = beforeSnapshot.values;
	      const persistedBaselineIdentity = String(job.metadata?.contextbridge_baseline_response_identity || '');
	      const beforeIdentities = persistedBaselineIdentity
	        ? new Set([persistedBaselineIdentity]) : new Set(before.map(responseIdentity).filter(Boolean));
      const beforeUserTurns = document.querySelectorAll(profile.name === 'gemini' ? 'user-query' : '[data-turn="user"]').length;
      const previousElement = before.length ? before[before.length - 1] : null;
      // An explicitly supplied empty baseline is meaningful: on resume the
      // finished answer may already be visible. Falling back to that answer
      // as the baseline would make the owned turn look unchanged forever.
      const hasBaseline = Object.prototype.hasOwnProperty.call(job.metadata || {}, 'contextbridge_baseline_text');
      const previousText = hasBaseline ? String(job.metadata.contextbridge_baseline_text || '')
        : (before.length ? responseText(before[before.length - 1]) : '');
	      const previousIdentity = persistedBaselineIdentity || responseIdentity(previousElement);
	      const persistedBaselineCount = Number(job.metadata?.contextbridge_baseline_response_count);
	      const previousResponseCount = Number.isInteger(persistedBaselineCount) && persistedBaselineCount >= 0
        ? persistedBaselineCount : beforeSnapshot.count;
	      const baselineTextDigest = /^[a-f0-9]{64}$/.test(String(job.metadata?.contextbridge_baseline_text_digest || ''))
	        ? String(job.metadata.contextbridge_baseline_text_digest) : '';
      const resumeOnly = Boolean(job.metadata?.contextbridge_resume_only);
	  let confirmedGeminiImageUpload = false;
	  let retainedSubmittedPrompt = false;
	  let clickedSendForThisPrompt = false;
	  let uploadedAttachmentProof = null;
	  const initialFailure = providerError(null, false, beforeUserTurns);
	  if (initialFailure?.blocking) {
	    resolve({ ok: false, error: initialFailure.message, code: initialFailure.code, retryable: initialFailure.retryable });
	    return;
	  }
	  if (!resumeOnly && profile.name === 'chatgpt' && chatGPTStopVisible()) {
	    throw new Error('ChatGPT still shows Stop; the previous generation may be active and no new prompt was typed');
	  }
	  const chooseWithFallback = async (kind, requested, alternatives) => {
		const choices = [requested, ...(Array.isArray(alternatives) ? alternatives.slice(0, 4) : [])]
			.map((value) => String(value || '').trim()).filter((value, index, all) => value && all.indexOf(value) === index);
		for (let index = 0; index < choices.length; index += 1) {
			try { return await choosePreference(kind, choices[index]); }
			catch (error) {
				// No fallback for missing selectors, uncertain post-selection state,
				// timeouts, or a submitted prompt. These are not proof of absence.
				if (index === choices.length - 1 || !new RegExp(`^Requested ${kind} "[^"]+" is (?:not available|disabled) in this chat`, 'i').test(String(error?.message || ''))) throw error;
			}
		}
		return '';
	  };
	  const selectedModel = !resumeOnly && job.model ? await chooseWithFallback('model', job.model, job.metadata?.contextbridge_model_fallbacks) : '';
	  const selectedReasoning = !resumeOnly && job.reasoning ? await chooseWithFallback('reasoning', job.reasoning, job.metadata?.contextbridge_reasoning_fallbacks) : '';
	  const clearedGeminiTool = !resumeOnly && !editTarget && await clearIncompatibleGeminiTools(job);
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
	  if (!resumeOnly && !editTarget && job.metadata?.contextbridge_image_tool) {
		await chooseImageTool(input);
		input = first(selectors.input);
		if (!input) throw new Error('Prompt input disappeared after selecting the image tool');
	  }
		  if (!resumeOnly && !editTarget && job.metadata?.contextbridge_music_tool) {
			await chooseMusicTool(input);
		input = first(selectors.input);
			if (!input) throw new Error('Prompt input disappeared after selecting the music tool');
		  }
		  if (!resumeOnly) {
			requireExpectedConversation();
			await authorizeAction('observe');
			requireExpectedConversation();
		  }
	      if (!resumeOnly && !editTarget && job.image_base64) {
        const mediaType = String(job.image_media_type || 'image/png');
        const name = `contextbridge-image.${extensionFor(mediaType)}`;
        const fileInput = await uploadInput(mediaType, name);
        if (!fileInput) throw new Error('No compatible image upload input appeared; the prompt was not sent');
        if (fileInput.files?.length) throw new Error('An unsent attachment is already present; the prompt was not sent');
	        const beforeAttachment = composerAttachmentEvidence(name);
	        if (beforeAttachment.attached || beforeAttachment.named || beforeAttachment.count > 0) throw new Error('An unsent attachment is already present; the prompt was not sent');
	        await authorizeAction('upload');
	        const uploadedName = addFile(fileInput, job.image_base64, mediaType, name);
	        const evidence = await waitForComposerAttachment(beforeAttachment, uploadedName);
	        if (!evidence) {
	          throw new Error(`${profile.name} did not show the uploaded image in its composer; the prompt was not sent`);
	        }
	        uploadedAttachmentProof = { before: beforeAttachment, evidence, name: uploadedName, kind: 'image' };
	        if (profile.name === 'gemini') confirmedGeminiImageUpload = true;
      }
      if (!resumeOnly && !editTarget && job.metadata?.contextbridge_input_file) {
        const attachment = job.metadata.contextbridge_input_file;
        if (job.image_base64) throw new Error('Only one carried artifact may be attached to a follow-up');
        if (!attachment.data_base64 || String(attachment.data_base64).length > 12 * 1024 * 1024) throw new Error('Carried file bytes are missing or too large');
        const fileInput = await uploadInput(attachment.media_type, attachment.name);
	        if (!fileInput) throw new Error('No compatible file input was found; the next prompt was not sent');
	        if (fileInput.files?.length) throw new Error('An unsent attachment is already present; the prompt was not sent');
	        const beforeAttachment = composerAttachmentEvidence(attachment.name);
	        if (beforeAttachment.attached || beforeAttachment.named || beforeAttachment.count > 0) throw new Error('An unsent attachment is already present; the prompt was not sent');
	        await authorizeAction('upload');
	        const uploadedName = addFile(fileInput, attachment.data_base64, attachment.media_type, attachment.name);
	        const evidence = await waitForComposerAttachment(beforeAttachment, uploadedName);
	        if (!evidence) {
	          throw new Error(`${profile.name} did not show the uploaded file in its composer; the prompt was not sent`);
	        }
	        uploadedAttachmentProof = { before: beforeAttachment, evidence, name: uploadedName, kind: 'file' };
      }
      if (!resumeOnly) {
        if (editTarget) {
          if (String(input.value || input.innerText || input.textContent || '').trim()) {
            throw new Error('Prompt editor contains another draft; the prior message was not edited');
          }
          const scope = input.closest?.('form, [data-node-type="input-area"]');
          if (scope && ([...scope.querySelectorAll('input[type="file"]')].some((field) => field.files?.length)
            || scope.querySelector('[data-testid*="attachment" i], [data-test-id*="attachment" i], .attachment-chip, .file-chip'))) {
            throw new Error('Prompt editor contains an unsent attachment; the prior message was not edited');
          }
          await editOwnedMessage(editTarget);
        } else {
          const editorText = (element) => String(element?.value || element?.innerText || element?.textContent || '');
          const normalized = (value) => String(value || '').replace(/[\u200b-\u200d\ufeff]/g, '').replace(/\s+/g, ' ').trim();
	          const expected = normalized(job.prompt);
	          const draft = normalized(editorText(input));
	          if (draft && draft !== expected) throw new Error('Prompt editor contains another draft');
	          await authorizeAction('observe');
	          requireExpectedConversation();
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
          retainedSubmittedPrompt = true;
          input.setAttribute?.('data-contextbridge-owned-job', String(job.id || ''));
          let submit = sendControl();
          for (let attempt = 0; (!submit || submit.disabled || submit.getAttribute('aria-disabled') === 'true') && attempt < 20; attempt += 1) {
            await wait(150);
            submit = sendControl();
          }
	          if (uploadedAttachmentProof && !await waitForComposerAttachment(uploadedAttachmentProof.before, uploadedAttachmentProof.name, uploadedAttachmentProof.evidence)) {
	            throw new Error(`${profile.name} no longer shows the uploaded ${uploadedAttachmentProof.kind} in its composer; the prompt was not sent`);
	          }
          if (submit && !submit.disabled && submit.getAttribute('aria-disabled') !== 'true') {
            const sendDeadline = Date.parse(jobDeadline || '');
	            if (Number.isFinite(sendDeadline) && Date.now() >= sendDeadline) {
	              throw new Error('Browser job deadline expired before Send; no prompt was submitted');
	            }
	            await authorizeAction('send');
	            submit.click();
            clickedSendForThisPrompt = true;
          } else if (submit) {
            throw new Error('Send button stayed disabled after filling the prompt');
          } else {
            if (profile.name === 'chatgpt' && chatGPTStopVisible()) {
              throw new Error('ChatGPT still shows Stop; the new prompt was not submitted');
            }
	            if (profile.name === 'gemini') throw new Error('Send button is not visible after filling the prompt');
	            await authorizeAction('send');
	            input.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', code: 'Enter', bubbles: true }));
            input.dispatchEvent(new KeyboardEvent('keyup', { key: 'Enter', code: 'Enter', bubbles: true }));
          }
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
      let sawActiveGeneration = false;
      let lastBusyAt = Date.now();
      let lastPercent = 0;
      let progressChangedAt = Date.now();
      let lastReadyImages = 0;
      let readyImagesSince = 0;
      const submittedAt = Date.now();
      const needsGeminiImageProof = profile.name === 'gemini' && Boolean(job.image_base64);
      const normalizedPrompt = String(job.prompt || '').normalize('NFKC').replace(/\s+/g, ' ').trim();
      const submittedGeminiTurn = () => {
        const turns = document.querySelectorAll('user-query');
        if (!resumeOnly && turns.length !== beforeUserTurns + 1) return null;
        const turn = turns.length ? (turns.item ? turns.item(turns.length - 1) : turns[turns.length - 1]) : null;
        const content = turn?.querySelector?.('[id^="user-query-content-"]') || turn;
        const text = String(content?.textContent || '').normalize('NFKC').replace(/\s+/g, ' ').trim();
        if (normalizedPrompt && text === normalizedPrompt) return turn;
        // Gemini currently renders image-only user turns without the prompt
        // text in DOM. Accept that shape only after this same automation has
        // verified the attachment preview, retained the exact editor text,
        // clicked Send, and observed exactly one new turn. The response must
        // still follow that turn; resume/reload never uses this weaker proof.
        return needsGeminiImageProof && !resumeOnly && !text
          && confirmedGeminiImageUpload && retainedSubmittedPrompt && clickedSendForThisPrompt ? turn : null;
      };
	      let nextLeaseCheck = Date.now() + 5000;
	      while (Date.now() < deadline) {
	        await wait(650);
        if (leaseContext && (Date.now() >= nextLeaseCheck
            || (activeExpectedConversationURL && location.href !== activeExpectedConversationURL))) {
          await authorizeAction('observe');
          nextLeaseCheck = Date.now() + 5000;
        }
        requireExpectedConversation();
        const responseSnapshot = responseTail(selectors.response);
        const responses = responseSnapshot.values;
        let latestElement = responses.length ? responses[responses.length - 1] : null;
        if (needsGeminiImageProof) {
          const turn = submittedGeminiTurn();
          if (!turn) {
            if (Date.now() - submittedAt >= 15000) {
              resolve({ ok: false, error: 'Gemini did not show the image-job prompt in a new user turn; no assistant answer was accepted', code: 'browser_submit_unavailable' });
              return;
            }
            continue;
          }
          const scope = turn.closest?.('.conversation-container');
          if (scope) {
            const candidates = scope.querySelectorAll('model-response');
            latestElement = null;
            for (let index = candidates.length - 1; index >= Math.max(0, candidates.length - 64); index -= 1) {
              const candidate = candidates.item ? candidates.item(index) : candidates[index];
              if (typeof turn.compareDocumentPosition === 'function' && Boolean(turn.compareDocumentPosition(candidate) & 4)) {
                latestElement = candidate;
                break;
              }
            }
          } else {
            latestElement = null;
            for (let index = responses.length - 1; index >= 0; index -= 1) {
              const candidate = responses[index];
              if (typeof turn.compareDocumentPosition === 'function' && Boolean(turn.compareDocumentPosition(candidate) & 4)) {
                latestElement = candidate;
                break;
              }
            }
          }
          if (!latestElement) continue;
        }
        const latest = responseText(latestElement);
		const readyImages = boundedCount(latestElement, 'img',
			(image) => image.complete && image.naturalWidth >= 128 && image.naturalHeight >= 128);
		if (readyImages !== lastReadyImages) {
			lastReadyImages = readyImages;
			readyImagesSince = Date.now();
		}
		const latestIdentity = responseIdentity(latestElement);
		const latestDigest = baselineTextDigest ? await digestHex(new TextEncoder().encode(latest)) : '';
		const changedResponse = responseSnapshot.count > previousResponseCount
			|| (baselineTextDigest ? latestDigest !== baselineTextDigest : latest !== previousText)
			|| Boolean(previousIdentity && latestIdentity && latestIdentity !== previousIdentity);
        const fallbackNotice = latestElement?.closest?.('.conversation-container')?.querySelector?.('peak-hour-fallback-disclaimer');
        if (profile.name === 'gemini' && geminiModeFamily(selectedModel || job.model) === 'pro' && changedResponse && isVisible(fallbackNotice)) {
          resolve({ ok: false, error: 'Gemini used another model during peak demand; requested Pro output was not accepted', code: 'browser_model_unavailable', retryable: true });
          return;
        }
        const state = pageState();
        sawActiveGeneration = sawActiveGeneration || state.busyReasons.some((reason) => reason !== 'aria_busy');
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
			resolve({ ok: false, error: 'Generation ended without a new assistant turn; reloading once when enabled to recover the conversation', code: 'missing_response_after_generation', recoverable: job.metadata?.contextbridge_auto_reload !== false });
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
        if (providerFailure && (!busy || providerFailure.blocking)) {
			resolve({ ok: false, error: providerFailure.message, code: providerFailure.code, retryable: providerFailure.retryable });
			return;
		}
		const plainTextJob = String(job.output?.mode || '').toLowerCase() === 'text'
			&& !Number(job.output?.min_artifacts || 0) && !Number(job.output?.min_images || 0) && !Number(job.output?.min_media || 0)
			&& !job.metadata?.contextbridge_image_tool && !job.metadata?.contextbridge_music_tool;
		const staleStop = state.busyReasons.includes('stop_button')
			&& state.busyReasons.every((reason) => reason === 'stop_button' || reason === 'aria_busy');
		// Background ChatGPT tabs can keep Stop mounted long after plain text is
		// visible. Wake only our auto-created tab after a stable new answer;
		// the caller verifies ownership before foregrounding or reloading it.
		const staleStopWait = profile.name === 'gemini' ? 120000 : 30000;
		if (profile.name === 'chatgpt' && plainTextJob && !resumeOnly && !editTarget
			&& job.metadata?.contextbridge_auto_reload !== false && staleStop && state.stopDisabled
			&& !changedResponse && state.inputReady && document.querySelectorAll('[data-turn="user"]').length > beforeUserTurns
			&& Date.now() - submittedAt >= 105000) {
			resolve({ ok: false, error: 'ChatGPT accepted the prompt but kept a disabled Stop without a new answer; reloading once to check the turn', code: 'stalled_response', recoverable: true });
			return;
		}
		if ((profile.name === 'chatgpt' || profile.name === 'gemini') && plainTextJob && !resumeOnly
			&& job.metadata?.contextbridge_auto_reload !== false && staleStop
			&& changedResponse && stableText && stableSince > 0 && state.inputReady
			&& Date.now() - stableSince >= staleStopWait) {
			resolve({ ok: false, error: `${profile.name} kept Stop visible after unchanged text; reloading once to verify the finished turn`, code: 'stalled_response', recoverable: true });
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
		const imageCards = latestElement && profile.name === 'chatgpt' ? boundedCount(latestElement, 'button[aria-label]',
			(button) => button.closest('[class*="artifact-row"]') && /\.(?:png|jpe?g|webp|gif)$/i.test(button.getAttribute('aria-label') || '')) : 0;
		const artifactCount = job.output?.artifacts && latestElement
			? boundedCount(latestElement, 'img', (image) => (!image.naturalWidth || image.naturalWidth >= 128) && (!image.naturalHeight || image.naturalHeight >= 128))
				+ latestElement.querySelectorAll('a[download], pre code, [data-file-citation-primary-file-id], video[src], audio[src]').length + imageCards
			: 0;
		const imageCount = latestElement ? boundedCount(latestElement, 'img',
			(image) => image.getAttribute('aria-hidden') !== 'true' && image.naturalWidth >= 128 && image.naturalHeight >= 128) + imageCards : 0;
		const mediaCount = latestElement ? latestElement.querySelectorAll('video[src], audio[src]').length : 0;
		const stableValue = latest || (artifactCount ? `artifact:${artifactCount}` : '');
        if (!stableValue || !changedResponse) continue;
        if (editTarget && !sawActiveGeneration && responseSnapshot.count <= beforeSnapshot.count
          && (!latestIdentity || beforeIdentities.has(latestIdentity))) continue;
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
		  const mediaMissing = job.output?.min_media > mediaCount;
		  if ((filesMissing || imagesMissing || mediaMissing) && Date.now() - stableSince < 15000) continue;
		  const artifacts = await collectArtifacts(latestElement, job.output || {});
		  const confirmedModel = profile.name === 'gemini' && (selectedModel || job.model) && !['auto', 'default'].includes(String(selectedModel || job.model).toLowerCase())
			? confirmGeminiMode(selectedModel || job.model) : selectedModel;
		  resolve({ ok: true, text: latest, artifacts, selected_model: confirmedModel, selected_reasoning: selectedReasoning,
		    submitted_prompt_verified: needsGeminiImageProof && Boolean(submittedGeminiTurn()) });
          return;
        }
      }
      resolve({ ok: false, error: 'Timed out while waiting for a stable response', code: 'browser_timeout', recoverable: false, percent: lastPercent });
      return;
    } catch (error) {
	      const message = error.message || String(error);
	      const failureRules = [
	        [/local ContextBridge service could not verify/i, 'browser_bridge_unavailable'],
	        [/browser job lease|lease cannot be verified/i, 'browser_lease_lost'],
	        [/conversation URL|expected conversation URL/i, 'browser_session_changed'],
	        [/requested model|model selector/i, 'browser_model_unavailable'],
	        [/ChatGPT still shows Stop/i, 'browser_provider_busy'],
	        [/edit mode|previous ContextBridge message|previous message|Edit dialog|Edit update button|message is already being edited|provider still shows Stop|prompt editor contains an unsent attachment/i, 'browser_edit_unavailable'],
	        [/requested reasoning|reasoning selector/i, 'browser_reasoning_unavailable'],
	        [/deadline expired before Send/i, 'browser_timeout'],
	        [/send button stayed disabled|send button is not visible|prompt editor did not retain|prompt editor changed|gemini editor did not accept|incompatible selected tool/i, 'browser_submit_unavailable'],
	        [/prompt editor contains another draft|unsent attachment is already present/i, 'browser_composer_busy'],
	        [/image creation is rate limited/i, 'browser_rate_limited'],
	        [/compatible (?:image upload|file) input|did not show the uploaded (?:image|file)|no longer shows the uploaded (?:image|file)/i, 'browser_upload_unavailable'],
	        [/image creation tool|image tool menu/i, 'browser_image_tool_unavailable'],
	        [/music creation tool/i, 'browser_music_tool_unavailable']
	      ];
	      const code = failureRules.find(([pattern]) => pattern.test(message))?.[1] || 'browser_automation_error';
      resolve({ ok: false, error: message, code });
    }
  });
}

function isNewAssistantTurn(before, after) {
  if (Number(after?.response_count) < Number(before?.response_count)) return false;
  if (Number(after?.response_count) > Number(before?.response_count)) return true;
  const priorID = String(before?.response_identity || '');
  return Boolean(priorID && after?.response_identity && String(after.response_identity) !== priorID);
}

function isEditAssistantTurn(before, after, text, baselineText) {
  const initialCount = Number(before?.response_count);
  return Boolean(initialCount > 0 && Number(after?.response_count) >= initialCount
    && after?.active_generation && text && text !== baselineText);
}

function captureProgress(selectors, expectedURL = '') {
  if (expectedURL && location.href !== expectedURL) {
    throw new Error('The conversation URL changed; no provider content was observed');
  }
  const visibleText = (element) => (element?.innerText || element?.textContent || '').trim();
  const boundedNodes = (root, selector, limit = 64) => {
    if (!root?.querySelectorAll) return [];
    let nodes;
    try { nodes = root.querySelectorAll(selector); } catch (_) { return []; }
    const values = [];
    for (let index = Math.max(0, nodes.length - limit); index < nodes.length; index += 1) {
      values.push(nodes.item ? nodes.item(index) : nodes[index]);
    }
    return values;
  };
  const joinVisibleText = (parts, limit = 64 * 1024) => {
    let result = '';
    for (const part of parts) {
      const value = visibleText(part);
      if (!value) continue;
      const remaining = limit - result.length;
      if (remaining <= 0) break;
      result += `${result ? '\n' : ''}${value.slice(0, remaining)}`;
    }
    return result;
  };
  const responseText = (element) => {
    if (!element) return '';
    if (element.querySelector('[data-testid="image-gen-loading-state"], [data-testid="image-gen-loading-progress"]')) return '';
    let markdownParts = boundedNodes(element, '[data-message-author-role="assistant"] .markdown, message-content .markdown');
    if (!markdownParts.length) markdownParts = boundedNodes(element, '.markdown');
    if (markdownParts.length) return joinVisibleText(markdownParts);
    // A ChatGPT assistant turn without answer markup can contain only thinking
    // chrome (such as "Pro-Denkvorgang"); it is not user-facing answer text.
    if (element.matches?.('section[data-turn="assistant"]')) return '';
    if (element.querySelector('img') && element.matches?.('section[data-turn="assistant"], model-response')) return '';
    const assistantParts = boundedNodes(element, '[data-message-author-role="assistant"]');
    if (assistantParts.length) return joinVisibleText(assistantParts);
    return visibleText(element).slice(0, 64 * 1024);
  };
  const isVisible = (element) => Boolean(element && (element.offsetWidth || element.offsetHeight || element.getClientRects().length));
  const anyVisible = (selector, limit = 64) => {
    let nodes;
    try { nodes = document.querySelectorAll(selector); } catch (_) { return false; }
    for (let index = nodes.length - 1; index >= Math.max(0, nodes.length - limit); index -= 1) {
      const node = nodes.item ? nodes.item(index) : nodes[index];
      if (isVisible(node)) return true;
    }
    return false;
  };
  let responseCount = 0;
  let latestResponse = null;
  for (const selector of selectors?.response || []) {
    try {
      const found = document.querySelectorAll(selector);
      if (!found.length) continue;
      responseCount = found.length;
      for (let index = found.length - 1; index >= Math.max(0, found.length - 64); index -= 1) {
        const candidate = found.item ? found.item(index) : found[index];
        if (isVisible(candidate)) { latestResponse = candidate; break; }
      }
      if (latestResponse) break;
    } catch (_) {}
  }
  let busy = false;
  let percent = 0;
  let detail = '';
  const indicators = document.querySelectorAll('[data-testid="image-gen-loading-progress"], [role="progressbar"][aria-valuenow]');
  for (let index = indicators.length - 1; index >= Math.max(0, indicators.length - 64); index -= 1) {
    const indicator = indicators.item ? indicators.item(index) : indicators[index];
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
    if (anyVisible(selector)) {
      busy = true;
      break;
    }
  }
  const activeGeneration = [
    '[data-is-streaming="true"]', '.result-streaming',
    'button[data-testid*="stop" i]', 'button[aria-label*="stop" i]',
    'button[aria-label*="beenden" i]', 'button[aria-label*="abbrechen" i]'
  ].some((selector) => anyVisible(selector));
  const latestText = responseText(latestResponse);
	const responseIdentity = latestResponse ? ['data-message-id', 'data-testid', 'data-turn', 'id']
		.map((name) => latestResponse.getAttribute?.(name) || '').filter(Boolean).join('|') : '';
	const fallbackNotice = latestResponse?.closest?.('.conversation-container')?.querySelector?.('peak-hour-fallback-disclaimer');
	const modelFallback = isVisible(fallbackNotice);
	const currentModel = String(document.querySelector?.('bard-mode-switcher button[aria-haspopup]')
		?.getAttribute('aria-label')?.match(/(?:derzeit ausgewählt|currently selected|selected)\s*:\s*(.+)$/i)?.[1] || '').trim();
  const visibleMessages = (items, limit = 64) => {
    const values = [];
    for (const selector of items) {
      let nodes;
      try { nodes = document.querySelectorAll(selector); } catch (_) { continue; }
      for (let index = nodes.length - 1; index >= Math.max(0, nodes.length - (limit - values.length)); index -= 1) {
        const node = nodes.item ? nodes.item(index) : nodes[index];
        if (isVisible(node)) values.push(visibleText(node).slice(0, 400));
      }
      if (values.length >= limit) break;
    }
    return values.filter(Boolean).join(' ');
  };
  const dialogs = visibleMessages(['[role="dialog"]', '[role="alertdialog"]']);
  const alerts = visibleMessages(['[role="alert"]', '[aria-live="assertive"]', '[data-testid*="error" i]', '.toast-error', '.error-message']);
  let retryVisible = false;
  const buttons = document.querySelectorAll('button');
  for (let index = buttons.length - 1; index >= Math.max(0, buttons.length - 128); index -= 1) {
    const button = buttons.item ? buttons.item(index) : buttons[index];
    if (isVisible(button) && /retry|try again|regenerate|erneut|noch einmal|wiederholen/i.test(`${visibleText(button)} ${button.getAttribute('aria-label') || ''}`)) {
      retryVisible = true;
      break;
    }
  }
  const failureText = [dialogs, alerts, retryVisible ? latestText : ''].filter(Boolean).join(' ');
  const rateLimitPattern = /rate.?limit|usage.?limit|quota|capacity|limit erreicht|höchstgrenze erreicht|zu viele anfragen|too many (?:requests|messages)|try again later|später erneut|temporarily (?:unavailable|restricted)/i;
  if (rateLimitPattern.test(failureText) || /something went wrong|etwas ist schief/i.test(failureText)) {
    const rateLimited = rateLimitPattern.test(failureText);
    return { text: '', busy: false, percent: 0, detail: 'Provider error', current_model: currentModel,
      provider_error_code: rateLimited ? 'browser_rate_limited' : 'browser_provider_error',
      blocking_provider_error_code: rateLimitPattern.test(dialogs) ? 'browser_rate_limited' : '' };
  }
  return { text: latestText, response_count: responseCount, response_identity: responseIdentity,
    busy, active_generation: activeGeneration, percent, detail: detail || (busy ? 'Generating' : ''), current_model: currentModel, model_fallback: modelFallback };
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
	if (mode === 'text') {
	  const bounded = boundedUTF8(clean, outputByteLimit(spec));
	  return { mode, text: bounded.text, model, artifacts, ...(bounded.truncated ? { truncated: true } : {}) };
	}
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

function outputByteLimit(spec) {
  const requested = Number(spec?.max_bytes || 0);
  return Number.isInteger(requested) && requested >= 256 && requested <= 1024 * 1024
    ? requested : 64 * 1024;
}

function boundedUTF8(value, limit) {
  const text = String(value || '');
  if (new TextEncoder().encode(text).length <= limit) return { text, truncated: false };
  let low = 0;
  let high = text.length;
  while (low < high) {
    const middle = Math.ceil((low + high) / 2);
    const candidate = text.slice(0, middle);
    if (new TextEncoder().encode(candidate).length <= limit) low = middle;
    else high = middle - 1;
  }
  // Avoid returning half of a UTF-16 surrogate pair. TextEncoder would
  // replace it, which would fit the byte budget but alter the answer.
  let end = low;
  if (end > 0 && end < text.length) {
    const last = text.charCodeAt(end - 1);
    if (last >= 0xD800 && last <= 0xDBFF) end -= 1;
  }
  return { text: text.slice(0, end), truncated: true };
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
  if (heartbeatTimer) clearInterval(heartbeatTimer);
  heartbeatTimer = setInterval(() => sendHeartbeat('waiting'), 5000);
  heartbeatAlarmWanted = true;
  return reconcileHeartbeatAlarm();
}

function stopHeartbeat() {
  if (heartbeatTimer) clearInterval(heartbeatTimer);
  heartbeatTimer = 0;
  heartbeatAlarmWanted = false;
  return reconcileHeartbeatAlarm();
}

function reconcileHeartbeatAlarm() {
  // Alarms usually survive MV3 worker suspension, but Chromium explicitly
  // permits clearing them on a browser restart. A process-local boolean cannot
  // prove that the persistent alarm still exists, so serialize an alarms.get()
  // check every time this worker resumes. Re-reading the desired state after
  // each await also prevents a late create from undoing an explicit Disconnect.
  heartbeatAlarmSync = heartbeatAlarmSync.catch(() => false).then(async () => {
    if (!api.alarms) {
      heartbeatAlarmRegistered = false;
      return false;
    }
    let existing = null;
    if (api.alarms.get) {
      try { existing = await api.alarms.get(HEARTBEAT_ALARM); } catch (_) { existing = null; }
    } else if (heartbeatAlarmRegistered) {
      existing = { name: HEARTBEAT_ALARM };
    }
    if (!heartbeatAlarmWanted) {
      if ((existing || heartbeatAlarmRegistered) && api.alarms.clear) {
        try { await Promise.resolve(api.alarms.clear(HEARTBEAT_ALARM)); } catch (_) {}
      }
      heartbeatAlarmRegistered = false;
      return false;
    }
    if (!existing && api.alarms.create) {
      try {
        await Promise.resolve(api.alarms.create(HEARTBEAT_ALARM, {
          delayInMinutes: 0.5,
          periodInMinutes: 0.5
        }));
      } catch (_) {
        heartbeatAlarmRegistered = false;
        return false;
      }
    }
    if (!heartbeatAlarmWanted) {
      try { await Promise.resolve(api.alarms.clear?.(HEARTBEAT_ALARM)); } catch (_) {}
      heartbeatAlarmRegistered = false;
      return false;
    }
    heartbeatAlarmRegistered = Boolean(existing || api.alarms.create);
    return heartbeatAlarmRegistered;
  });
  return heartbeatAlarmSync;
}

function sendHeartbeat(state, connecting = false, generation = lifecycleGeneration) {
  // A Connect click must not queue behind an older, slow diagnostic heartbeat.
  if (connecting) return sendHeartbeatOnce(state, true, generation);
  if (state !== 'paused' && Date.now() < nextHeartbeatAt) return false;
  if (state === 'paused' && heartbeatInFlight) return heartbeatInFlight.finally(() => sendHeartbeatOnce('paused', false, generation));
  if (heartbeatInFlight) return heartbeatInFlight;
  heartbeatInFlight = sendHeartbeatOnce(state, connecting, generation).finally(() => { heartbeatInFlight = null; });
  return heartbeatInFlight;
}

function capabilityScanInterval(profileName, capabilities) {
  if (!['chatgpt', 'gemini'].includes(profileName) || capabilities.currentModel) return 30 * 60 * 1000;
  const diagnostic = capabilities.scanDiagnostic || {};
  // A newly created chat can answer before its composer menu finishes
  // mounting. Retry a few times promptly, then return to the normal cadence.
  if (String(diagnostic.model || '').startsWith('no trigger') && Number(diagnostic.noTriggerAttempts || 0) < 3) {
    return 15 * 1000;
  }
  return 5 * 60 * 1000;
}

const BROWSER_HEARTBEAT_BODY_BUDGET = 112 * 1024;

// The service rejects heartbeat bodies above 128 KiB before decoding them.
// Keep margin for future fixed metadata, while retaining every attached tab's
// routing-critical state. Selector diagnostics and choice inventories degrade
// first because they are supporting evidence, not authoritative availability.
function browserHeartbeatBody(payload, budget = BROWSER_HEARTBEAT_BODY_BUDGET) {
  const encode = (value) => JSON.stringify(value);
  const size = (value) => new TextEncoder().encode(value).byteLength;
  let body = encode(payload);
  if (size(body) <= budget) return body;

  const limited = (value, maximum) => String(value || '').slice(0, maximum);
  const compact = {
    ...payload,
    state: limited(payload.state, 30),
    origin: limited(payload.origin, 300),
    tab_title: limited(payload.tab_title, 300),
    profile_label: limited(payload.profile_label, 100),
    extension_version: limited(payload.extension_version, 30),
    browser: limited(payload.browser, 30),
    tabs: (payload.tabs || []).map((tab) => ({ ...tab }))
  };
  for (let index = compact.tabs.length - 1; index >= 0 && size(body) > budget; index -= 1) {
    delete compact.tabs[index].dom;
    body = encode(compact);
  }
  for (let index = compact.tabs.length - 1; index >= 0 && size(body) > budget; index -= 1) {
    delete compact.tabs[index].models;
    delete compact.tabs[index].reasoning_levels;
    delete compact.tabs[index].model_scan;
    delete compact.tabs[index].reasoning_scan;
    body = encode(compact);
  }
  if (size(body) <= budget) return body;

  compact.tabs = compact.tabs.map((tab) => ({
    id: Number.isInteger(Number(tab.id)) ? Number(tab.id) : 0,
    origin: limited(tab.origin, 300),
    title: limited(tab.title, 300),
    profile: limited(tab.profile, 100),
    state: limited(tab.state, 30),
    session_key: limited(tab.session_key, 80),
    session_key_supported: tab.session_key_supported === true,
    can_create_fresh_chat: tab.can_create_fresh_chat === true,
    default_fresh_chat: tab.default_fresh_chat === true,
    current_model: limited(tab.current_model, 100),
    current_reasoning: limited(tab.current_reasoning, 100),
    dom_status: limited(tab.dom_status, 20)
  }));
  return encode(compact);
}

async function sendHeartbeatOnce(state, connecting = false, generation = lifecycleGeneration) {
  const requestId = ++heartbeatRequestId;
  const cfg = await settings();
  if (generation !== lifecycleGeneration) return false;
  if (!cfg.token) return false;
  if (!connecting && state !== 'paused' && !cfg.running) return false;
  if (stopRequested && state !== 'paused' && !connecting) return false;
  const tabs = [];
  const tabIds = configuredTabIDs(cfg);
  if (connecting) await reportConnectionProgress('Registering selected tabs', 0, tabIds.length);
  const attachedTabs = [];
  for (const [index, tabId] of tabIds.entries()) {
    try {
      const tab = await withDeadline(api.tabs.get(tabId), 2000);
      attachedTabs.push(tab);
    } catch (_) {}
    if (connecting) await reportConnectionProgress('Registering selected tabs', index + 1, tabIds.length);
  }
  if (connecting && attachedTabs.length !== tabIds.length) return false;
  const sessionKeys = browserSessionKeysForTabs(cfg, attachedTabs);
  const freshChatAccess = {};
  for (const profile of new Set(attachedTabs.map((tab) => routingProfileName(cfg, tab)).filter((name) => ['chatgpt', 'gemini'].includes(name)))) {
    freshChatAccess[profile] = await canCreateFreshChat(profile, tabIds.length);
  }
  for (const tab of attachedTabs) {
    try {
      const tabId = Number(tab.id);
      const profile = profileForTab(cfg, tab);
      const capabilities = capabilitiesForTab(cfg, tab, profile);
      const diagnostic = tabDOMDiagnostics.get(tabId);
      const routingProfile = routingProfileName(cfg, tab);
      tabs.push({
        id: tabId,
        origin: tab?.url && /^https?:/i.test(tab.url) ? new URL(tab.url).origin : '',
        title: tab?.title || '', profile: routingProfile,
        state: browserTabState(cfg, tabId),
        session_key: sessionKeys.get(tabId) || '',
        session_key_supported: true,
        can_create_fresh_chat: freshChatAccess[routingProfile] === true,
        default_fresh_chat: ['new_chat', 'new_chat_per_job'].includes(cfg.sessionMode),
        current_model: capabilities.currentModel || '', current_reasoning: capabilities.currentReasoning || '',
        models: capabilities.models || [], reasoning_levels: capabilities.reasoningLevels || [],
        model_scan: capabilities.scanDiagnostic?.model || '',
        reasoning_scan: capabilities.scanDiagnostic?.reasoning || '',
        last_failure: cfg.tabFailures?.[tabId] || null,
        dom_status: !connecting && diagnostic?.url === tab.url ? diagnostic.status : 'pending',
        dom: !connecting && diagnostic?.url === tab.url ? diagnostic.dom : null
      });
    } catch (_) {}
  }
  if (connecting && tabs.length !== attachedTabs.length) return false;
  const tab = tabs[0] || null;
  const profile = tab ? (cfg.taughtProfiles?.[tab.origin] || globalThis.ContextBridgeProfiles?.forURL(tab.origin)) : null;
  const effectiveState = state === 'paused' ? 'paused' : (busyTabs.size ? 'working' : state);
  if (connecting) await reportConnectionProgress('Registering with local service', 0, 1);
  if (stopRequested && state !== 'paused') return false;
  if (generation !== lifecycleGeneration) return false;
  try {
    const payload = {
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
    };
    const response = await fetchWithTimeout(`${cfg.bridgeUrl}/v1/browser/heartbeat`, {
      method: 'POST',
      headers: { Authorization: `Bearer ${cfg.token}`, 'Content-Type': 'application/json' },
      body: browserHeartbeatBody(payload)
    }, 5000);
    if (generation !== lifecycleGeneration) return false;
    if (state !== 'paused') await recordHeartbeatResult(cfg, response.ok, response.status, requestId, generation);
    if (response.ok && state !== 'paused' && !finishedTabCleanup) {
      finishedTabCleanup = closeFinishedOwnedTabs().catch(() => {}).finally(() => { finishedTabCleanup = null; });
    }
    if (response.ok && !connecting && state !== 'paused') {
      for (const tabId of tabIds) scheduleTabDiagnostics(tabId);
    }
    return response.ok;
  } catch (_) {
    if (generation !== lifecycleGeneration) return false;
    if (state !== 'paused') await recordHeartbeatResult(cfg, false, 0, requestId, generation);
    return false;
  }
}

async function recordHeartbeatResult(cfg, ok, status, requestId, generation = lifecycleGeneration) {
  if (generation !== lifecycleGeneration || stopRequested || requestId < lastAppliedHeartbeatId) return;
  lastAppliedHeartbeatId = requestId;
  if (ok) {
    heartbeatFailures = 0;
    nextHeartbeatAt = 0;
    if (cfg.relayConnected !== true || cfg.connectionError || Date.now() - Number(cfg.lastHeartbeatAt || 0) >= 20000) {
      await persistLifecycleState(generation, { relayConnected: true, connectionError: '', lastHeartbeatAt: Date.now() });
    }
    return;
  }
  heartbeatFailures += 1;
  if (status === 401 || status === 403) {
    const stopGeneration = ++lifecycleGeneration;
    stopRequested = true;
    if (!await persistLifecycleState(stopGeneration, { running: false, relayConnected: false,
      connectionError: 'The local service rejected this connection. Check the pairing token or access permissions, then click Connect.' })) return;
    if (stopGeneration !== lifecycleGeneration) return;
    await stopHeartbeat();
    return;
  }
  if (!cfg.autoReconnect && heartbeatFailures >= 3) {
    const stopGeneration = ++lifecycleGeneration;
    stopRequested = true;
    if (!await persistLifecycleState(stopGeneration, { running: false, relayConnected: false,
      connectionError: 'Connection lost. Automatic reconnect is off; click Connect to retry.' })) return;
    if (stopGeneration !== lifecycleGeneration) return;
    await stopHeartbeat();
    return;
  }
  nextHeartbeatAt = Date.now() + Math.min(60000, 5000 * 2 ** Math.min(heartbeatFailures - 1, 4));
  const incompatible = status >= 400 && status < 500;
  const connectionError = incompatible
    ? `Local service rejected a status update (HTTP ${status}). Check that the service and extension are compatible; ContextBridge will retry after an update.`
    : '';
  if (cfg.relayConnected !== false || cfg.connectionError !== connectionError) {
    await persistLifecycleState(generation, { relayConnected: false, connectionError });
  }
}

function scheduleTabDiagnostics(tabId, forceScan = false) {
  if (forceScan) capabilityForcePending.add(tabId);
  if (diagnosticsInFlight.has(tabId)) return;
  const forceThisRun = capabilityForcePending.delete(tabId);
  const task = refreshTabDiagnostics(tabId, forceThisRun).catch(() => {}).finally(() => {
    diagnosticsInFlight.delete(tabId);
    if (capabilityForcePending.has(tabId)) scheduleTabDiagnostics(tabId);
  });
  diagnosticsInFlight.set(tabId, task);
}

async function executeDiagnosticScript(tabId, request, milliseconds) {
  if (diagnosticScriptsInFlight.has(tabId)) throw new Error('The prior AI-tab diagnostic is still pending');
  const raw = api.scripting.executeScript(request);
  diagnosticScriptsInFlight.set(tabId, raw);
  raw.then(() => {
    if (diagnosticScriptsInFlight.get(tabId) === raw) diagnosticScriptsInFlight.delete(tabId);
  }, () => {
    if (diagnosticScriptsInFlight.get(tabId) === raw) diagnosticScriptsInFlight.delete(tabId);
  });
  return withDeadline(raw, milliseconds);
}

async function refreshTabDiagnostics(tabId, forceScan = false) {
  const cfg = await settings();
  if (!cfg.running || !configuredTabIDs(cfg).includes(tabId)) return;
  const tab = await withDeadline(api.tabs.get(tabId), 2000);
  const profile = profileForTab(cfg, tab);
  if (['chatgpt', 'gemini'].includes(profile?.name) && !capabilityWatchInstalled.has(tabId)) {
    try {
      const installed = await executeDiagnosticScript(tabId, { target: { tabId }, func: watchPageCapabilityInteractions }, 4000);
      if (installed?.[0]?.result === true) capabilityWatchInstalled.add(tabId);
    } catch (_) {}
  }
  let capabilities = capabilitiesForTab(cfg, tab, profile);
  let scannedAt = 0;
  const scanInterval = capabilityScanInterval(profile?.name, capabilities);
  const needsUpdatedModelScan = ['chatgpt', 'gemini'].includes(profile?.name) && !capabilities.currentModel
    && capabilities.scanDiagnostic?.version !== api.runtime.getManifest().version;
  if (['chatgpt', 'gemini'].includes(profile?.name) && !busyTabs.has(tabId)
      && (forceScan || needsUpdatedModelScan || Date.now() - Number(cfg.tabCapabilityScans?.[tabId] || 0) > scanInterval)) {
    try {
      const safe = await executeDiagnosticScript(tabId, { target: { tabId }, func: safeToDiscoverPageCapabilities }, 4000);
      if (safe?.[0]?.result === true) {
        const scanned = await executeDiagnosticScript(tabId, { target: { tabId }, func: discoverPageCapabilities }, 8000);
        const previousAttempts = Number(capabilities.scanDiagnostic?.noTriggerAttempts || 0);
        capabilities = { ...(scanned?.[0]?.result || capabilities), pageContext: capabilityPageContext(tab, profile) };
        capabilities.scanDiagnostic = { ...(capabilities.scanDiagnostic || {}), version: api.runtime.getManifest().version };
        if (String(capabilities.scanDiagnostic.model || '').startsWith('no trigger')) {
          capabilities.scanDiagnostic.noTriggerAttempts = Math.min(3, previousAttempts + 1);
        }
        scannedAt = Date.now();
      }
    } catch (_) {}
  }
  try {
    const report = await executeDiagnosticScript(tabId, { target: { tabId }, func: inspectPageCapabilities }, 4000);
    const live = report?.[0]?.result || {};
    const validModel = profile?.name === 'gemini'
      ? (value) => Boolean(String(value || '').trim() && String(value).length <= 100)
      : (value) => acceptedModelLabel.test(value);
    const modelMatchesComposer = (value) => !live.currentModelVersion
      || String(value || '').match(/\b\d+(?:\.\d+)?\b/)?.[0] === live.currentModelVersion;
    capabilities = {
      ...capabilities,
	  pageContext: capabilityPageContext(tab, profile),
      currentModel: scannedAt && validModel(capabilities.currentModel || '') && modelMatchesComposer(capabilities.currentModel) ? capabilities.currentModel
        : (validModel(live.currentModel || '') ? live.currentModel
          : (validModel(capabilities.currentModel || '') && modelMatchesComposer(capabilities.currentModel) ? capabilities.currentModel : '')),
      currentReasoning: scannedAt && capabilities.currentReasoning ? capabilities.currentReasoning
        : (live.currentReasoning || capabilities.currentReasoning || ''),
      models: [...new Set([...(capabilities.models || []), ...(live.models || [])])].filter(validModel),
      reasoningLevels: [...new Set([...(capabilities.reasoningLevels || []), ...(live.reasoningLevels || [])])]
    };
  } catch (_) {}
  // Keep bounded selector-only diagnostics separate from the heartbeat. A slow
  // or suspended AI page must never prevent the service from staying paired.
  const previousDiagnostic = tabDOMDiagnostics.get(tabId);
  const diagnosticStartedAt = Date.now();
  try {
    const snapshot = await executeDiagnosticScript(tabId, { target: { tabId }, func: inspectPageDOM, args: [profile?.selectors || {}] }, 4000);
    const current = await withDeadline(api.tabs.get(tabId), 2000);
    if (current.url === tab.url) {
      const latencyMs = Date.now() - diagnosticStartedAt;
      const slowScans = latencyMs >= 2500 ? Math.min(10, Number(previousDiagnostic?.slowScans || 0) + 1) : 0;
      tabDOMDiagnostics.set(tabId, { url: tab.url, status: snapshot?.[0]?.result ? 'ready' : 'unavailable',
        latencyMs, slowScans, dom: snapshot?.[0]?.result || null });
    }
  } catch (error) {
    const latencyMs = Date.now() - diagnosticStartedAt;
    tabDOMDiagnostics.set(tabId, { url: tab.url, status: /time|deadline|respond/i.test(String(error?.message || '')) ? 'timeout' : 'error',
      latencyMs, slowScans: Math.min(10, Number(previousDiagnostic?.slowScans || 0) + 1), dom: null });
  }
  const current = await withDeadline(api.tabs.get(tabId), 2000).catch(() => null);
  if (current?.url !== tab.url) return;
  const write = capabilityWrite.then(async () => {
    const latest = await api.storage.local.get({ tabCapabilities: {}, tabCapabilityScans: {} });
    if (JSON.stringify(latest.tabCapabilities?.[tabId] || {}) !== JSON.stringify(capabilities) || scannedAt) {
      await api.storage.local.set({
        tabCapabilities: { ...latest.tabCapabilities, [tabId]: capabilities },
        ...(scannedAt ? { tabCapabilityScans: { ...latest.tabCapabilityScans, [tabId]: scannedAt } } : {})
      });
    }
  });
  capabilityWrite = write.catch(() => {});
  await write;
  if ((forceScan && scannedAt) || !previousDiagnostic || previousDiagnostic.url !== tab.url
      || previousDiagnostic.status !== tabDOMDiagnostics.get(tabId)?.status) {
    await sendHeartbeat(busyTabs.size ? 'working' : 'waiting');
  }
}

function capabilityPageContext(tab, profile) {
  return {
    url: String(tab?.url || '').slice(0, 2048),
    origin: tab?.url && /^https?:/i.test(tab.url) ? new URL(tab.url).origin : '',
    profile: String(profile?.name || '').slice(0, 50)
  };
}

function capabilitiesForTab(cfg, tab, profile) {
  const capabilities = cfg.tabCapabilities?.[tab?.id] || {};
  const expected = capabilityPageContext(tab, profile);
  const stored = capabilities.pageContext || {};
  if (!stored.url || stored.url !== expected.url || stored.origin !== expected.origin || stored.profile !== expected.profile) return {};
  return capabilities;
}

function browserTabState(cfg, tabId) {
  if (busyTabs.has(tabId)) return 'working';
  if (Number(cfg.tabCooldowns?.[tabId] || 0) > Date.now()) return 'rate_limited';
  if (Object.values(cfg.sessionBindings || {}).some((binding) => Number(binding?.tabId) === tabId)) return 'session_bound';
  return 'waiting';
}

// Only cluster-derived opaque bindings may leave the browser worker. Local
// direct session names, saved URLs, titles, and page content stay on this PC.
// A permanent URL is the proof anchor; shared fresh-chat URLs are deliberately
// never advertised as session identity.
function browserSessionKeysForTabs(cfg, tabs) {
  const result = new Map();
  const conflicted = new Set();
  const byId = new Map((tabs || []).map((tab) => [Number(tab?.id), tab]));
  for (const [workKey, binding] of Object.entries(cfg.sessionBindings || {})) {
    if (binding?.legacy || binding?.perJob) continue;
    let parts;
    try { parts = JSON.parse(workKey); } catch (_) { continue; }
    if (!Array.isArray(parts) || parts.length !== 2) continue;
    const sessionKey = String(parts[0] || '');
    const profile = String(parts[1] || '');
    if (!/^cb:[a-f0-9]{64}$/.test(sessionKey) || !profile) continue;
    const savedURL = String(binding?.url || '');
    if (!savedURL || isFreshChatURL(savedURL)) continue;
    const candidates = (tabs || []).filter((tab) => String(tab?.url || '') === savedURL && routingProfileName(cfg, tab) === profile);
    if (!candidates.length) continue;
    const mapped = Number(binding?.tabId || 0);
    const recorded = candidates.find((tab) => Number(tab?.id) === mapped);
    const selected = recorded || (candidates.length === 1 ? candidates[0] : null);
    if (!selected || !byId.has(Number(selected.id))) continue;
    const tabId = Number(selected.id);
    if (conflicted.has(tabId)) continue;
    if (result.has(tabId) && result.get(tabId) !== sessionKey) {
      result.delete(tabId);
      conflicted.add(tabId);
      continue;
    }
    result.set(tabId, sessionKey);
  }
  return result;
}

async function canCreateFreshChat(profile, attachedTabCount) {
  const freshURL = freshChatURLForProfile(profile);
  if (!freshURL || attachedTabCount >= 16) return false;
  try {
    return await withDeadline(api.permissions.contains({ origins: [new URL(freshURL).origin + '/*'] }), 2000) === true;
  } catch (_) {
    return false;
  }
}

async function invalidateTabCapabilities(tabId) {
  const write = capabilityWrite.then(async () => {
    const latest = await api.storage.local.get({ tabCapabilities: {}, tabCapabilityScans: {} });
    const tabCapabilities = { ...latest.tabCapabilities };
    const tabCapabilityScans = { ...latest.tabCapabilityScans };
    delete tabCapabilities[tabId];
    delete tabCapabilityScans[tabId];
    await api.storage.local.set({ tabCapabilities, tabCapabilityScans });
  });
  capabilityWrite = write.catch(() => {});
  await write;
}

function withDeadline(promise, milliseconds) {
  let timeout;
  return Promise.race([
    promise,
    new Promise((_, reject) => { timeout = setTimeout(() => reject(new Error('AI tab did not respond in time')), milliseconds); })
  ]).finally(() => clearTimeout(timeout));
}

async function fetchWithTimeout(url, options, milliseconds) {
  const controller = new AbortController();
  const externalSignal = options?.signal;
  const abortFromExternal = () => controller.abort(externalSignal?.reason);
  if (externalSignal?.aborted) abortFromExternal();
  else externalSignal?.addEventListener?.('abort', abortFromExternal, { once: true });
  const timeout = setTimeout(() => controller.abort(), milliseconds);
  try { return await fetch(url, { ...options, signal: controller.signal }); }
  finally {
    clearTimeout(timeout);
    externalSignal?.removeEventListener?.('abort', abortFromExternal);
  }
}

async function settings() {
  return api.storage.local.get({
    bridgeUrl: 'http://127.0.0.1:32145',
    token: '',
    profile: 'chatgpt',
    tabId: 0,
    tabIds: [],
    running: false,
    relayConnected: false,
    lastHeartbeatAt: 0,
    autoReconnect: true,
    autoAttachFreshTabs: false,
    preserveDrafts: false,
    autoCloseFinishedChats: false,
    ownedDrafts: {},
    autoAttachBlockedTabIds: [],
    useVisualProfile: true,
    taughtProfiles: {},
    sessionTabs: {},
    sessionBindings: {},
    sessionBindingsMigrated: false,
    sessionMode: 'manual',
    tabEditModes: {},
    tabCapabilities: {},
    tabCapabilityScans: {},
    tabFailures: {},
    tabCooldowns: {},
    pendingCompletions: {},
    browserJobClaims: {},
    connectionError: '',
    lastError: ''
  });
}

function configuredTabIDs(cfg) {
  const values = Array.isArray(cfg?.tabIds) && cfg.tabIds.length ? cfg.tabIds : [cfg?.tabId];
  return [...new Set(values.map(Number).filter((value) => Number.isInteger(value) && value > 0))].slice(0, 16);
}

function safeToDiscoverPageCapabilities() {
  const anyVisible = (selector, limit = 64) => {
    const nodes = document.querySelectorAll(selector);
    for (let index = nodes.length - 1; index >= Math.max(0, nodes.length - limit); index -= 1) {
      const element = nodes.item ? nodes.item(index) : nodes[index];
      if (element && (element.offsetWidth || element.offsetHeight || element.getClientRects().length)) return true;
    }
    return false;
  };
  if (anyVisible('[role="dialog"], [role="alertdialog"], [role="menu"], [role="listbox"]')) return false;
  const composer = document.querySelector('#prompt-textarea, rich-textarea [contenteditable="true"][role="textbox"], div.ql-editor[contenteditable="true"][role="textbox"]');
  if (!composer) return false;
  // A private draft is not modified by opening and closing the model menu.
  // Do not interrupt somebody who currently has the editor focused, though.
  if (String(composer.innerText || composer.textContent || '').trim()
      && (document.activeElement === composer || composer.contains?.(document.activeElement))
      && document.hasFocus?.() !== false) return false;
  return !anyVisible('[aria-busy="true"], button[data-testid*="stop" i], button[aria-label*="stop" i]');
}

// Runs only in explicitly attached AI tabs. It sends a content-free hint after
// a real user changes a model or effort control; the worker then performs its
// existing bounded, verified scan. Synthetic ContextBridge clicks are ignored.
function watchPageCapabilityInteractions() {
  const runtime = (globalThis.browser || globalThis.chrome)?.runtime;
  if (!runtime?.sendMessage || !document?.addEventListener) return false;
  if (globalThis.__contextbridgeCapabilityWatch) return true;
  const relevant = /(?:gpt[\s._-]*\d|gemini|modell|model|denk|reason|effort|thinking|sofort|instant|niedrig|low|mittel|medium|hoch|high|pro|max)/i;
  let timer = 0;
  const observe = (event) => {
    if (event.isTrusted !== true) return;
    const control = event.target?.closest?.('button, [role="button"], [role="menuitem"], [role="menuitemradio"], [role="option"], [role="slider"], input[type="range"]');
    if (!control) return;
    const label = `${control.innerText || control.textContent || ''} ${control.getAttribute?.('aria-label') || ''} ${control.getAttribute?.('data-testid') || ''}`.slice(0, 180);
    if (!relevant.test(label) && !control.matches?.('[role="slider"], input[type="range"]')) return;
    clearTimeout(timer);
    timer = setTimeout(() => {
      try { Promise.resolve(runtime.sendMessage({ type: 'page-capability-interaction' })).catch(() => {}); }
      catch (_) {}
    }, 100);
  };
  document.addEventListener('click', observe, true);
  document.addEventListener('change', observe, true);
  document.addEventListener('pointerup', observe, true);
  document.addEventListener('keyup', (event) => {
    if (['Enter', ' ', 'ArrowLeft', 'ArrowRight', 'ArrowUp', 'ArrowDown'].includes(event.key)) observe(event);
  }, true);
  globalThis.__contextbridgeCapabilityWatch = true;
  return true;
}

function inspectPageCapabilities() {
  const visible = (element) => Boolean(element && (element.offsetWidth || element.offsetHeight || element.getClientRects().length));
  const text = (element) => String(element?.innerText || element?.textContent || element?.getAttribute?.('aria-label') || '').replace(/\s+/g, ' ').trim().slice(0, 100);
  const unique = (values, limit) => [...new Set(values.filter(Boolean))].slice(0, limit);
  const boundedElements = (root, selector, limit = 256) => {
    const nodes = root?.querySelectorAll?.(selector) || [];
    const values = [];
    const edge = Math.ceil(limit / 2);
    for (let index = 0; index < Math.min(nodes.length, edge); index += 1) values.push(nodes.item ? nodes.item(index) : nodes[index]);
    for (let index = Math.max(edge, nodes.length - edge); index < nodes.length && values.length < limit; index += 1) {
      values.push(nodes.item ? nodes.item(index) : nodes[index]);
    }
    return values;
  };
  // A previous assistant turn can expose its own "switch model" button. Only
  // the live composer describes the model for the next job.
  const composerInput = document.querySelector?.('#prompt-textarea, rich-textarea [contenteditable="true"][role="textbox"], div.ql-editor[contenteditable="true"][role="textbox"]');
  const composer = composerInput?.closest?.('[data-node-type="input-area"], form')
    || document.querySelector?.('form[data-type="unified-composer"]') || document.querySelector?.('form');
  const controls = boundedElements(composer || document, 'button, [role="button"]').filter(visible);
  const options = boundedElements(document, '[role="menuitem"], [role="menuitemradio"], [role="option"], [aria-checked], [aria-selected]').filter(visible);
  const modelPattern = /^(?:gpt[\s._-]*\d+(?:\.\d+)?(?:[\s._-]*(?:astra|sol|terra|luna|pro|mini|nano|codex|thinking|instant))?|gemini(?:[\s._-]*\d+(?:\.\d+)?(?:[\s._-]*(?:pro|flash|lite|thinking|preview|experimental))*)?|astra|sol|terra|luna|\d+(?:\.\d+)?\s+(?:pro|flash|astra|sol|terra|luna))$/i;
  const reasoningPattern = /^(?:instant|sofort|fast|schnell|low|niedrig|medium|mittel|high|hoch|very high|sehr hoch|xhigh|pro|max|maximum)$/i;
  const semantic = (element, pattern) => pattern.test(`${element.getAttribute('data-testid') || ''} ${element.getAttribute('aria-label') || ''}`);
  const modelOptionLabel = (element) => {
    const lines = String(element?.innerText || element?.textContent || '').split('\n').map((line) => line.replace(/\s+/g, ' ').trim());
    return lines.find((line) => modelPattern.test(line)) || text(element);
  };
  const checked = (element) => element.getAttribute('aria-checked') === 'true'
    || element.getAttribute('aria-selected') === 'true' || element.getAttribute('aria-current') === 'true'
    || element.getAttribute('data-selected') === 'true' || ['checked', 'selected'].includes(element.getAttribute('data-state'))
    || Boolean(element.querySelector?.('svg use[href*="#check" i], svg[data-testid*="check" i], [data-state="checked"], [class*="check" i]'));
  const modelControl = (element) => semantic(element, /model[-_ ]?(?:switcher|selector|picker|menu)|(?:choose|select|current)[-_ ]?model|modellauswahl|modellmenü|modellmodus/i);
  const currentModel = text(controls.find((element) => {
    const label = `${text(element)} ${element.getAttribute('aria-label') || ''}`;
    return modelPattern.test(text(element)) && !/modelle ergänzen|add models|preismodell|pricing model/i.test(label);
  }));
  const compactComposerSelection = controls.map(text)
    .map((value) => value.match(/^(?:gpt[- ]?)?(\d+(?:\.\d+)?)\s+(sehr hoch|very high|hoch|high|mittel|medium|niedrig|low|sofort|instant|fast|schnell|pro|max)$/i))
    .find(Boolean);
  const modelMenuOpen = controls.some((element) =>
    element.getAttribute('aria-expanded') === 'true'
    && semantic(element, /model[-_ ]?(?:switcher|selector|picker|menu)|(?:choose|select|current|switch)[-_ ]?model|modellauswahl|modellmenü|modellmodus|modell[-_ ]?wechseln/i));
  const selectedModel = modelMenuOpen ? modelOptionLabel(options.find((element) =>
    checked(element) && modelPattern.test(modelOptionLabel(element)))) : '';
  const currentReasoning = compactComposerSelection?.[2]
    || text(controls.find((element) => reasoningPattern.test(text(element))));
  const geminiModeName = /^(?:gemini\s*)?(?:\d+(?:\.\d+)?\s*)?(?:flash|pro|thinking|schnell|erweitert)(?:[ -](?:lite|erweitert|advanced|preview))?$/i;
  const geminiPicker = document.querySelector?.('bard-mode-switcher button[aria-haspopup]')
    || (globalThis.location?.hostname === 'gemini.google.com' ? boundedElements(document, 'button, [role="button"]').filter(visible)
      .filter((element) => !element.closest?.('model-response, user-query, [role="menu"], [role="navigation"], nav'))
      .map((element) => {
        const hint = `${element.tagName || ''} ${element.id || ''} ${element.className || ''} ${element.getAttribute('aria-label') || ''} ${element.getAttribute('data-testid') || ''} ${element.parentElement?.tagName || ''} ${element.parentElement?.className || ''}`;
        const label = text(element);
        const selected = /(?:derzeit ausgewählt|currently selected|selected)\s*:\s*(.+)$/i.exec(element.getAttribute('aria-label') || '')?.[1]?.trim() || '';
        const semantic = /(?:model|modell|mode|modus)[-_ ]?(?:switcher|selector|picker|menu)|(?:model|modell|mode|modus)auswahl/i.test(hint);
        const score = (semantic ? 4 : 0) + (geminiModeName.test(selected) ? 4 : 0)
          + (geminiModeName.test(label) ? 2 : 0) + (element.getAttribute('aria-haspopup') ? 2 : 0);
        return { element, score };
      }).filter((item) => item.score >= 4).sort((a, b) => b.score - a.score)[0]?.element : null);
  const geminiSelected = geminiPicker?.getAttribute('aria-label')?.match(/(?:derzeit ausgewählt|currently selected|selected)\s*:\s*(.+)$/i)?.[1]?.trim() || '';
  const geminiCurrent = (geminiModeName.test(geminiSelected) ? geminiSelected : geminiModeName.test(text(geminiPicker)) ? text(geminiPicker) : '').slice(0, 100);
  const geminiMenu = geminiPicker?.getAttribute('aria-controls') && document.getElementById?.(geminiPicker.getAttribute('aria-controls'));
  const visibleGeminiModels = geminiMenu ? boundedElements(geminiMenu, 'button, [role="menuitem"], [role="option"], [role="menuitemradio"]', 128)
    .filter((item) => visible(item) && !item.disabled && item.getAttribute('aria-disabled') !== 'true')
    .map((item) => {
      const primary = text(item.querySelector?.('.picker-primary-text, .mode-name, .model-name'));
      const secondary = text(item.querySelector?.('.picker-secondary-text'));
      return primary ? `${primary} ${secondary}`.trim() : text(item);
    }) : [];
  return {
    currentModel: geminiCurrent || selectedModel || currentModel,
    currentReasoning: geminiCurrent ? '' : currentReasoning,
    currentModelVersion: geminiCurrent ? '' : (compactComposerSelection?.[1] || ''),
    models: geminiCurrent ? unique(visibleGeminiModels, 50) : unique(options.map(modelOptionLabel).filter((value) => modelPattern.test(value)), 50),
    reasoningLevels: geminiCurrent ? [] : unique(options.map(text).filter((value) => reasoningPattern.test(value)), 20)
  };
}

function inspectPageDOM(selectors) {
  const visible = (element) => Boolean(element && (element.offsetWidth || element.offsetHeight || element.getClientRects().length));
  const short = (value, limit = 120) => String(value || '').replace(/\s+/g, ' ').trim().slice(0, limit);
  const boundedElements = (root, selector, limit = 128) => {
    const nodes = root?.querySelectorAll?.(selector) || [];
    const values = [];
    const edge = Math.ceil(limit / 2);
    for (let index = 0; index < Math.min(nodes.length, edge); index += 1) values.push(nodes.item ? nodes.item(index) : nodes[index]);
    for (let index = Math.max(edge, nodes.length - edge); index < nodes.length && values.length < limit; index += 1) {
      values.push(nodes.item ? nodes.item(index) : nodes[index]);
    }
    return values;
  };
  const anyVisible = (selector, limit = 64) => boundedElements(document, selector, limit).some(visible);
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
        for (const element of boundedElements(document, selector, Math.max(64, limit * 8))) {
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
  const fileInputs = boundedElements(document, 'input[type="file"]', 12);
  const composer = inputs[0]?.closest('[data-node-type="input-area"], form') || document.querySelector('form[data-type="unified-composer"]') || document.querySelector('form');
  const tools = boundedElements(composer, 'button, [role="button"]', 64).filter(visible).slice(0, 32);
  const seenTools = new Set(tools);
  for (const element of boundedElements(document, '[role="menuitem"], [role="option"], toolbox-drawer-item [role="menuitemcheckbox"]', 128)) {
    if (visible(element) && !seenTools.has(element)) {
      tools.push(element);
      seenTools.add(element);
    }
    if (tools.length >= 32) break;
  }
  let responseCount = 0;
  let latest = null;
  for (const selector of selectors.response || []) {
    try {
      const found = document.querySelectorAll(selector);
      if (!found.length) continue;
      responseCount = found.length;
      latest = found.item ? found.item(found.length - 1) : found[found.length - 1];
      break;
    } catch (_) {}
  }
  const geminiTurns = document.querySelectorAll('user-query');
  const lastGeminiTurn = geminiTurns.item ? geminiTurns.item(geminiTurns.length - 1) : geminiTurns[geminiTurns.length - 1];
  const lastGeminiContent = lastGeminiTurn?.querySelector?.('[id^="user-query-content-"]');
  const lastGeminiText = String(lastGeminiContent?.textContent || lastGeminiTurn?.textContent || '').trim();
  const geminiResponseAfterTurn = Boolean(lastGeminiTurn && latest
    && typeof lastGeminiTurn.compareDocumentPosition === 'function'
    && (lastGeminiTurn.compareDocumentPosition(latest) & 4));
  const busySelectors = [
    ['aria_busy', '[aria-busy="true"]'],
    ['streaming_attribute', '[data-is-streaming="true"]'],
    ['streaming_class', '.result-streaming'],
    ['image_loading', '[data-testid="image-gen-loading-state"], [data-testid="image-gen-loading-state-frame"]'],
    ['stop_button', 'button[data-testid*="stop" i], button[aria-label*="stop" i], button[aria-label*="beenden" i], button[aria-label*="abbrechen" i]']
  ];
  const busyIndicators = busySelectors.flatMap(([name, selector]) => {
    try { return anyVisible(selector) ? [name] : []; } catch (_) { return []; }
  });
  const stopControls = boundedElements(document, 'button[data-testid*="stop" i], button[aria-label*="stop" i], button[aria-label*="beenden" i], button[aria-label*="abbrechen" i]', 64).filter(visible);
  const latestMarkdown = latest?.querySelector?.('message-content .markdown, [data-message-author-role="assistant"] .markdown');
  const latestText = String(latestMarkdown?.innerText || latestMarkdown?.textContent || latest?.innerText || '').trim();
  const latestImageNodes = latest?.querySelectorAll?.('img') || [];
  const latestImages = boundedElements(latest, 'img', 64);
  let latestResponseBusy = false;
  try { latestResponseBusy = Boolean(latest?.querySelectorAll?.('[aria-busy="true"], [data-is-streaming="true"], .result-streaming')?.length); } catch (_) {}
  let imageProgress = 0;
  for (const element of boundedElements(document, '[data-testid="image-gen-loading-progress"], [role="progressbar"][aria-valuenow]', 64)) {
    if (visible(element)) imageProgress = Math.max(imageProgress, Number(element.getAttribute('aria-valuenow')) || 0);
  }
  const relevant = /bild|image|musik|music|file|datei|ordner|folder|upload|attach|tool|werkzeug|auswahl von|selection of|deselect|gpt|gemini|modell|model|denk|reason|effort|hoch|high/i;
  // Bounded selector diagnostics for provider chrome only. Never include a
  // prompt, response, or arbitrary button label in heartbeat diagnostics.
  const modeName = /^(?:(?:gemini|gpt)[\s._-]*)?(?:\d+(?:\.\d+)?[\s._-]*)?(?:flash|pro|advanced|erweitert|schnell|fast|thinking|nachdenken|auto)(?:[\s._-]*(?:flash|pro|advanced|erweitert|preview))?$/i;
  const modelControls = boundedElements(document, 'button, [role="button"], [role="combobox"]', 256)
    .filter((element) => visible(element) && !element.closest?.('model-response, user-query, [data-message-author-role="assistant"], [data-message-author-role="user"], nav, [role="navigation"]'))
    .filter((element) => {
      const hint = `${element.id || ''} ${element.getAttribute('data-testid') || ''} ${element.getAttribute('data-test-id') || ''} ${element.getAttribute('aria-label') || ''}`;
      const label = short(element.innerText, 80);
      return /model|modell|mode|modus|switcher/i.test(hint) || modeName.test(label);
    }).slice(0, 12).map((element) => {
      const item = describe(element);
      const label = short(element.innerText, 80);
      item.text = modeName.test(label) ? label : '';
      if (!/model|modell|mode|modus|flash|pro|schnell|erweitert/i.test(item.aria_label)) item.aria_label = '';
      return item;
    });
  const inputCharacters = String(inputs[0]?.value || inputs[0]?.innerText || inputs[0]?.textContent || '').trim().length;
  return {
    captured_at: new Date().toISOString(),
    page_visibility: ['visible', 'hidden', 'prerender'].includes(document.visibilityState) ? document.visibilityState : 'unknown',
    was_discarded: document.wasDiscarded === true,
    inputs: inputs.map((element) => describe(element)),
    input_has_text: inputCharacters > 0,
    input_characters: Math.min(inputCharacters, 100000),
    submit: submit.map((element) => describe(element)),
    file_inputs: fileInputs.map((element) => describe(element)),
    tools: tools.slice(0, 32).map((element) => {
      const item = describe(element, true);
      if (!relevant.test(`${item.text} ${item.aria_label} ${item.test_id}`) && item.has_popup !== 'menu') item.text = '';
      return item;
    }),
    model_controls: modelControls,
    assistant_turns: Math.min(responseCount, 10000),
    gemini_user_turns: Math.min(geminiTurns.length, 10000),
    gemini_last_user_turn_characters: Math.min(lastGeminiText.length, 100000),
    gemini_last_user_turn_has_content_id: Boolean(lastGeminiContent?.id),
    gemini_response_after_last_user_turn: geminiResponseAfterTurn,
    last_response_characters: Math.min(latestText.length, 100000),
    last_response_busy: latestResponseBusy,
    busy_indicators: busyIndicators,
    stop_button_disabled: Boolean(stopControls.length && stopControls.every((button) => button.disabled || button.getAttribute('aria-disabled') === 'true')),
    stop_button_spinning: stopControls.some((button) => Boolean(button.querySelector('[class*="spin" i], [role="progressbar"], svg[aria-label*="loading" i]'))),
    last_response_images: Math.min(latestImageNodes.length, 10000),
    last_response_loaded_images: latestImages.filter((image) => image.complete && image.naturalWidth >= 128 && image.naturalHeight >= 128).length,
    image_progress: Math.max(0, Math.min(100, imageProgress))
  };
}

async function discoverPageCapabilities() {
  const visible = (element) => Boolean(element && (element.offsetWidth || element.offsetHeight || element.getClientRects().length));
  const text = (element) => String(element?.innerText || element?.textContent || element?.getAttribute?.('aria-label') || '').replace(/\s+/g, ' ').trim().slice(0, 100);
  const unique = (values, limit) => [...new Set(values.filter(Boolean))].slice(0, limit);
  const wait = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
  const pointerDown = (element) => {
    if (typeof PointerEvent !== 'function') return false;
    element.dispatchEvent(new PointerEvent('pointerdown', {
      bubbles: true, cancelable: true, button: 0, buttons: 1, pointerType: 'mouse', isPrimary: true
    }));
    return true;
  };
  const mouseDown = (element) => {
    if (typeof MouseEvent !== 'function') return false;
    element.dispatchEvent(new MouseEvent('mousedown', { bubbles: true, cancelable: true, button: 0, buttons: 1 }));
    return true;
  };
  const modelPattern = /^(?:gpt[\s._-]*\d+(?:\.\d+)?(?:[\s._-]*(?:astra|sol|terra|luna|pro|mini|nano|codex|thinking|instant))?|gemini(?:[\s._-]*\d+(?:\.\d+)?(?:[\s._-]*(?:pro|flash|lite|thinking|preview|experimental))*)?|astra|sol|terra|luna|\d+(?:\.\d+)?\s+(?:pro|flash|astra|sol|terra|luna))$/i;
  const reasoningPattern = /^(?:instant|sofort|fast|schnell|low|niedrig|medium|mittel|high|hoch|very high|sehr hoch|xhigh|pro|max|maximum)$/i;
  const semantic = (element, pattern) => pattern.test(`${element.getAttribute('data-testid') || ''} ${element.getAttribute('aria-label') || ''}`);
  const modelOptionLabel = (element) => {
    const lines = String(element?.innerText || element?.textContent || '').split('\n').map((line) => line.replace(/\s+/g, ' ').trim());
    return lines.find((line) => modelPattern.test(line)) || text(element);
  };
  const checked = (element) => element.getAttribute('aria-checked') === 'true'
    || element.getAttribute('aria-selected') === 'true' || element.getAttribute('aria-current') === 'true'
    || element.getAttribute('data-selected') === 'true' || ['checked', 'selected'].includes(element.getAttribute('data-state'))
    || Boolean(element.querySelector?.('svg use[href*="#check" i], svg[data-testid*="check" i], [data-state="checked"], [class*="check" i]'));
  const modelControl = (element) => semantic(element, /model[-_ ]?(?:switcher|selector|picker|menu)|(?:choose|select|current|switch)[-_ ]?model|modellauswahl|modellmenü|modellmodus|modell[-_ ]?wechseln/i);
  const geminiPage = globalThis.location?.hostname === 'gemini.google.com';
  const geminiModeName = /^(?:gemini\s*)?(?:\d+(?:\.\d+)?\s*)?(?:flash|pro|thinking|schnell|erweitert)(?:[ -](?:lite|erweitert|advanced|preview))?$/i;
  const geminiPicker = document.querySelector?.('bard-mode-switcher button[aria-haspopup]')
    || (geminiPage ? [...document.querySelectorAll('button, [role="button"]')].filter(visible)
      .filter((element) => !element.closest?.('model-response, user-query, [role="menu"], [role="navigation"], nav'))
      .map((element) => {
        const hint = `${element.tagName || ''} ${element.id || ''} ${element.className || ''} ${element.getAttribute('aria-label') || ''} ${element.getAttribute('data-testid') || ''} ${element.parentElement?.tagName || ''} ${element.parentElement?.className || ''}`;
        const selected = /(?:derzeit ausgewählt|currently selected|selected)\s*:\s*(.+)$/i.exec(element.getAttribute('aria-label') || '')?.[1]?.trim() || '';
        const semantic = /(?:model|modell|mode|modus)[-_ ]?(?:switcher|selector|picker|menu)|(?:model|modell|mode|modus)auswahl/i.test(hint);
        return { element, score: (semantic ? 4 : 0) + (geminiModeName.test(selected) ? 4 : 0)
          + (geminiModeName.test(text(element)) ? 2 : 0) + (element.getAttribute('aria-haspopup') ? 2 : 0) };
      }).filter((item) => item.score >= 4).sort((a, b) => b.score - a.score)[0]?.element : null);
  if (geminiPicker && visible(geminiPicker)) {
    const selected = geminiPicker.getAttribute('aria-label')?.match(/(?:derzeit ausgewählt|currently selected|selected)\s*:\s*(.+)$/i)?.[1]?.trim() || '';
    const currentModel = (geminiModeName.test(selected) ? selected : geminiModeName.test(text(geminiPicker)) ? text(geminiPicker) : '').slice(0, 100);
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
    const models = unique(choices.map(modelLabel).filter((value) => geminiModeName.test(value)), 50);
    geminiPicker.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', code: 'Escape', bubbles: true }));
    await wait(150);
    return { currentModel, currentReasoning: '', models, reasoningLevels: [],
      scanDiagnostic: { model: `other trigger, expanded=${geminiPicker.getAttribute('aria-expanded') === 'true'}, submenu=false, ${choices.length} candidates, open=click`, reasoning: 'no trigger (0 composer menus)' } };
  }
  if (geminiPage) return { currentModel: '', currentReasoning: '', models: [], reasoningLevels: [],
    scanDiagnostic: { model: 'no trigger (0 composer menus)', reasoning: 'no trigger (0 composer menus)' } };
  const scan = async (kind) => {
    const pattern = kind === 'model' ? modelPattern : reasoningPattern;
    const composerInput = document.querySelector('#prompt-textarea, rich-textarea [contenteditable="true"][role="textbox"], div.ql-editor[contenteditable="true"][role="textbox"]');
    const composer = composerInput?.closest?.('[data-node-type="input-area"], form')
      || document.querySelector('form[data-type="unified-composer"]') || document.querySelector('form');
    const composerMenus = [...(composer?.querySelectorAll?.('button[aria-haspopup="menu"]') || [])]
      .filter(visible).filter((element) => !/composer-plus|add.files|dateien.*hinzufügen/i.test(
        `${element.id || ''} ${element.getAttribute('data-testid') || ''} ${element.getAttribute('aria-label') || ''}`));
    const composerPills = [...document.querySelectorAll('button.__composer-pill[aria-haspopup="menu"]')].filter(visible);
    const triggers = [...document.querySelectorAll('button, [role="button"]')].filter(visible).filter((element) => {
      const label = `${text(element)} ${element.getAttribute('aria-label') || ''}`;
      if (/modelle ergänzen|add models|preismodell|pricing model/i.test(label)) return false;
      if (kind === 'model') return modelControl(element) || modelPattern.test(text(element));
      return semantic(element, /reason|denk|effort|thinking/i) || reasoningPattern.test(text(element));
    });
    // ChatGPT's current composer can expose only the reasoning pill. Its menu
    // also contains the model choices; "Modell wechseln" on an older answer
    // would inspect that answer instead of the model for the next prompt.
    const composerTrigger = composerPills.find((element) =>
      reasoningPattern.test(text(element)) || /^(?:denkaufwand|reasoning|effort)$/i.test(text(element))
      || semantic(element, /reason|denk|effort|thinking|model|modell/i))
      || (composerPills.length === 1 ? composerPills[0] : null)
      || composerMenus.find((element) =>
        reasoningPattern.test(text(element)) || /denkaufwand|reason|effort|model|modell/i.test(
          `${text(element)} ${element.getAttribute('aria-label') || ''}`))
      || (composerMenus.length === 1 ? composerMenus[0] : null);
    const trigger = (kind === 'model' && composerTrigger)
      || (kind === 'reasoning' && composerTrigger)
      || triggers.find((element) => kind === 'model' && modelControl(element)) || triggers[0];
    if (!trigger) return { current: '', values: [], diagnostic: `no trigger (${composerMenus.length} composer menus)` };
    let current = pattern.test(text(trigger)) ? text(trigger) : '';
    const alreadyOpen = trigger.getAttribute('aria-expanded') === 'true';
    const candidateSelector = 'button, [role="button"], [role="menuitem"], [role="menuitemradio"], [role="option"], [data-radix-collection-item], [aria-checked], [aria-selected], li';
    const visibleBefore = new Set([...document.querySelectorAll(candidateSelector)].filter(visible));
    const label = kind === 'model' ? modelOptionLabel : text;
    const menuCandidates = () => [...document.querySelectorAll(candidateSelector)]
      .filter((element) => element !== trigger && visible(element) && (alreadyOpen || !visibleBefore.has(element)));
    let openMethod = alreadyOpen ? 'already' : '';
    if (!alreadyOpen) {
      // Radix dropdown triggers respond to pointerdown, not a synthetic click.
      // Fall back to click for sites with an ordinary click handler.
      if (pointerDown(trigger)) { openMethod = 'pointer'; await wait(100); }
      if (trigger.getAttribute('aria-expanded') !== 'true' && menuCandidates().length === 0 && mouseDown(trigger)) {
        openMethod = 'mouse';
        await wait(100);
      }
      if (trigger.getAttribute('aria-expanded') !== 'true' && menuCandidates().length === 0) {
        openMethod = 'click';
        trigger.click();
      }
    }
    let choices = [];
    // Radix and ChatGPT's composer menus can render asynchronously. Capture
    // only exact model/effort labels, not messages or arbitrary page content.
    for (let attempt = 0; attempt < 12; attempt++) {
      if (attempt || !alreadyOpen) await wait(150);
      choices = menuCandidates();
      if (choices.some((element) => pattern.test(label(element)))) break;
    }
    let submenuOpened = false;
    if (kind === 'model' && !choices.some((element) => modelPattern.test(modelOptionLabel(element)))) {
      // In the current ChatGPT UI the first popup contains a "5.6 Sehr hoch >"
      // header and a reasoning slider. That header opens the model list. Only
      // click a newly exposed navigation control, never a model choice.
      const versionAndEffort = /^(?:gpt[- ]?)?\d+(?:\.\d+)?\s+(?:sehr hoch|hoch|mittel|niedrig|sofort|very high|high|medium|low|instant|fast)(?:\s*[›>→])?$/i;
      const submenu = choices.find((element) => !modelPattern.test(modelOptionLabel(element))
        && (versionAndEffort.test(text(element))
          || (element.getAttribute('aria-haspopup') && /^(?:model|modell|modelle)$/i.test(text(element)))));
      if (submenu) {
        if (pointerDown(submenu)) await wait(100);
        if (!menuCandidates().some((element) => modelPattern.test(modelOptionLabel(element))) && mouseDown(submenu)) await wait(100);
        if (!menuCandidates().some((element) => modelPattern.test(modelOptionLabel(element)))) submenu.click();
        submenuOpened = true;
        for (let attempt = 0; attempt < 12; attempt++) {
          await wait(150);
          choices = menuCandidates();
          if (choices.some((element) => modelPattern.test(modelOptionLabel(element)))) break;
        }
      }
    }
    const menuExpanded = trigger.getAttribute('aria-expanded') === 'true';
    const selected = choices.find((element) => checked(element) && pattern.test(label(element)));
    if (selected) current = label(selected);
    const values = unique(choices.map(label).filter((value) => pattern.test(value)), kind === 'model' ? 50 : 20);
    if (!alreadyOpen) {
      document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', code: 'Escape', bubbles: true }));
      await wait(100);
      if (trigger.getAttribute('aria-expanded') === 'true' && pointerDown(trigger)) await wait(100);
      if (trigger.getAttribute('aria-expanded') === 'true') trigger.click();
    }
    return { current, values, diagnostic: `${composerTrigger === trigger ? 'composer' : 'other'} trigger, expanded=${menuExpanded}, submenu=${submenuOpened}, ${choices.length} candidates, open=${openMethod}` };
  };
  const models = await scan('model');
  const reasoning = await scan('reasoning');
  return {
    currentModel: models.current, currentReasoning: reasoning.current,
    models: models.values, reasoningLevels: reasoning.values,
    scanDiagnostic: { model: models.diagnostic, reasoning: reasoning.diagnostic }
  };
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
