const $ = (id) => document.getElementById(id);
let token = '';
let timer = 0;

initialize();

async function initialize() {
  const hash = new URLSearchParams(location.hash.slice(1));
  token = hash.get('token') || sessionStorage.getItem('contextbridge-token') || '';
  if (hash.has('token')) history.replaceState(null, '', location.pathname);
  await checkHealth();
  if (token) {
    sessionStorage.setItem('contextbridge-token', token);
    await refresh();
  }
}

$('auth-form').addEventListener('submit', async (event) => {
  event.preventDefault();
  token = $('token').value.trim();
  if (!token) return;
  sessionStorage.setItem('contextbridge-token', token);
  await refresh();
});
$('refresh').addEventListener('click', refresh);
$('update-toggle').addEventListener('change', updateAutomaticUpdates);
$('forget-token').addEventListener('click', () => {
  sessionStorage.removeItem('contextbridge-token');
  token = '';
  clearInterval(timer);
  $('workspace').hidden = true;
  $('auth-band').hidden = false;
  $('token').value = '';
  $('token').focus();
});

async function checkHealth() {
  try {
    const response = await fetch('/health', { cache: 'no-store' });
    const health = await response.json();
    $('service-state').dataset.state = response.ok ? 'online' : 'error';
    $('service-state').querySelector('span').textContent = response.ok ? 'Local service online' : 'Service error';
    $('version').textContent = `${versionLabel(health.version)}  local control center`;
  } catch (_) {
    $('service-state').dataset.state = 'error';
    $('service-state').querySelector('span').textContent = 'Service unavailable';
  }
}

async function refresh() {
  clearInterval(timer);
  try {
    const response = await fetch('/v1/status', {
      headers: { Authorization: `Bearer ${token}` },
      cache: 'no-store'
    });
    if (!response.ok) throw new Error(response.status === 401 ? 'The pairing token is not valid.' : `Status request failed: ${response.status}`);
    const data = await response.json();
    $('auth-error').textContent = '';
    $('auth-band').hidden = true;
    $('workspace').hidden = false;
    render(data);
    timer = setInterval(refresh, 3000);
  } catch (error) {
    $('workspace').hidden = true;
    $('auth-band').hidden = false;
    $('auth-error').textContent = error.message || String(error);
  }
}

function render(data) {
  $('service-metric').textContent = versionLabel(data.version);
  $('listen').textContent = data.listen || '';
  $('queue-metric').textContent = String(data.queued || 0);
  $('completed-metric').textContent = `${data.completed || 0} this session, ${data.metrics?.jobs_total || 0} total, ${data.metrics?.jobs_failed || 0} failed`;
  $('refresh-time').textContent = `Updated ${new Date().toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' })}`;
  $('inbox-path').textContent = data.storage?.inbox || '';
  $('data-path').textContent = data.storage?.directory || '';
  $('models-path').textContent = data.storage?.models || '';
  renderUpdates(data.updates || null);
  renderTunnel(data.tunnel || {});
  renderRuntime(data.runtime || {});
  renderModels(data.runtime?.models || [], data.rag || {});
  renderMetrics(data.metrics || {});
  renderBrowser(data.browser || {});
  renderRoutes(data.routes || {});
  renderActivity(data.activity || []);
}

function renderUpdates(updates) {
  const toggle = $('update-toggle');
  if (!updates) {
    toggle.checked = false;
    toggle.disabled = true;
    $('update-state').textContent = 'Update manager unavailable';
    return;
  }
  toggle.disabled = false;
  toggle.checked = Boolean(updates.enabled);
  $('update-state').textContent = updates.enabled ? `Enabled, ${updates.channel || 'stable'} channel` : 'Disabled on this device';
  const checked = meaningfulTimestamp(updates.last_checked) ? ` Last checked ${relativeTime(updates.last_checked)}.` : '';
  const available = updates.update_available ? ` Version ${updates.available_version} is ready.` : '';
  $('update-detail').textContent = `Current ${versionLabel(updates.current_version)}.${available}${checked}`;
}

