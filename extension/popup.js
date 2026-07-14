const $ = (id) => document.getElementById(id);

document.addEventListener('DOMContentLoaded', async () => {
  const saved = await chrome.storage.local.get({
    bridgeUrl: 'http://127.0.0.1:32145', token: '', profile: 'chatgpt', tabId: 0, running: false
  });
  $('url').value = saved.bridgeUrl;
  $('token').value = saved.token;
  await loadTabs(saved.tabId);
  await loadProfiles(saved);
  renderStatus(saved.running, saved.running ? 'Paired and waiting' : 'Not paired');
});

$('test').addEventListener('click', async () => {
  const bridgeOrigin = new URL($('url').value).origin + '/*';
  const granted = await chrome.permissions.request({ origins: [bridgeOrigin] });
  if (!granted) {
    renderStatus(false, 'Local bridge permission was not granted');
    return;
  }
  await saveInputs();
  const result = await chrome.runtime.sendMessage({ type: 'test' });
  renderStatus(Boolean(result?.ok), result?.ok ? `Bridge ${result.version || ''} is ready` : (result?.error || 'Bridge unavailable'));
});

$('pair').addEventListener('click', async () => {
  const tabId = Number($('tab').value);
  const tab = await chrome.tabs.get(tabId);
  const origin = new URL(tab.url).origin + '/*';
  const bridgeOrigin = new URL($('url').value).origin + '/*';
  const granted = await chrome.permissions.request({ origins: [origin, bridgeOrigin] });
  if (!granted) {
    renderStatus(false, 'Permission was not granted');
    return;
  }
  await saveInputs();
  const result = await chrome.runtime.sendMessage({ type: 'start' });
  renderStatus(Boolean(result?.ok), result?.ok ? 'Paired and waiting' : (result?.error || 'Could not pair'));
});

$('stop').addEventListener('click', async () => {
  await chrome.runtime.sendMessage({ type: 'stop' });
  renderStatus(false, 'Pairing stopped');
});

async function saveInputs() {
  await chrome.storage.local.set({
    bridgeUrl: $('url').value.replace(/\/$/, ''),
    token: $('token').value.trim(),
    profile: $('profile').value,
    tabId: Number($('tab').value)
  });
}

async function loadTabs(selected) {
  const tabs = await chrome.tabs.query({});
  $('tab').textContent = '';
  for (const tab of tabs.filter((item) => /^https?:/.test(item.url || ''))) {
    const option = document.createElement('option');
    option.value = tab.id;
    option.textContent = `${tab.title || 'Untitled'}  |  ${new URL(tab.url).host}`;
    option.selected = tab.id === selected;
    $('tab').append(option);
  }
}

async function loadProfiles(saved) {
  if (!saved.token) return;
  try {
    const response = await fetch(`${saved.bridgeUrl}/v1/browser/profiles`, {
      headers: { Authorization: `Bearer ${saved.token}` }
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
  } catch (_) {}
}

function renderStatus(live, text) {
  $('status').textContent = text;
  document.querySelector('.signal').classList.toggle('live', live);
  $('stop').classList.toggle('visible', live);
}
