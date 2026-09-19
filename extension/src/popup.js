const api = globalThis.browser || globalThis.chrome;
// The V10 surface intentionally uses human-facing IDs. Keep the proven
// controller behind it through a small semantic alias layer instead of
// duplicating security-sensitive attach, permission, and pairing logic.
const visibleControl = {
  version: 'version', url: 'bridge-url', token: 'pairing-token', 'toggle-token': 'show-token',
  'scan-capabilities': 'scan-model', teach: 'customize-detection', test: 'test-service',
  'select-ai-tabs': 'attach-fresh', 'all-tabs': 'show-all-tabs', stop: 'disconnect-button',
  'release-session-tab': 'release-session'
};
const $ = (id) => document.getElementById(visibleControl[id] || id);
let currentTab = null;
let currentProfile = null;
let attachedTabIDs = [];
let liveTabState = new Map();
let connectPending = false;

document.addEventListener('DOMContentLoaded', initialize);
api.storage.onChanged?.addListener((changes, area) => {
  if (area !== 'local') return;
  if (changes.tabIds) attachedTabIDs = [...new Set((changes.tabIds.newValue || []).map(Number).filter(Boolean))];
  if (changes.connectionProgress && connectPending) showConnectionProgress(changes.connectionProgress.newValue);
  if (changes.tabIds || changes.tabCapabilities || changes.tabCooldowns || changes.tabFailures || changes.lastError || changes.connectionError || changes.running || changes.relayConnected) void refreshLiveTabs(Boolean(changes.tabIds || changes.running || changes.connectionError || changes.relayConnected));
});

async function initialize() {
  $('version').textContent = `v${api.runtime.getManifest().version}`;
  const saved = await settings();
  $('url').value = saved.bridgeUrl;
  $('token').value = saved.token;
  $('visual-mode').checked = saved.useVisualProfile;
  $('auto-reconnect').checked = saved.autoReconnect;
  $('auto-attach-fresh').checked = saved.autoAttachFreshTabs;
  $('preserve-drafts').checked = saved.preserveDrafts;
  $('auto-close-finished').checked = saved.autoCloseFinishedChats;
  $('session-mode').value = saved.sessionMode;
  attachedTabIDs = [...new Set((saved.tabIds?.length ? saved.tabIds : [saved.tabId]).map(Number).filter(Boolean))];
  toggleProfileMode();
  await loadTabs(0, false);
  await loadProfiles(saved);
  await refreshState();
  await refreshUpdatePreference(saved);
  setInterval(() => { if (!document.hidden) void refreshLiveTabs(true); }, 2500);
}

async function refreshUpdatePreference(saved = null) {
  const toggle = $('auto-update');
  toggle.disabled = true;
  const cfg = saved || await settings();
  if (!cfg.token) {
    toggle.checked = false;
    $('auto-update-state').textContent = 'Off by default. Connect to change this PC\'s setting.';
    return;
  }
  try {
    const response = await fetch(`${cfg.bridgeUrl}/v1/settings/updates`, {
      headers: { Authorization: `Bearer ${cfg.token}` }, cache: 'no-store'
    });
    if (!response.ok) throw new Error('Local update setting unavailable');
    const status = await response.json();
    toggle.checked = Boolean(status.enabled);
    toggle.disabled = false;
    $('auto-update-state').textContent = status.enabled
      ? 'On for this PC. Changes install only while it is idle.'
      : 'Off for this PC. Manual updates still work.';
  } catch (_) {
    $('auto-update-state').textContent = 'Connect to the local service to manage updates.';
  }
}

$('auto-update').addEventListener('change', async () => {
  const toggle = $('auto-update');
  const requested = toggle.checked;
  toggle.disabled = true;
  try {
    const cfg = await settings();
    const response = await fetch(`${cfg.bridgeUrl}/v1/settings/updates`, {
      method: 'PUT',
      headers: { Authorization: `Bearer ${cfg.token}`, 'Content-Type': 'application/json' },
      body: JSON.stringify({ enabled: requested })
    });
    if (!response.ok) throw new Error('Local update setting could not be saved');
    const status = await response.json();
    if (Boolean(status.enabled) !== requested) throw new Error('The device config locks automatic updates off');
    toggle.checked = Boolean(status.enabled);
    $('auto-update-state').textContent = status.enabled
      ? 'On for this PC. Changes install only while it is idle.'
      : 'Off for this PC. Manual updates still work.';
  } catch (error) {
    toggle.checked = !requested;
    setStatus('error', error.message || String(error));
  } finally {
    toggle.disabled = false;
  }
});

async function refreshLiveTabs(refreshProfile = false) {
  try {
    await loadTabs(primaryTabID(), await hasTabsPermission());
    if (refreshProfile) await refreshState();
  } catch (_) {}
}