async function updateAutomaticUpdates() {
  const toggle = $('update-toggle');
  const enabled = toggle.checked;
  toggle.disabled = true;
  try {
    const response = await fetch('/v1/settings/updates', {
      method: 'PUT',
      headers: { Authorization: `Bearer ${token}`, 'Content-Type': 'application/json' },
      body: JSON.stringify({ enabled })
    });
    if (!response.ok) throw new Error(`Setting update failed: ${response.status}`);
    renderUpdates(await response.json());
  } catch (error) {
    toggle.checked = !enabled;
    $('update-state').textContent = error.message || String(error);
  } finally {
    toggle.disabled = false;
  }
}

function renderTunnel(tunnel) {
  const connected = Boolean(tunnel.connected);
  $('tunnel-metric').textContent = connected ? 'Connected' : (tunnel.state || 'Not connected');
  $('tunnel-detail').textContent = connected
    ? `${tunnel.target || 'server'} via ${tunnel.transport || 'secure transport'}`
    : (meaningfulTimestamp(tunnel.last_seen) ? `Last signal ${relativeTime(tunnel.last_seen)}` : 'No heartbeat received');
}

function renderRuntime(runtime) {
  const hardware = runtime.hardware || {};
  const gpus = hardware.gpus || [];
  const memoryAvailable = formatBytes(hardware.memory_available_bytes);
  const memoryTotal = formatBytes(hardware.memory_total_bytes);
  $('cpu-name').textContent = hardware.cpu || `${hardware.os || ''} ${hardware.architecture || ''}`.trim() || 'Unknown CPU';
  $('memory-detail').textContent = `${memoryAvailable} available of ${memoryTotal}${hardware.memory_type ? ` · ${hardware.memory_type}` : ''}`;
  $('gpu-detail').textContent = gpus.length
    ? gpus.map((gpu) => `${gpu.name} (${gpu.backend}, ${formatBytes(gpu.memory_free_bytes)} free, ${gpu.utilization_percent || 0}%, ${gpu.temperature_c || 0}°C)`).join(', ')
    : 'No supported GPU telemetry detected';
  const backends = hardware.backends || [];
  $('backend-detail').textContent = backends.filter((item) => item.available).map((item) => item.name).join(', ') || 'CPU only';
  $('hardware-metric').textContent = gpus.length ? gpus[0].backend : 'CPU';
  $('hardware-detail').textContent = gpus.length ? `${formatBytes(gpus[0].memory_free_bytes)} VRAM free` : `${memoryAvailable} RAM free`;

  const list = $('engine-list');
  list.textContent = '';
  for (const [name, engine] of Object.entries(runtime.engines || {}).sort(([a], [b]) => a.localeCompare(b))) {
    const row = document.createElement('div');
    row.className = 'engine';
    row.dataset.state = engine.state || 'unknown';
    const identity = document.createElement('div');
    const title = document.createElement('strong');
    title.textContent = name;
    const type = document.createElement('span');
    type.textContent = engine.type || 'engine';
    identity.append(title, type);
    const state = document.createElement('span');
    state.className = 'state-tag';
    state.textContent = engine.state || 'unknown';
    const detail = document.createElement('div');
    detail.className = 'engine-detail';
    const model = document.createElement('span');
    model.textContent = engine.model || engine.version || 'No default model';
    const affinity = document.createElement('span');
    affinity.textContent = engine.affinity || 'Idle';
    detail.append(model, affinity);
    row.append(identity, state, detail);
    if (engine.warning) {
      const warning = document.createElement('p');
      warning.className = 'runtime-warning';
      warning.textContent = engine.warning;
      row.append(warning);
    }
    for (const loaded of engine.models || []) {
      const loadedModel = document.createElement('p');
      loadedModel.className = 'loaded-model';
      const allocation = loaded.vram_bytes ? `${formatBytes(loaded.vram_bytes)} VRAM` : `${formatBytes(loaded.size_bytes)} model`;
      loadedModel.textContent = loaded.loaded
        ? `${loaded.name}  loaded on ${loaded.affinity || 'runtime'}  ${allocation}`
        : `${loaded.name}  cached  ${formatBytes(loaded.size_bytes)}`;
      row.append(loadedModel);
    }
    list.append(row);
  }
}

