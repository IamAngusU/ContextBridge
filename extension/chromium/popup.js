const api = globalThis.browser || globalThis.chrome;
const $ = (id) => document.getElementById(id);
let currentTab = null;
let currentProfile = null;

document.addEventListener('DOMContentLoaded', initialize);

async function initialize() {
  $('version').textContent = `v${api.runtime.getManifest().version}`;
  const saved = await settings();
  $('url').value = saved.bridgeUrl;
  $('token').value = saved.token;
  $('visual-mode').checked = saved.useVisualProfile;
  toggleProfileMode();
  await loadTabs(saved.tabId, false);
  await loadProfiles(saved);
  await refreshState();
}

$('refresh-tabs').addEventListener('click', () => loadTabs(Number($('tab').value), false));
$('all-tabs').addEventListener('click', async () => {
  const granted = await api.permissions.request({ permissions: ['tabs'] });
  if (!granted) return setStatus('error', 'Tab access was not granted');
  await loadTabs(Number($('tab').value), true);
  $('all-tabs').hidden = true;
});
$('tab').addEventListener('change', async () => {
  await api.storage.local.set({ tabId: Number($('tab').value) });
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
    const tabId = Number($('tab').value);
    const tab = await api.tabs.get(tabId);
    const origins = [new URL(tab.url).origin + '/*', new URL($('url').value).origin + '/*'];
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
    const result = await api.runtime.sendMessage({ type: 'verify-profile', tabId: Number($('tab').value) });
    if (!result?.ok) throw new Error(result?.error || 'One or more targets are missing');
    const image = result.report.file_input ? 'image ready' : 'text only';
    renderProfile(currentProfile, true, `Prompt, send, and response found. ${image}.`);
    setStatus('live', 'Profile is ready');
  } catch (error) {
    renderProfile(currentProfile, false, error.message || String(error));
    setStatus('error', 'Profile needs attention');
  }
});

$('pair').addEventListener('click', async () => {
  try {
    const tab = await api.tabs.get(Number($('tab').value));
    const origins = [new URL(tab.url).origin + '/*', new URL($('url').value).origin + '/*'];
    const granted = await api.permissions.request({ origins });
    if (!granted) return setStatus('error', 'Connection access was not granted');
    await saveInputs();
    const test = await api.runtime.sendMessage({ type: 'test' });
    if (!test?.ok) throw new Error(test?.error || 'Local service is unavailable');
    const result = await api.runtime.sendMessage({ type: 'start' });
    if (!result?.ok) throw new Error(result?.error || 'Connection could not start');
    renderRunning(true);
    setStatus('live', 'Connected and waiting');
  } catch (error) {
    setStatus('error', error.message || String(error));
  }
});

$('stop').addEventListener('click', async () => {
  await api.runtime.sendMessage({ type: 'stop' });
  renderRunning(false);
  setStatus('idle', 'Connection stopped');
});

$('test').addEventListener('click', async () => {
  try {
    const bridgeOrigin = new URL($('url').value).origin + '/*';
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
  await api.storage.local.set({
    bridgeUrl: $('url').value.trim().replace(/\/$/, ''),
    token: $('token').value.trim(),
    profile: $('profile').value,
    tabId: Number($('tab').value),
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
  const eligible = tabs.filter((tab) => tab.id && /^https?:/i.test(tab.url || ''));
  $('tab').textContent = '';
  for (const tab of eligible) {
    const option = document.createElement('option');
    option.value = String(tab.id);
    const host = safeHost(tab.url);
    const windowLabel = allWindows ? `W${tab.windowId}  ` : '';
    option.textContent = `${windowLabel}${tab.title || 'Untitled'}  |  ${host}`;
    option.selected = tab.id === selected;
    $('tab').append(option);
  }
  if (!$('tab').value && eligible[0]) $('tab').value = String(eligible[0].id);
  $('all-tabs').hidden = await hasTabsPermission();
  await api.storage.local.set({ tabId: Number($('tab').value || 0) });
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
  const tabId = Number($('tab').value || saved.tabId);
  currentTab = null;
  try {
    const tab = tabId ? await api.tabs.get(tabId) : null;
    if (tab?.url && /^https?:/i.test(tab.url)) {
      currentTab = { id: tab.id, title: tab.title || safeHost(tab.url), origin: new URL(tab.url).origin };
    }
  } catch (_) {}
  currentProfile = currentTab ? saved.taughtProfiles[currentTab.origin] || null : null;
  renderProfile(currentProfile);
  renderRunning(saved.running);
  updateActions(Boolean(currentProfile), saved.useVisualProfile);
  if (saved.lastError) setStatus('error', saved.lastError);
  else if (saved.running) setStatus('live', 'Connected and waiting');
  else setStatus('idle', 'Not connected');
}

function renderProfile(profile, valid = null, copy = '') {
  $('profile-state').classList.toggle('ready', Boolean(profile) && valid !== false);
  $('profile-state').classList.toggle('invalid', valid === false);
  $('profile-title').textContent = profile ? profile.label : 'No visual profile yet';
  $('profile-copy').textContent = copy || (profile
    ? `Saved locally for ${safeHost(profile.origin)}.`
    : 'Choose the prompt, send, response, and optional image controls.');
}

function renderRunning(running) {
  $('pair').hidden = running;
  $('stop').hidden = !running;
  $('teach').disabled = running;
  $('tab').disabled = running;
}

function updateActions(hasProfile, visualMode) {
  $('verify').disabled = !hasProfile;
  $('remove-profile').disabled = !hasProfile;
  $('export-profile').disabled = !hasProfile;
  $('pair').disabled = visualMode ? !hasProfile : false;
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
    running: false,
    useVisualProfile: true,
    taughtProfiles: {},
    lastError: ''
  });
}
