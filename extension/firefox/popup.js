const api = globalThis.browser || globalThis.chrome;
const $ = (id) => document.getElementById(id);
let currentTab = null;
let currentProfile = null;
let attachedTabIDs = [];
let liveTabState = new Map();

document.addEventListener('DOMContentLoaded', initialize);
api.storage.onChanged?.addListener((changes, area) => {
  if (area !== 'local') return;
  if (changes.tabIds) attachedTabIDs = [...new Set((changes.tabIds.newValue || []).map(Number).filter(Boolean))];
  if (changes.tabIds || changes.tabCapabilities || changes.tabCooldowns || changes.tabFailures || changes.lastError || changes.connectionError || changes.running) void refreshLiveTabs(Boolean(changes.tabIds || changes.running || changes.connectionError));
});

async function initialize() {
  $('version').textContent = `v${api.runtime.getManifest().version}`;
  const saved = await settings();
  $('url').value = saved.bridgeUrl;
  $('token').value = saved.token;
  $('visual-mode').checked = saved.useVisualProfile;
  $('auto-attach-fresh').checked = saved.autoAttachFreshTabs;
  attachedTabIDs = [...new Set((saved.tabIds?.length ? saved.tabIds : [saved.tabId]).map(Number).filter(Boolean))];
  toggleProfileMode();
  await loadTabs(0, false);
  await loadProfiles(saved);
  await refreshState();
  setInterval(() => { if (!document.hidden) void refreshLiveTabs(); }, 2500);
}

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
  const tab = await api.tabs.get(tabId);
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
  const tabs = await api.tabs.query({ active: true, currentWindow: true });
  return tabs.find((tab) => tab.id) || null;
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
    const result = await api.runtime.sendMessage({ type: 'scan-capabilities', tabId: primaryTabID() });
    if (!result?.ok) throw new Error(result?.error || 'Could not inspect model choices');
    const models = result.capabilities?.models || [];
    const levels = result.capabilities?.reasoningLevels || [];
    setStatus('live', `${models.length} model choice${models.length === 1 ? '' : 's'} · ${levels.length} reasoning level${levels.length === 1 ? '' : 's'}`);
  } catch (error) {
    setStatus('error', error.message || String(error));
  }
});