$('tab-filter').addEventListener('change', () => loadTabs(primaryTabID(), false));
$('select-ai-tabs').addEventListener('click', async () => {
  try {
    const granted = await api.permissions.request({ permissions: ['tabs'] });
    if (!granted) throw new Error('Tab access was not granted');
    const allTabs = await api.tabs.query({});
    const candidates = allTabs.filter((tab) => tab.id && globalThis.ContextBridgeProfiles?.forURL(tab.url || '') && isFreshChatURL(tab.url));
    if (!candidates.length) throw new Error('No new, empty ChatGPT or Gemini chat was found. Allow an existing tab explicitly if you want to use it.');
    const origins = [...new Set(candidates.map((tab) => permissionPattern(tab.url)))];
    if (!await api.permissions.request({ origins })) throw new Error('Page access was not granted');
    const fresh = [];
    for (const tab of candidates) {
      const result = await api.runtime.sendMessage({ type: 'check-tab-freshness', tabId: tab.id });
      if (result?.fresh) fresh.push({ ...tab, fresh: true });
    }
    const selected = selectStarterTabs(fresh);
    if (!selected.length) throw new Error('No confirmed empty chat is ready. Attach an existing chat explicitly from its page or the tab list.');
    await blockAutoAttach(selected.map((tab) => tab.id), false);
    await setAttachedTabIDs([...attachedTabIDs, ...selected.map((tab) => tab.id)]);
    await loadTabs(selected[0].id, true);
    await refreshState();
    setStatus((await settings()).running ? 'live' : 'idle', `${selected.length} fresh AI tab${selected.length === 1 ? '' : 's'} attached`);
  } catch (error) {
    setStatus('error', error.message || String(error));
  }
});