function renderModels(models, rag) {
  const list = $('model-list');
  list.textContent = '';
  const installed = models.filter((model) => model.installed).length;
  $('model-summary').textContent = `${installed} of ${models.length} installed`;
  if (!models.length) {
    const empty = document.createElement('p');
    empty.className = 'empty';
    empty.textContent = 'No model manifests are configured.';
    list.append(empty);
  }
  for (const model of models) {
    const row = document.createElement('div');
    row.className = 'model-row';
    const name = document.createElement('strong');
    name.textContent = model.name;
    const kind = document.createElement('span');
    kind.textContent = model.kind || 'model';
    const state = document.createElement('span');
    state.className = model.installed ? 'installed' : 'missing';
    state.textContent = model.installed ? formatBytes(model.size_bytes) : 'Run contextbridge pull';
    const source = document.createElement('code');
    source.textContent = `${model.repository}/${model.file}`;
    row.append(name, kind, state, source);
    list.append(row);
  }
  $('rag-state').textContent = rag.enabled ? 'RAG is configured' : 'RAG is disabled';
  $('rag-backend').textContent = rag.backend || 'None';
  $('rag-documents').textContent = String(rag.documents || 0);
  $('rag-route').textContent = rag.embedding_route || 'None';
}

function renderMetrics(metrics) {
  const jobs = Number(metrics.jobs_total || 0);
  const failed = Number(metrics.jobs_failed || 0);
  const average = jobs ? Math.round(Number(metrics.latency_total_ms || 0) / jobs) : 0;
  $('metrics-jobs').textContent = String(jobs);
  $('metrics-latency').textContent = average ? `${average} ms` : 'No samples';
  $('metrics-failure').textContent = jobs ? `${((failed / jobs) * 100).toFixed(1)}%` : '0%';
  $('metrics-vectors').textContent = String(metrics.embedding_vectors || 0);
  $('metrics-updated').textContent = meaningfulTimestamp(metrics.updated_at) ? `Updated ${relativeTime(metrics.updated_at)}` : 'Waiting for the first job';
  renderBreakdown('metrics-routes', metrics.by_route || {});
  renderBreakdown('metrics-tasks', metrics.by_task || {});
  renderProviderBreakdown(metrics);
  renderBreakdown('metrics-models', metrics.by_model || {});
  renderBreakdown('metrics-flags', metrics.by_flag || {});
}

function renderProviderBreakdown(metrics) {
  const list = $('metrics-providers');
  list.textContent = '';
  const entries = Object.entries(metrics.by_provider || {}).sort((a, b) => Number(b[1]) - Number(a[1]) || a[0].localeCompare(b[0]));
  if (!entries.length) {
    const row = document.createElement('li');
    row.className = 'empty';
    row.textContent = 'No samples yet';
    list.append(row);
    return;
  }
  for (const [name, countValue] of entries.slice(0, 8)) {
    const count = Number(countValue || 0);
    const latency = Number(metrics.provider_latency_ms?.[name] || 0);
    const latencySamples = Number(metrics.provider_latency_samples?.[name] || 0);
    const failures = Number(metrics.provider_failures?.[name] || 0);
    const row = document.createElement('li');
    const label = document.createElement('span');
    label.textContent = name || 'unknown';
    const value = document.createElement('strong');
    value.textContent = `${count}  ${latencySamples && latency ? Math.round(latency / latencySamples) + ' ms avg' : ''}${failures ? '  ' + failures + ' failed' : ''}`.trim();
    row.append(label, value);
    list.append(row);
  }
}