$('pair').addEventListener('click', async () => {
  try {
    await api.storage.local.set({ connectionError: '', lastError: '' });
    let tabs = await Promise.all(selectedTabIDs().map((tabId) => api.tabs.get(tabId)));
    let attachCurrentID = 0;
    if (!selectedTabIDs().length) {
      const current = await currentPageTab();
      if (!current?.id) throw new Error('Open a ChatGPT or Gemini page first');
      const tab = await checkedAttachTab(current.id);
      tabs = [tab];
      attachCurrentID = tab.id;
    }
    const origins = [...new Set([...tabs.map((tab) => permissionPattern(tab.url)), permissionPattern($('url').value)])];
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
    setStatus('live', `Connected · ${tabs.length} tab${tabs.length === 1 ? '' : 's'} ready`);
  } catch (error) {
    const message = error.message || String(error);
    await api.storage.local.set({ connectionError: message });
    setStatus('error', message);
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
  let runtimeTabs = new Map();
  try {
    const status = await api.runtime.sendMessage({ type: 'status' });
    runtimeTabs = new Map((status?.tabs || []).map((tab) => [tab.id, tab]));
  } catch (_) {}
  liveTabState = runtimeTabs;
  const focusedID = Number(selected) || eligible.find((tab) => tab.active)?.id || attachedTabIDs[0] || 0;
  const desired = eligible.map((tab) => {
    const host = safeHost(tab.url);
    const windowLabel = allWindows ? `W${tab.windowId}  ` : '';
    const attached = attachedTabIDs.includes(tab.id);
    const live = runtimeTabs.get(tab.id);
    const state = tabDisplayState(attached, live?.state);
    const model = attached && live?.currentModel ? ` · ${live.currentModel}` : '';
    return { id: tab.id, label: `${attached ? '●' : '○'} ${state}${model} · ${windowLabel}${tab.title || 'Untitled'}  |  ${host}` };
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
  $('current-page').classList.toggle('ready', Boolean(profile));
  $('current-provider').textContent = profile
    ? `${String(profile.label || profile.name || 'AI page').replace(/ \(auto-detected\)$/, '')} detected`
    : 'No supported AI page detected';
  $('toggle-current-tab').disabled = !available || (!attached && ((!profile && saved.useVisualProfile) || attachedTabIDs.length >= 16));
  $('toggle-current-tab').textContent = attached ? 'Detach this page' : 'Attach this page';
  $('current-tab-state').textContent = !available
    ? 'Open ChatGPT or Gemini to connect it.'
    : `${tab.title || safeHost(tab.url)} · ${attached ? (saved.running ? 'connected' : 'attached, connection stopped') : (profile ? 'ready' : 'teach this page in Advanced setup')}`;
}

function filterTabList(tabs, filter, attachedIDs) {
  return tabs.filter((tab) => tab.id && /^https?:/i.test(tab.url || ''))
    .filter((tab) => filter === 'all' || (filter === 'attached') === attachedIDs.includes(tab.id));
}

function tabDisplayState(attached, liveState) {
  if (!attached) return 'Available';
  if (liveState === 'working') return 'Working';
  if (liveState === 'rate_limited') return 'Cooling down';
  return 'Attached';
}

async function setAttachedTabIDs(values) {
  const previous = new Set(attachedTabIDs);
  attachedTabIDs = [...new Set(values.map(Number).filter(Boolean))].slice(0, 16);
  const removed = [...previous].filter((id) => !attachedTabIDs.includes(id));
  const updates = { tabId: attachedTabIDs[0] || 0, tabIds: attachedTabIDs };
  if (removed.length) {
    const saved = await settings();
    for (const key of ['tabCapabilities', 'tabCapabilityScans', 'tabFailures']) {
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
    ? `${attachedTabIDs.length} tab${attachedTabIDs.length === 1 ? '' : 's'} connected. Open Manage other tabs to change the pool.`
    : attachedTabIDs.length
      ? `${attachedTabIDs.length} tab${attachedTabIDs.length === 1 ? '' : 's'} ready. Connect starts them without a separate test.`
      : 'Connect will ask to attach this AI page. No separate test is required.';
  if (saved.connectionError) setStatus('error', saved.connectionError);
  else if (saved.running && saved.lastError) setStatus('error', saved.lastError);
  else if (saved.running) setStatus('live', `Connected · ${attachedTabIDs.length} tab${attachedTabIDs.length === 1 ? '' : 's'} attached`);
  else setStatus('idle', 'Not connected');
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
  $('pair').hidden = running;
  $('stop').hidden = !running;
  $('teach').disabled = running;
  $('scan-capabilities').disabled = !primaryTabID();
  $('pair').textContent = attachedTabIDs.length ? 'Connect attached tabs' : 'Connect this AI page';
}

function updateActions(profile, visualMode) {
  const hasProfile = Boolean(profile);
  $('verify').disabled = !hasProfile;
  $('remove-profile').disabled = !hasProfile || profile.source === 'builtin';
  $('export-profile').disabled = !hasProfile;
  // Keep Connect clickable so it can attach the current recognized page and
  // surface actionable setup errors instead of appearing permanently disabled.
  $('pair').disabled = false;
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

async function settings() {
  return api.storage.local.get({
    bridgeUrl: 'http://127.0.0.1:32145',
    token: '',
    profile: 'chatgpt',
    tabId: 0,
    tabIds: [],
    running: false,
    autoAttachFreshTabs: false,
    autoAttachBlockedTabIds: [],
    useVisualProfile: true,
    taughtProfiles: {},
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