function selectStarterTabs(tabs) {
  const supported = tabs.filter((tab) => tab.id && tab.fresh === true && globalThis.ContextBridgeProfiles?.forURL(tab.url || ''));
  const choose = (profile) => supported
    .filter((tab) => globalThis.ContextBridgeProfiles.forURL(tab.url).name === profile)
    .sort((a, b) => Number(b.active) - Number(a.active) || Number(b.lastAccessed || 0) - Number(a.lastAccessed || 0) || b.id - a.id)[0];
  return [choose('chatgpt'), choose('gemini')].filter(Boolean);
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

$('toggle-selected-tab').addEventListener('click', async () => {
  try {
    const tabId = primaryTabID();
    if (!tabId) throw new Error('Choose a tab first');
    const wasAttached = attachedTabIDs.includes(tabId);
    if (wasAttached) await detachTab(tabId);
    else await attachTab(tabId);
    await loadTabs(tabId, await hasTabsPermission());
    await refreshState();
    setStatus((await settings()).running ? 'live' : 'idle', wasAttached
      ? 'Selected tab detached; no new jobs will be sent to it'
      : 'Selected tab attached');
  } catch (error) { setStatus('error', error.message || String(error)); }
});

$('toggle-current-tab').addEventListener('click', async () => {
  try {
    // Resolve the actual page under the popup, not the selected list row.
    const tab = await currentPageTab();
    if (!tab?.id || !/^https?:/i.test(tab.url || '')) throw new Error('Open this popup on an AI web page first');
    const attached = attachedTabIDs.includes(tab.id);
    if (attached) await detachTab(tab.id);
    else await attachTab(tab.id);
    await loadTabs(tab.id, await hasTabsPermission());
    await refreshState();
    setStatus((await settings()).running ? 'live' : 'idle', attached
      ? 'Current page detached; no new jobs will be sent to it'
      : 'Current page attached');
  } catch (error) { setStatus('error', error.message || String(error)); }
});

async function checkedAttachTab(tabId) {
  if (!tabId) throw new Error('Choose a tab first');
  const tab = await withPopupDeadline(api.tabs.get(tabId), 2000);
  if (!/^https?:/i.test(tab.url || '')) throw new Error('Only web pages can be attached');
  const saved = await settings();
  if (saved.useVisualProfile && !saved.taughtProfiles[new URL(tab.url).origin] && !globalThis.ContextBridgeProfiles?.forURL(tab.url)) {
    throw new Error('Teach this page before attaching it');
  }
  return tab;
}

async function attachTab(tabId, permissionGranted = false) {
  if (attachedTabIDs.includes(tabId)) return;
  if (attachedTabIDs.length >= 16) throw new Error('The 16-tab safety limit is reached; detach a tab first');
  const tab = await checkedAttachTab(tabId);
  if (!permissionGranted && !await api.permissions.request({ origins: [permissionPattern(tab.url)] })) {
    throw new Error('Page access was not granted');
  }
  await blockAutoAttach([tabId], false);
  await setAttachedTabIDs([...attachedTabIDs, tabId]);
}

async function detachTab(tabId) {
  if (!tabId || !attachedTabIDs.includes(tabId)) return;
  await blockAutoAttach([tabId], true);
  await setAttachedTabIDs(attachedTabIDs.filter((id) => id !== tabId));
}

async function currentPageTab() {
  const tabs = await withPopupDeadline(api.tabs.query({ active: true, currentWindow: true }), 2000);
  return tabs.find((tab) => tab.id) || null;
}

async function scanTargetTabID() {
  const selected = primaryTabID();
  const selectedTab = selected ? await api.tabs.get(selected).catch(() => null) : null;
  if (selectedTab?.id && globalThis.ContextBridgeProfiles?.forURL(selectedTab.url || '')) return selectedTab.id;
  const current = await currentPageTab();
  if (current?.id && globalThis.ContextBridgeProfiles?.forURL(current.url || '')) return current.id;
  throw new Error('Open a ChatGPT or Gemini tab before scanning model choices');
}

$('detach-all').addEventListener('click', async () => {
  await blockAutoAttach(attachedTabIDs, true);
  await setAttachedTabIDs([]);
  await loadTabs(primaryTabID(), await hasTabsPermission());
  await refreshState();
  setStatus('idle', 'All tabs detached');
});

$('auto-attach-fresh').addEventListener('change', async () => {
  await api.storage.local.set({ autoAttachFreshTabs: $('auto-attach-fresh').checked });
  if ($('auto-attach-fresh').checked) await api.runtime.sendMessage({ type: 'discover-fresh-tabs' });
});
$('auto-reconnect').addEventListener('change', async () => {
  await api.storage.local.set({ autoReconnect: $('auto-reconnect').checked });
});
$('preserve-drafts').addEventListener('change', async () => {
  await api.storage.local.set({ preserveDrafts: $('preserve-drafts').checked });
});
$('auto-close-finished').addEventListener('change', async () => {
  await api.storage.local.set({ autoCloseFinishedChats: $('auto-close-finished').checked });
});
$('session-mode').addEventListener('change', async () => {
  const mode = $('session-mode').value;
  await api.storage.local.set({ sessionMode: ['new_chat', 'new_chat_per_job'].includes(mode) ? mode : 'manual' });
});
$('release-session-tab').addEventListener('click', async () => {
  try {
    const tab = await currentPageTab();
    const result = await api.runtime.sendMessage({ type: 'release-session-tab', tabId: tab?.id });
    if (!result?.ok) throw new Error(result?.error || 'This page could not be released');
    setStatus('live', 'Empty chat ready for the next session');
  } catch (error) { setStatus('error', error.message || String(error)); }
});
$('edit-last-message').addEventListener('change', async () => {
  const toggle = $('edit-last-message');
  const requested = toggle.checked;
  toggle.disabled = true;
  try {
    const tab = await currentPageTab();
    const result = await api.runtime.sendMessage({ type: 'set-tab-edit-mode', tabId: tab?.id, enabled: requested });
    if (!result?.ok) throw new Error(result?.error || 'Could not change this tab');
    setStatus('live', requested ? 'This page will edit its previous ContextBridge text prompt' : 'This page will send new messages');
  } catch (error) {
    toggle.checked = !requested;
    setStatus('error', error.message || String(error));
  } finally { toggle.disabled = false; }
});
$('all-tabs').addEventListener('click', async () => {
  const granted = await api.permissions.request({ permissions: ['tabs'] });
  if (!granted) return setStatus('error', 'Tab access was not granted');
  await loadTabs(primaryTabID(), true);
  $('all-tabs').hidden = true;
});
$('tab').addEventListener('change', async () => {
  await refreshState();
});
$('visual-mode').addEventListener('change', async () => {
  await api.storage.local.set({ useVisualProfile: $('visual-mode').checked });
  toggleProfileMode();
  await refreshState();
});
$('profile').addEventListener('change', saveInputs);

$('teach').addEventListener('click', async () => {
  try {
    const tabId = primaryTabID();
    const tab = await api.tabs.get(tabId);
    const origins = [permissionPattern(tab.url), permissionPattern($('url').value)];
    const granted = await api.permissions.request({ origins });
    if (!granted) return setStatus('error', 'Page access was not granted');
    await saveInputs();
    const result = await api.runtime.sendMessage({ type: 'start-teaching', tabId });
    if (!result?.ok) throw new Error(result?.error || 'Teaching mode could not start');
    setStatus('idle', 'Teaching in selected tab');
    window.close();
  } catch (error) {
    setStatus('error', error.message || String(error));
  }
});

$('verify').addEventListener('click', async () => {
  try {
    const tab = await api.tabs.get(primaryTabID());
    const granted = await api.permissions.request({ origins: [permissionPattern(tab.url)] });
    if (!granted) return setStatus('error', 'Page access was not granted');
    const result = await api.runtime.sendMessage({ type: 'verify-profile', tabId: primaryTabID() });
    if (!result?.ok) throw new Error(result?.error || 'One or more targets are missing');
    const image = result.report.file_input ? 'image ready' : 'text only';
    const response = result.report.response_pending ? 'response target ready after the first answer' : 'response found';
    renderProfile(currentProfile, true, `Prompt and send ready; ${response}. ${image}.`);
    setStatus('live', 'Profile is ready');
  } catch (error) {
    renderProfile(currentProfile, false, error.message || String(error));
    setStatus('error', 'Profile needs attention');
  }
});

$('scan-capabilities').addEventListener('click', async () => {
  try {
    const result = await api.runtime.sendMessage({ type: 'scan-capabilities', tabId: await scanTargetTabID() });
    if (!result?.ok) throw new Error(result?.error || 'Could not inspect model choices');
    const models = result.capabilities?.models || [];
    const levels = result.capabilities?.reasoningLevels || [];
    const diagnostic = models.length ? '' : ` · ${result.capabilities?.scanDiagnostic?.model || 'no scan detail'}`;
    setStatus('live', `${models.length} model choice${models.length === 1 ? '' : 's'} · ${levels.length} reasoning level${levels.length === 1 ? '' : 's'}${diagnostic}`);
  } catch (error) {
    setStatus('error', error.message || String(error));
  }
});

$('pair').addEventListener('click', async () => {
  if (connectPending) return;
  connectPending = true;
  $('pair').disabled = true;
  $('pair').setAttribute('aria-busy', 'true');
  $('pair').textContent = 'Connecting…';
  setStatus('connecting', 'Connecting to the local service…');
  try {
    await api.storage.local.set({ connectionError: '', lastError: '' });
    const reconciled = await api.runtime.sendMessage({ type: 'reconcile-tabs' });
    if (!reconciled?.ok) throw new Error(reconciled?.error || 'Could not check saved AI tabs');
    attachedTabIDs = [...new Set((reconciled.tabIds || []).map(Number).filter(Boolean))];
    let tabs = Array.isArray(reconciled.tabs) ? reconciled.tabs : [];
    const removedClosedTabs = Number(reconciled.removed?.length || 0);
    let attachCurrentID = 0;
    if (!selectedTabIDs().length) {
      const current = await currentPageTab();
      if (!current?.id) throw new Error('Open a ChatGPT or Gemini page first');
      const tab = await checkedAttachTab(current.id);
      tabs = [tab];
      attachCurrentID = tab.id;
    }
    const origins = [...new Set([...tabs.map((tab) => permissionPattern(tab.url)), ...mediaOriginsFor(tabs), permissionPattern($('url').value)])];
    const granted = await api.permissions.request({ origins });
    if (!granted) throw new Error('Connection access was not granted');
    if (attachCurrentID) {
      await attachTab(attachCurrentID, true);
      await loadTabs(attachCurrentID, await hasTabsPermission());
    }
    await saveInputs();
    const result = await api.runtime.sendMessage({ type: 'start' });
    if (!result?.ok) throw new Error(result?.error || 'Connection could not start');
    renderRunning(true);
    const repaired = removedClosedTabs ? ` · removed ${removedClosedTabs} closed tab${removedClosedTabs === 1 ? '' : 's'}` : '';
    setStatus('live', `Connected · ${tabs.length} tab${tabs.length === 1 ? '' : 's'} registered${repaired} · scanning page controls in background`);
    await refreshUpdatePreference();
  } catch (error) {
    const message = error.message || String(error);
    await api.storage.local.set({ connectionError: message });
    setStatus('error', message);
  } finally {
    connectPending = false;
    $('pair').removeAttribute('aria-busy');
    $('pair').disabled = false;
    renderRunning((await settings()).running);
  }
});

$('stop').addEventListener('click', async () => {
  await api.runtime.sendMessage({ type: 'stop' });
  await api.storage.local.set({ connectionError: '', lastError: '' });
  renderRunning(false);
  setStatus('idle', 'Connection stopped');
});

$('test').addEventListener('click', async () => {
  try {
    const bridgeOrigin = permissionPattern($('url').value);
    const granted = await api.permissions.request({ origins: [bridgeOrigin] });
    if (!granted) return setStatus('error', 'Local service access was not granted');
    await saveInputs();
    const result = await api.runtime.sendMessage({ type: 'test' });
    if (!result?.ok) throw new Error(result?.error || 'Local service unavailable');
    setStatus('live', `Local service ${result.version || ''} is ready`);
    await loadProfiles(await settings());
  } catch (error) {
    setStatus('error', error.message || String(error));
  }
});

$('toggle-token').addEventListener('click', () => {
  const visible = $('token').type === 'text';
  $('token').type = visible ? 'password' : 'text';
  $('toggle-token').textContent = visible ? 'Show' : 'Hide';
});

$('remove-profile').addEventListener('click', async () => {
  if (!currentTab?.origin) return;
  const result = await api.runtime.sendMessage({ type: 'remove-profile', origin: currentTab.origin });
  if (!result?.ok) return setStatus('error', result?.error || 'Profile could not be removed');
  currentProfile = null;
  renderProfile(null);
  updateActions(false, false);
  setStatus('idle', 'Page profile removed');
});

$('export-profile').addEventListener('click', async () => {
  if (!currentProfile) return;
  const yaml = profileYAML(currentProfile);
  try {
    await navigator.clipboard.writeText(yaml);
  } catch (_) {
    const textarea = document.createElement('textarea');
    textarea.value = yaml;
    document.body.append(textarea);
    textarea.select();
    document.execCommand('copy');
    textarea.remove();
  }
  setStatus('live', 'YAML profile copied');
});

async function saveInputs() {
  const tabIds = selectedTabIDs();
  await api.storage.local.set({
    bridgeUrl: $('url').value.trim().replace(/\/$/, ''),
    token: $('token').value.trim(),
    profile: $('profile').value,
    tabId: tabIds[0] || 0,
    tabIds,
    useVisualProfile: $('visual-mode').checked
  });
}

async function loadTabs(selected, allWindows) {
  let tabs = [];
  try {
    if (allWindows || await hasTabsPermission()) tabs = await api.tabs.query({});
    else tabs = await api.tabs.query({ active: true, currentWindow: true });
  } catch (_) {
    tabs = await api.tabs.query({ active: true, currentWindow: true });
  }
  const filter = $('tab-filter').value || 'all';
  const eligible = filterTabList(tabs, filter, attachedTabIDs);
  const saved = await settings();
  const bindings = Object.values(saved.sessionBindings || {});
  let runtimeTabs = new Map();
  try {
    const status = await api.runtime.sendMessage({ type: 'status' });
    runtimeTabs = new Map((status?.tabs || []).map((tab) => [tab.id, tab]));
  } catch (_) {}
  liveTabState = runtimeTabs;
  const page = await currentPageTab();
  const focusedID = preferredTabID(selected, eligible, page?.id, attachedTabIDs);
  const desired = eligible.map((tab) => {
    const host = safeHost(tab.url);
    const windowLabel = allWindows ? `W${tab.windowId}  ` : '';
    const attached = attachedTabIDs.includes(tab.id);
    const live = runtimeTabs.get(tab.id);
    const state = tabDisplayState(attached, live?.state);
    const reservation = bindings.find((entry) => Number(entry?.tabId) === tab.id);
    const session = reservation ? (reservation.legacy ? ' · old chat: open a new one' : ` · session: ${reservation.label || 'reserved'}`) : '';
    const model = attached && live?.currentModel ? ` · ${live.currentModel}` : '';
    const edit = attached && saved.tabEditModes?.[tab.id] ? ' · edits last' : '';
    return { id: tab.id, label: `${attached ? '●' : '○'} ${state}${session}${model}${edit} · ${windowLabel}${tab.title || 'Untitled'}  |  ${host}` };
  });
  const existing = [...$('tab').options];
  if (existing.length !== desired.length || desired.some((item, index) => existing[index]?.value !== String(item.id) || existing[index]?.textContent !== item.label)) {
    const scrollTop = $('tab').scrollTop;
    $('tab').replaceChildren(...desired.map((item) => {
      const option = document.createElement('option');
      option.value = String(item.id);
      option.textContent = item.label;
      option.selected = focusedID === item.id;
      return option;
    }));
    $('tab').scrollTop = scrollTop;
  }
  if (!$('tab').selectedOptions.length && eligible[0]) $('tab').options[0].selected = true;
  $('all-tabs').hidden = await hasTabsPermission();
  describeSelectedTab();
  await updateCurrentPageAction();
  $('attached-summary').textContent = attachedTabIDs.length
    ? `${attachedTabIDs.length} AI tab${attachedTabIDs.length === 1 ? '' : 's'} attached · manage other tabs below`
    : 'No AI tabs attached yet.';
}

async function updateCurrentPageAction() {
  let tab = null;
  try { tab = await currentPageTab(); } catch (_) {}
  const available = Boolean(tab?.id && /^https?:/i.test(tab.url || ''));
  const saved = await settings();
  const origin = available ? new URL(tab.url).origin : '';
  const profile = available ? saved.taughtProfiles[origin] || globalThis.ContextBridgeProfiles?.forURL(tab.url) : null;
  const attached = available && attachedTabIDs.includes(tab.id);
  const binding = Object.values(saved.sessionBindings || {}).find((entry) => Number(entry?.tabId) === tab?.id);
  $('current-page').classList.toggle('ready', Boolean(profile));
  $('current-provider').textContent = profile
    ? `${String(profile.label || profile.name || 'AI page').replace(/ \(auto-detected\)$/, '')} detected`
    : 'No supported AI page detected';
  $('toggle-current-tab').disabled = !available || (!attached && ((!profile && saved.useVisualProfile) || attachedTabIDs.length >= 16));
  $('toggle-current-tab').textContent = attached ? 'Detach this page' : 'Attach this page';
  $('release-session-tab').hidden = !attached;
  const canEdit = attached && ['chatgpt', 'gemini'].includes(profile?.name);
  $('edit-last-message-row').hidden = !canEdit;
  $('edit-last-message-hint').hidden = !canEdit;
  $('edit-last-message').checked = canEdit && saved.tabEditModes?.[tab.id] === true;
  $('edit-last-message').disabled = !canEdit || liveTabState.get(tab?.id)?.busy === true;
  $('current-tab-state').textContent = !available
    ? 'Open ChatGPT or Gemini to connect it.'
    : `${tab.title || safeHost(tab.url)} · ${attached ? (binding?.legacy ? 'old chat; open a new empty chat' : binding ? `session ${binding.label || 'reserved'}` : (saved.running ? 'connected' : 'attached, connection stopped')) : (profile ? 'ready' : 'teach this page in Advanced setup')}`;
  renderSessionAdvisory('session-health', attached ? liveTabState.get(tab?.id) : null);
}

function filterTabList(tabs, filter, attachedIDs) {
  return tabs.filter((tab) => tab.id && /^https?:/i.test(tab.url || ''))
    .filter((tab) => filter === 'all' || (filter === 'attached') === attachedIDs.includes(tab.id));
}

function preferredTabID(selected, eligible, currentWindowTabID, attachedIDs) {
  return Number(selected) || eligible.find((tab) => tab.id === currentWindowTabID)?.id
    || eligible.find((tab) => tab.active)?.id || attachedIDs[0] || 0;
}

function tabDisplayState(attached, liveState) {
  if (!attached) return 'Available';
  if (liveState === 'working') return 'Working';
  if (liveState === 'rate_limited') return 'Cooling down';
  return 'Attached';
}

function sessionHealthAdvisory(live) {
  const health = live?.pageHealth || {};
  const turns = Math.max(Number(health.assistantTurns) || 0, Number(health.userTurns) || 0);
  if (health.discarded) {
    return { level: 'warning', text: 'The browser discarded this page. ContextBridge will not resend a job blindly. Show the tab once if a job is waiting, or detach it and attach a fresh chat.' };
  }
  if (['timeout', 'error'].includes(health.domStatus)) {
    return { level: 'warning', text: 'This page is not answering the bounded control scan. ContextBridge keeps the connection alive but will not send blindly. Show the tab once if a job waits; otherwise detach it.' };
  }
  if (health.domStatus === 'unavailable') {
    return { level: 'notice', text: 'The provider controls are not ready yet. A background page may still be loading or asleep; jobs wait instead of guessing.' };
  }
  if (Number(health.slowScans) >= 2) {
    return { level: 'notice', text: `This provider page needed about ${(Number(health.diagnosticMs) / 1000).toFixed(1)}s for repeated control scans. It can still work, but a fresh chat is safer for unrelated new work.` };
  }
  if (turns >= 120) {
    return { level: 'warning', text: `Long conversation: ${turns} provider turns were detected. ContextBridge reads only the newest matching answer and never hides old messages. For unrelated work, detach this page and attach a fresh chat.` };
  }
  if (turns >= 60) {
    return { level: 'notice', text: `Growing conversation: ${turns} provider turns were detected. Existing context remains available, but a fresh chat may respond faster for unrelated work.` };
  }
  return null;
}

function renderSessionAdvisory(id, live) {
  const element = $(id);
  const advisory = sessionHealthAdvisory(live);
  element.hidden = !advisory;
  element.textContent = advisory?.text || '';
  if (advisory) element.setAttribute('data-level', advisory.level);
  else element.removeAttribute('data-level');
}

async function setAttachedTabIDs(values) {
  const previous = new Set(attachedTabIDs);
  attachedTabIDs = [...new Set(values.map(Number).filter(Boolean))].slice(0, 16);
  const removed = [...previous].filter((id) => !attachedTabIDs.includes(id));
  const updates = { tabId: attachedTabIDs[0] || 0, tabIds: attachedTabIDs };
  if (removed.length) {
    const saved = await settings();
    for (const key of ['tabCapabilities', 'tabCapabilityScans', 'tabFailures', 'tabEditModes']) {
      const entries = { ...(saved[key] || {}) };
      for (const id of removed) delete entries[id];
      updates[key] = entries;
    }
  }
  await api.storage.local.set(updates);
  await api.runtime.sendMessage({ type: 'refresh-tabs' });
}

async function blockAutoAttach(tabIds, blocked) {
  const saved = await settings();
  const values = new Set((saved.autoAttachBlockedTabIds || []).map(Number).filter(Boolean));
  for (const id of tabIds) {
    if (blocked) values.add(id);
    else values.delete(id);
  }
  await api.storage.local.set({ autoAttachBlockedTabIds: [...values].slice(-100) });
}

function describeSelectedTab() {
  const id = primaryTabID();
  const attached = attachedTabIDs.includes(id);
  const live = liveTabState.get(id);
  $('toggle-selected-tab').disabled = !id || (!attached && attachedTabIDs.length >= 16);
  $('toggle-selected-tab').textContent = attached ? 'Detach selected tab' : 'Attach selected tab';
  $('detach-all').disabled = attachedTabIDs.length === 0;
  $('tab-state').textContent = !id ? 'Choose a tab to inspect or attach.'
    : attached && live?.lastFailure ? `Attached · last job: ${live.lastFailure.code} (${live.lastFailure.reason || 'other'}). Detach at any time.`
    : attached ? `Attached · ${tabDisplayState(true, live?.state)}${live?.currentModel ? ` · ${live.currentModel}` : ''}. Detach at any time.`
      : 'Not attached. Use Attach selected tab explicitly, including its existing conversation if present.';
  renderSessionAdvisory('tab-session-health', attached ? live : null);
}

async function loadProfiles(saved) {
  if (!saved.token) return;
  try {
    const response = await fetch(`${saved.bridgeUrl}/v1/browser/profiles`, {
      headers: { Authorization: `Bearer ${saved.token}` },
      cache: 'no-store'
    });
    if (!response.ok) return;
    const profiles = await response.json();
    $('profile').textContent = '';
    for (const [name, profile] of Object.entries(profiles)) {
      const option = document.createElement('option');
      option.value = name;
      option.textContent = profile.label || name;
      option.selected = name === saved.profile;
      $('profile').append(option);
    }
    if ($('profile').value !== saved.profile && $('profile').value) {
      await api.storage.local.set({ profile: $('profile').value });
    }
  } catch (_) {}
}

async function refreshState() {
  const saved = await settings();
  const tabId = primaryTabID() || saved.tabId;
  currentTab = null;
  try {
    const tab = tabId ? await api.tabs.get(tabId) : null;
    if (tab?.url && /^https?:/i.test(tab.url)) {
      currentTab = { id: tab.id, title: tab.title || safeHost(tab.url), origin: new URL(tab.url).origin };
    }
  } catch (_) {}
  currentProfile = currentTab
    ? saved.taughtProfiles[currentTab.origin] || globalThis.ContextBridgeProfiles?.forURL(currentTab.origin) || null
    : null;
  try {
    const runtimeState = await api.runtime.sendMessage({ type: 'status' });
    if (runtimeState?.profile && runtimeState?.tab?.id === currentTab?.id) currentProfile = runtimeState.profile;
  } catch (_) {}
  renderProfile(currentProfile);
  renderRunning(saved.running);
  updateActions(currentProfile, saved.useVisualProfile);
  describeSelectedTab();
  $('connect-hint').textContent = saved.running
    ? `${attachedTabIDs.length} tab${attachedTabIDs.length === 1 ? '' : 's'} attached. Open Manage other tabs to change the pool.`
    : attachedTabIDs.length
      ? `${attachedTabIDs.length} tab${attachedTabIDs.length === 1 ? '' : 's'} ready. Connect starts them without a separate test.`
      : 'Connect will ask to attach this AI page. No separate test is required.';
  const heartbeatFresh = saved.relayConnected === true && Date.now() - Number(saved.lastHeartbeatAt || 0) < 60000;
  if (connectPending) showConnectionProgress(saved.connectionProgress);
  else if (saved.connectionError) setStatus('error', saved.connectionError);
  else if (saved.running && !heartbeatFresh) setStatus('connecting', 'Checking the local service connection…');
  else if (saved.running && saved.lastError) setStatus('error', saved.lastError);
  else if (saved.running) setStatus('live', `Connected · ${attachedTabIDs.length} tab${attachedTabIDs.length === 1 ? '' : 's'} attached`);
  else setStatus('idle', 'Not connected');
}

function showConnectionProgress(progress) {
  if (!connectPending || !progress) return;
  const done = Math.max(0, Number(progress.done) || 0);
  const total = Math.max(1, Number(progress.total) || 1);
  const eta = Number(progress.etaSeconds) > 0 ? ` · about ${Math.ceil(Number(progress.etaSeconds))}s left` : '';
  $('pair').textContent = `Connecting ${done}/${total}…`;
  setStatus('connecting', `${progress.phase || 'Connecting'} · ${done}/${total}${eta}`);
}

function renderProfile(profile, valid = null, copy = '') {
  $('profile-state').classList.toggle('ready', Boolean(profile) && valid !== false);
  $('profile-state').classList.toggle('invalid', valid === false);
  $('profile-title').textContent = profile ? profile.label : 'No visual profile yet';
  $('profile-copy').textContent = copy || (profile
    ? (profile.source === 'builtin'
      ? `Detected automatically for ${safeHost(profile.origin)}. You can customize it if the page changes.`
      : `Saved locally for ${safeHost(profile.origin)}.`)
    : 'Choose the prompt, send, response, and optional image controls.');
}

function renderRunning(running) {
  $('pair').hidden = running && !connectPending;
  $('stop').hidden = !running || connectPending;
  $('teach').disabled = running;
  $('scan-capabilities').disabled = !primaryTabID();
  if (!connectPending) $('pair').textContent = attachedTabIDs.length ? 'Connect attached tabs' : 'Connect this AI page';
}

function updateActions(profile, visualMode) {
  const hasProfile = Boolean(profile);
  $('verify').disabled = !hasProfile;
  $('remove-profile').disabled = !hasProfile || profile.source === 'builtin';
  $('export-profile').disabled = !hasProfile;
  // Keep Connect clickable so it can attach the current recognized page and
  // surface actionable setup errors instead of appearing permanently disabled.
  $('pair').disabled = connectPending;
  $('scan-capabilities').disabled = !primaryTabID();
}

function toggleProfileMode() {
  $('yaml-profile-row').querySelector('select').disabled = false;
}

function setStatus(state, text) {
  $('connection').dataset.state = state;
  $('status').textContent = text;
}

async function hasTabsPermission() {
  try { return await api.permissions.contains({ permissions: ['tabs'] }); } catch (_) { return false; }
}

function safeHost(url) {
  try { return new URL(url).host; } catch (_) { return String(url || ''); }
}

function permissionPattern(value) {
  const url = new URL(value);
  if (!['http:', 'https:'].includes(url.protocol)) throw new Error('Only HTTP(S) pages can be connected');
  // Browser host permissions never include a port; this pattern also covers
  // the loopback service on its configured port.
  return `${url.protocol}//${url.hostname}/*`;
}

function mediaOriginsFor(tabs) {
  return tabs.some((tab) => {
    try { return new URL(tab.url).hostname === 'gemini.google.com'; } catch (_) { return false; }
  }) ? ['https://contribution.usercontent.google.com/*'] : [];
}

function profileYAML(profile) {
  const name = profile.name || `visual-${safeHost(profile.origin).replace(/[^a-z0-9]+/gi, '-').toLowerCase()}`;
  const quote = (value) => JSON.stringify(String(value));
  const lines = [
    'browser_profiles:',
    `  ${name}:`,
    `    label: ${quote(profile.label || name)}`,
    `    match_url: ${quote(profile.match_url || `${profile.origin}/*`)}`,
    '    selectors:'
  ];
  for (const key of ['input', 'file_input', 'submit', 'response']) {
    const values = profile.selectors?.[key] || [];
    lines.push(`      ${key}: [${values.map(quote).join(', ')}]`);
  }
  return `${lines.join('\n')}\n`;
}

function withPopupDeadline(promise, milliseconds) {
  let timeout;
  return Promise.race([
    promise,
    new Promise((_, reject) => { timeout = setTimeout(() => reject(new Error('A selected browser tab did not respond; try Connect again')), milliseconds); })
  ]).finally(() => clearTimeout(timeout));
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
    sessionMode: 'manual',
    connectionProgress: null,
    tabEditModes: {},
    autoAttachBlockedTabIds: [],
    useVisualProfile: true,
    taughtProfiles: {},
    sessionBindings: {},
    tabCapabilities: {},
    tabCapabilityScans: {},
    tabFailures: {},
    connectionError: '',
    lastError: ''
  });
}

function selectedTabIDs() {
  return [...attachedTabIDs];
}

function primaryTabID() {
  return Number($('tab').value) || 0;
}