function renderBreakdown(id, values) {
  const list = $(id);
  list.textContent = '';
  const entries = Object.entries(values).sort((a, b) => Number(b[1]) - Number(a[1]) || a[0].localeCompare(b[0]));
  if (!entries.length) {
    const row = document.createElement('li');
    row.className = 'empty';
    row.textContent = 'No samples yet';
    list.append(row);
    return;
  }
  for (const [name, count] of entries.slice(0, 8)) {
    const row = document.createElement('li');
    const label = document.createElement('span');
    label.textContent = name || 'unknown';
    const value = document.createElement('strong');
    value.textContent = String(count);
    row.append(label, value);
    list.append(row);
  }
}

function renderBrowser(browser) {
  const connected = Boolean(browser.connected);
  $('browser-metric').textContent = connected ? `${browser.active_tabs || 1} tab${(browser.active_tabs || 1) === 1 ? '' : 's'} · ${browser.busy_tabs || 0} busy` : 'Not connected';
  $('browser-detail').textContent = connected
    ? `${browser.browser || 'browser'} ${browser.extension_version || ''}`.trim()
    : 'Open the extension to pair a tab';
  $('tab-title').textContent = (browser.tabs || []).map((tab) => tab.title).filter(Boolean).join(' · ') || browser.tab_title || 'No tab connected';
  $('tab-origin').textContent = browser.origin || 'None';
  $('tab-profile').textContent = (browser.tabs || []).map((tab) => `${tab.profile || 'browser'}${tab.current_model ? `: ${tab.current_model}` : ''}`).join(' · ') || browser.profile_label || 'None';
  $('tab-state').textContent = connected ? (browser.state || 'waiting') : 'paused';
  $('tab-seen').textContent = meaningfulTimestamp(browser.last_seen) ? relativeTime(browser.last_seen) : 'Never';
}

function renderRoutes(routes) {
  const list = $('route-list');
  list.textContent = '';
  for (const [name, route] of Object.entries(routes).sort(([a], [b]) => a.localeCompare(b))) {
    const row = document.createElement('div');
    row.className = 'route';
    const routeName = document.createElement('strong');
    routeName.textContent = name;
    const provider = document.createElement('span');
    provider.className = 'provider-tag';
    provider.textContent = route.provider || 'none';
    const fallback = document.createElement('span');
    const chain = [route.provider, ...(route.fallback || [])].filter(Boolean);
    fallback.textContent = `${chain.join('  >  ')}${route.browser_profile ? `  |  ${route.browser_profile}` : ''}`;
    row.append(routeName, provider, fallback);
    list.append(row);
  }
}

function renderActivity(items) {
  const list = $('activity');
  list.textContent = '';
  if (!items.length) {
    const empty = document.createElement('li');
    empty.className = 'empty';
    empty.textContent = 'No jobs have passed through this service yet.';
    list.append(empty);
    return;
  }
  for (const item of items.slice(0, 30)) {
    const row = document.createElement('li');
    const time = document.createElement('time');
    time.dateTime = item.time || '';
    time.textContent = item.time ? new Date(item.time).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' }) : '';
    const kind = document.createElement('span');
    kind.className = 'kind';
    kind.textContent = item.kind || 'event';
    const message = document.createElement('span');
    message.textContent = item.message || '';
    const code = document.createElement('code');
    code.textContent = item.job_id || '';
    row.append(time, kind, message, code);
    list.append(row);
  }
}

function relativeTime(value) {
  const seconds = Math.max(0, Math.round((Date.now() - new Date(value).getTime()) / 1000));
  if (seconds < 5) return 'Just now';
  if (seconds < 60) return `${seconds}s ago`;
  return `${Math.floor(seconds / 60)}m ago`;
}

function meaningfulTimestamp(value) {
  if (!value) return false;
  const date = new Date(value);
  return Number.isFinite(date.getTime()) && date.getUTCFullYear() >= 2000;
}

function versionLabel(value) {
  const version = String(value || 'dev');
  return version === 'dev' || version.startsWith('v') ? version : `v${version}`;
}

function formatBytes(value) {
  const size = Number(value || 0);
  if (!size) return 'unknown';
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  let current = size;
  let unit = 0;
  while (current >= 1024 && unit < units.length - 1) { current /= 1024; unit += 1; }
  return `${current.toFixed(1)} ${units[unit]}`;
}
