const $ = (id) => document.getElementById(id);
let token = '';
let timer = 0;
let serviceClock = null;
setInterval(() => { updateServiceClock(); updateScheduleCountdowns(); }, 1000);

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
$('schedule-form').addEventListener('submit', createSchedule);
$('schedule-form').elements.frequency.addEventListener('change', updateScheduleFields);
$('schedule-list').addEventListener('click', scheduleAction);
updateScheduleFields();
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
    const requestStarted = Date.now();
    const response = await fetch('/v1/status', {
      headers: { Authorization: `Bearer ${token}` },
      cache: 'no-store'
    });
    if (!response.ok) throw new Error(response.status === 401 ? 'The pairing token is not valid.' : `Status request failed: ${response.status}`);
    const data = await response.json();
    const requestEnded = Date.now();
    const serviceAt = Date.parse(data.server_time || '');
    if (Number.isFinite(serviceAt)) serviceClock = {at:serviceAt, received:performance.now(), offset:data.server_utc_offset_seconds || 0,
      delta:serviceAt-(requestStarted+requestEnded)/2, uncertainty:(requestEnded-requestStarted)/2};
    $('auth-error').textContent = '';
    $('auth-band').hidden = true;
    $('workspace').hidden = false;
    render(data);
    updateServiceClock();
    timer = setInterval(refresh, 3000);
  } catch (error) {
    $('workspace').hidden = true;
    $('auth-band').hidden = false;
    $('auth-error').textContent = error.message || String(error);
  }
}

function offsetLabel(seconds) {
  const amount=Math.abs(seconds), sign=seconds<0?'-':'+';
  return `UTC${sign}${String(Math.floor(amount/3600)).padStart(2,'0')}:${String(Math.floor(amount%3600/60)).padStart(2,'0')}`;
}
function signedDelay(ms) { return `${ms>=0?'+':'−'}${Math.abs(ms)<1000?Math.round(Math.abs(ms))+' ms':(Math.abs(ms)/1000).toFixed(1)+' s'}`; }
function updateServiceClock() {
  if (!serviceClock) return;
  const now=serviceClock.at+performance.now()-serviceClock.received;
  const local=new Date(now+serviceClock.offset*1000).toISOString().slice(11,19);
  $('server-clock').textContent=`${local} ${offsetLabel(serviceClock.offset)} · ${signedDelay(serviceClock.delta)} vs this browser (±${Math.round(serviceClock.uncertainty)} ms)`;
}
function remainingLabel(ms) {
  const seconds=Math.max(0,Math.ceil(ms/1000)), days=Math.floor(seconds/86400),hours=Math.floor(seconds%86400/3600),minutes=Math.floor(seconds%3600/60),remainder=seconds%60;
  return days?`${days}d ${hours}h ${minutes}m`:hours?`${hours}h ${minutes}m ${remainder}s`:`${minutes}m ${remainder}s`;
}
function updateScheduleCountdowns() {
  for (const element of document.querySelectorAll('[data-next-run]')) {
    const next=Date.parse(element.dataset.nextRun);
    element.textContent=Number.isFinite(next) ? (next<=Date.now()?'due · waiting for resources':`in ${remainingLabel(next-Date.now())}`) : '';
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
  renderSchedules(data.schedules || []);
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
  const pending = updates.pending_version ? ` Activation of ${versionLabel(updates.pending_version)} is pending.` : '';
  const failed = updates.last_error ? ` Last attempt failed: ${updates.last_error}` : '';
  $('update-detail').textContent = `Current ${versionLabel(updates.current_version)}.${pending}${available}${failed}${checked}`;
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
  const cpuName = hardware.cpu || `${hardware.os || ''} ${hardware.architecture || ''}`.trim() || 'Unknown CPU';
  const cpuSpeed = hardware.cpu_frequency_mhz ? ` · ${(hardware.cpu_frequency_mhz / 1000).toFixed(2)} GHz` : '';
  $('cpu-name').textContent = `${cpuName}${cpuSpeed} · ${hardware.cpu_utilization_percent || 0}% load`;
  $('system-detail').textContent = hardware.os_version || `${hardware.os || 'unknown'}/${hardware.architecture || 'unknown'}`;
  $('uptime-detail').textContent = formatUptime(hardware.uptime_seconds || 0);
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

  const packs = Array.isArray(runtime.resource_packs) ? runtime.resource_packs : [];
  $('resource-summary').textContent = packs.length ? `${packs.length} detected` : 'None present';
  const resourceList = $('resource-list');
  resourceList.textContent = '';
  if (!packs.length) {
    const empty = document.createElement('p');
    empty.className = 'empty';
    empty.textContent = 'Packs appear only while their marker is physically present.';
    resourceList.append(empty);
  }
  for (const pack of packs.slice().sort((a, b) => String(a.id || '').localeCompare(String(b.id || '')))) {
    const row = document.createElement('div');
    row.className = 'resource-row';
    const identity = document.createElement('div');
    const title = document.createElement('strong');
    title.textContent = pack.name || pack.id || 'Portable resource';
    const id = document.createElement('code');
    id.textContent = pack.id || 'unknown-id';
    identity.append(title, id);
    const kind = document.createElement('span');
    kind.className = 'resource-kind';
    kind.textContent = pack.kind || 'resource-pack';
    const detail = document.createElement('div');
    detail.className = 'resource-detail';
    const endpoints = Array.isArray(pack.endpoints) ? pack.endpoints : [];
    detail.textContent = endpoints.length
      ? endpoints.map((endpoint) => `${endpoint.id} · ${endpoint.type}${endpoint.capabilities?.length ? ` · ${endpoint.capabilities.join('+')}` : ''}`).join('  /  ')
      : 'Detected metadata only · no routable endpoint';
    row.append(identity, kind, detail);
    if (pack.warning) {
      const warning = document.createElement('p');
      warning.className = 'runtime-warning';
      warning.textContent = pack.warning;
      row.append(warning);
    }
    resourceList.append(row);
  }
}

function formatUptime(seconds) {
  seconds = Math.max(0, Number(seconds) || 0);
  const days = Math.floor(seconds / 86400);
  const hours = Math.floor((seconds % 86400) / 3600);
  const minutes = Math.floor((seconds % 3600) / 60);
  if (days) return `${days}d ${hours}h`;
  if (hours) return `${hours}h ${minutes}m`;
  return `${minutes}m`;
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
  renderRateBreakdown('metrics-attempted-providers',metrics.by_attempted_provider || {},metrics.attempted_provider_failures || {});
  renderRateBreakdown('metrics-attempted-models',metrics.by_attempted_model || {},metrics.model_failures || {});
  renderRateBreakdown('metrics-reasoning',metrics.by_reasoning || {},metrics.reasoning_failures || {});
  renderRateBreakdown('metrics-selections',metrics.by_selection || {},metrics.selection_failures || {});
}

function renderRateBreakdown(id, totals, failures) {
  const list=$(id);
  list.textContent='';
  const entries=Object.entries(totals).sort((a,b)=>Number(b[1])-Number(a[1]) || a[0].localeCompare(b[0]));
  if (!entries.length) { const row=document.createElement('li'); row.className='empty'; row.textContent='No samples since this version'; list.append(row); return; }
  for (const [name,totalValue] of entries.slice(0,8)) {
    const total=Number(totalValue), failed=Number(failures[name] || 0);
    const row=document.createElement('li'), label=document.createElement('span'), value=document.createElement('strong');
    label.textContent=name || 'unknown';
    value.textContent=`${failed}/${total} · ${total ? (100*failed/total).toFixed(1) : '0.0'}%`;
    row.append(label,value); list.append(row);
  }
}

function updateScheduleFields() {
  const frequency=$('schedule-form').elements.frequency.value;
  for (const field of $('schedule-form').querySelectorAll('[data-frequency]')) field.hidden=!field.dataset.frequency.split(' ').includes(frequency);
}

async function createSchedule(event) {
  event.preventDefault();
  const form=event.currentTarget, values=new FormData(form), frequency=String(values.get('frequency'));
  const timing={type:frequency};
  if (frequency==='at') {
    const date=new Date(String(values.get('at')));
    if (!Number.isFinite(date.getTime())) { $('schedule-message').textContent='Choose a future date and time.'; return; }
    timing.at=date.toISOString();
  } else if (frequency==='interval') timing.interval_seconds=Number(values.get('interval'))*60;
  else {
    timing.timezone=String(values.get('timezone') || Intl.DateTimeFormat().resolvedOptions().timeZone || 'Local').trim();
    if (frequency==='cron') timing.cron=String(values.get('cron') || '').trim();
    else timing.time=String(values.get('time') || '').trim();
    if (frequency==='weekly') timing.days=String(values.get('days') || '').split(',').map((item)=>item.trim()).filter(Boolean);
  }
  const alternatives=(name)=>String(values.get(name) || '').split(',').map((item)=>item.trim()).filter(Boolean);
  const job={prompt:String(values.get('prompt') || ''),route:String(values.get('route') || 'default').trim() || 'default',
    output:{mode:'text'}};
  for (const name of ['provider','model','reasoning']) { const value=String(values.get(name) || '').trim(); if (value) job[name]=value; }
  job.metadata={contextbridge_foreground_new_chat:values.has('foreground')};
  if (values.has('new-chat')) job.metadata.contextbridge_new_chat_per_run=true;
  const followUp=String(values.get('follow-up-prompt') || '').trim();
  const carry=String(values.get('follow-up-artifact') || '');
  if (carry && !followUp) { $('schedule-message').textContent='Add a follow-up prompt to carry an artifact.'; return; }
  if (carry) { job.output.artifacts=true; if (carry==='image') job.output.min_images=1; else job.output.min_artifacts=1; }
  const steps=followUp?[{name:'Follow-up',job:{route:job.route,prompt:followUp,output:{mode:'text'},...(job.provider?{provider:job.provider}:{}),...(job.model?{model:job.model}:{}),...(job.reasoning?{reasoning:job.reasoning}:{})},...(carry?{use_previous_artifact:carry}:{})}]:[];
  const payload={name:String(values.get('name') || '').trim(),job,timing,
    fallback:{models:alternatives('model-fallbacks'),reasoning:alternatives('reasoning-fallbacks')},steps};
  $('schedule-message').textContent='Saving…';
  try {
    const response=await fetch('/v1/schedules',{method:'POST',headers:{Authorization:`Bearer ${token}`,'Content-Type':'application/json'},body:JSON.stringify(payload)});
    const result=await response.json();
    if (!response.ok) throw new Error(result.error || `HTTP ${response.status}`);
    $('schedule-message').textContent=`Created ${result.name}.`;
    form.reset(); updateScheduleFields(); await refresh();
  } catch (error) { $('schedule-message').textContent=error.message || String(error); }
}

function renderSchedules(items) {
  $('schedule-count').textContent=`${items.length} schedule${items.length===1?'':'s'}`;
  const list=$('schedule-list'); list.textContent='';
  if (!items.length) { const empty=document.createElement('p'); empty.className='empty'; empty.textContent='No scheduled jobs yet.'; list.append(empty); return; }
  for (const item of items) {
    const row=document.createElement('article'), detail=document.createElement('div'), title=document.createElement('strong'), state=document.createElement('span'), controls=document.createElement('div');
    row.className='schedule-row'; title.textContent=item.name || item.id;
    const next=item.enabled && meaningfulTimestamp(item.next_run) ? `next ${new Date(item.next_run).toLocaleString()}` : 'paused / finished';
    const last=item.last_outcome ? ` · last ${item.last_outcome}${item.last_error ? ` (${item.last_error})` : ''}` : '';
    state.textContent=`${item.timing?.type || 'once'} · ${item.route || 'default'} · ${item.provider || 'route provider'} · ${item.model || 'auto model'} · ${item.reasoning || 'default reasoning'} · ${item.step_count || 1} step(s) · ${item.current_run_id ? 'running' : item.waiting_reason || next}${last}`;
    detail.append(title,state);
    if (item.enabled && meaningfulTimestamp(item.next_run)) { const countdown=document.createElement('small'); countdown.dataset.nextRun=item.next_run; detail.append(countdown); }
    if (item.history?.length) {
      const history=document.createElement('details'), summary=document.createElement('summary'), list=document.createElement('ol');
      summary.textContent=`History · ${item.history.length} recent run(s)`; history.append(summary);
      for (const run of item.history) {
        const entry=document.createElement('li'), link=document.createElement('span');
        link.textContent=`${new Date(run.started_at).toLocaleString()} · ${run.outcome} · ${run.id}`; entry.append(link);
        for (const step of run.steps || []) { const sub=document.createElement('small'); sub.textContent=`${step.name} · ${step.outcome} · ${step.id}`; entry.append(sub); }
        list.append(entry);
      }
      history.append(list); detail.append(history);
    }
    for (const [action,label] of [[item.enabled?'pause':'resume',item.enabled?'Pause':'Resume'],['run','Run now'],['delete','Delete']]) {
      const button=document.createElement('button'); button.type='button'; button.className='outline'; button.dataset.action=action; button.dataset.id=item.id; button.textContent=label; controls.append(button);
    }
    row.append(detail,controls); list.append(row);
  }
  updateScheduleCountdowns();
}

async function scheduleAction(event) {
  const button=event.target.closest('button[data-action]'); if (!button) return;
  const action=button.dataset.action, id=button.dataset.id;
  if (action==='delete' && !window.confirm('Delete this schedule? A running job cannot be unsent.')) return;
  button.disabled=true;
  try {
    const response=await fetch(`/v1/schedules/${encodeURIComponent(id)}${action==='delete'?'':`/${action}`}`,{
      method:action==='delete'?'DELETE':'POST',headers:{Authorization:`Bearer ${token}`}});
    const result=await response.json(); if (!response.ok) throw new Error(result.error || `HTTP ${response.status}`);
    $('schedule-message').textContent=action==='run'?`Run accepted: ${result.run_id}`:`Schedule ${action}d.`;
    await refresh();
  } catch (error) { $('schedule-message').textContent=error.message || String(error); button.disabled=false; }
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
