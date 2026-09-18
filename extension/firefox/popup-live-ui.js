(() => {
  const api = globalThis.browser || globalThis.chrome;
  const $ = (selector, root = document) => root.querySelector(selector);
  const $$ = (selector, root = document) => [...root.querySelectorAll(selector)];
  const reduceMotion = matchMedia('(prefers-reduced-motion: reduce)');
  let filter = 'all';
  let locale = 'en';
  let current = null;
  let providerLogos = {};
  let refreshTask = null;

  const text = {
    en: { connected:'Connected', disconnected:'Not connected', connecting:'Connecting', attached:'Attached', free:'Free', working:'Working', heartbeat:'Heartbeat', ready:'Ready', available:'Available', detach:'Detach', show:'Show', hide:'Hide', unknown:'Unknown' },
    de: { connected:'Verbunden', disconnected:'Nicht verbunden', connecting:'Verbindung wird hergestellt', attached:'Angehängt', free:'Frei', working:'Aktiv', heartbeat:'Heartbeat', ready:'Bereit', available:'Verfügbar', detach:'Trennen', show:'Anzeigen', hide:'Verbergen', unknown:'Unbekannt' }
  };
  const t = (key) => text[locale]?.[key] || text.en[key] || key;

  document.addEventListener('DOMContentLoaded', () => {
    providerLogos = { chatgpt: $('.provider-link:not(.gemini) img')?.src || '', gemini: $('.provider-link.gemini img')?.src || '' };
    bind();
    setTimeout(() => void refresh(true), 80);
    setInterval(() => { if (!document.hidden) void refresh(false); }, 2500);
  });
  api.storage.onChanged?.addListener((changes, area) => {
    if (area === 'local' && Object.keys(changes).some((key) => !key.startsWith('ui'))) void refresh(true);
  });

  function bind() {
    $$('.disclosure-trigger').forEach((button) => button.addEventListener('click', () => openDisclosure(button.closest('[data-disclosure]')?.dataset.disclosure)));
    $$('.filter-button').forEach((button) => button.addEventListener('click', () => {
      filter = button.dataset.filter || 'all';
      $$('.filter-button').forEach((item) => item.classList.toggle('active', item === button));
      const legacy = $('#tab-filter'); if (legacy) { legacy.value = filter; legacy.dispatchEvent(new Event('change')); }
      void refresh(false);
    }));
    $('#main-action').addEventListener('click', () => {
      const action = $('#main-action').dataset.action;
      if (action === 'connect') $('#pair').click();
      else if (action === 'attach') $('#toggle-current-tab').click();
    });
    $('#quick-detach').addEventListener('click', () => $('#toggle-current-tab').click());
    $('#tab-list').addEventListener('click', (event) => {
      const button = event.target.closest('.tab-action');
      if (!button) return;
      const select = $('#tab');
      select.value = button.dataset.tabId;
      select.dispatchEvent(new Event('change'));
      $('#toggle-selected-tab').click();
    });
    $$('[data-setting]').forEach((button) => button.addEventListener('click', () => proxySwitch(button)));
    $$('#session-options .option-card').forEach((button) => button.addEventListener('click', () => {
      $('#session-mode').value = button.dataset.mode;
      $('#session-mode').dispatchEvent(new Event('change'));
      closePopovers();
    }));
    $('#edit-switch').addEventListener('click', () => { $('#edit-last-message').checked = $('#edit-switch').getAttribute('aria-checked') !== 'true'; $('#edit-last-message').dispatchEvent(new Event('change')); });
    $('#copy-console').addEventListener('click', () => copyValue('cb console'));
    $('#popover-console').addEventListener('click', () => copyValue('cb console'));
    $('#copy-doctor').addEventListener('click', () => copyValue('cb doctor'));
    $$('.copy-chip').forEach((button) => button.addEventListener('click', () => copyValue(button.dataset.command || button.textContent)));
    $$('[data-copy]').forEach((button) => button.addEventListener('click', () => copyValue($(button.dataset.copy)?.value || '')));
    $('#connection-button').addEventListener('click', () => togglePopover($('#connection-popover'), $('#connection-button')));
    $('#scope-button').addEventListener('click', () => togglePopover($('#access-popover'), $('#scope-button')));
    $('#page-menu-button').addEventListener('click', () => togglePopover($('#page-menu'), $('#page-menu-button')));
    $('#session-policy-button').addEventListener('click', () => togglePopover($('#session-policy-popover'), $('#session-policy-button')));
    $('#appearance-button').addEventListener('click', () => togglePopover($('#appearance-popover'), $('#appearance-button')));
    $('#language-button').addEventListener('click', () => togglePopover($('#language-popover'), $('#language-button')));
    $('#manage-access').addEventListener('click', () => { closePopovers(); openDisclosure('tabs', true); });
    $$('.appearance-option').forEach((button) => button.addEventListener('click', () => saveUI({ uiTextSize: button.dataset.size })));
    $$('.theme-option').forEach((button) => button.addEventListener('click', () => saveUI({ uiTheme: button.dataset.themeOption })));
    document.addEventListener('click', (event) => {
      if (!event.target.closest('.popover, #connection-button, #scope-button, #page-menu-button, #session-policy-button, #appearance-button, #language-button')) closePopovers();
    });
    document.addEventListener('keydown', (event) => { if (event.key === 'Escape') closePopovers(); });
    renderLanguages();
  }

  async function refresh(forceService) {
    if (refreshTask) return refreshTask;
    refreshTask = refreshOnce(forceService).finally(() => { refreshTask = null; });
    return refreshTask;
  }

  async function refreshOnce(forceService) {
    const saved = await storage();
    locale = saved.uiLocale || ((navigator.language || '').toLowerCase().startsWith('de') ? 'de' : 'en');
    applyUI(saved);
    let runtime = { tabs: [] };
    try { runtime = await api.runtime.sendMessage({ type: 'status' }) || runtime; } catch (_) {}
    try { current = (await api.tabs.query({ active:true, currentWindow:true })).find((tab) => tab.id) || null; } catch (_) { current = null; }
    let tabs = [];
    try { tabs = await api.tabs.query(await hasTabsPermission() ? {} : { currentWindow:true }); } catch (_) { if (current) tabs = [current]; }
    for (const id of saved.tabIds || []) if (!tabs.some((tab) => tab.id === Number(id))) { try { tabs.push(await api.tabs.get(Number(id))); } catch (_) {} }
    let service = window.__cbServiceStatus || null;
    if (forceService || !service || Date.now() - Number(service._at || 0) > 10000) service = await getService(saved);
    window.__cbServiceStatus = service;
    render(saved, runtime, tabs, service);
  }

  async function getService(saved) {
    if (!saved.token) return null;
    try {
      const response = await fetch(`${saved.bridgeUrl}/v1/status`, { headers:{ Authorization:`Bearer ${saved.token}` }, cache:'no-store' });
      if (!response.ok) return { ok:false, status:response.status, _at:Date.now() };
      return { ...(await response.json()), _at:Date.now() };
    } catch (_) { return { ok:false, status:0, _at:Date.now() }; }
  }

  function render(saved, runtime, tabs, service) {
    const attached = [...new Set((saved.tabIds || []).map(Number).filter(Boolean))];
    const live = new Map((runtime.tabs || []).map((tab) => [Number(tab.id), tab]));
    const heartbeatAge = saved.lastHeartbeatAt ? Date.now() - Number(saved.lastHeartbeatAt) : Infinity;
    const online = saved.running && saved.relayConnected && heartbeatAge < 60000;
    const pending = $('#pair')?.getAttribute('aria-busy') === 'true';
    const connectionState = saved.connectionError || (saved.running && saved.lastError) ? 'error' : pending ? 'connecting' : online ? 'live' : saved.running ? 'reconnecting' : 'idle';
    const connectionLabel = connectionState === 'live' ? t('connected') : connectionState === 'idle' ? t('disconnected') : connectionState === 'error' ? (locale === 'de' ? 'Prüfen' : 'Check') : t('connecting');
    $('#connection-button').dataset.state = connectionState;
    liveText($('#connection-label'), connectionLabel);
    $('#route-line').dataset.state = connectionState;
    $('#connection-popover-title').textContent = connectionLabel;
    $('#connection-popover-copy').textContent = saved.connectionError || saved.lastError || (online ? (locale === 'de' ? 'Relay und Browser-Pool sind bereit.' : 'Relay and browser pool are ready.') : (locale === 'de' ? 'Verbinde die angehängten Tabs, sobald du bereit bist.' : 'Connect attached tabs when you are ready.'));
    $('#disconnect-button').disabled = !saved.running;
    const busy = [...live.values()].filter((tab) => tab.busy).length;
    const cooling = [...live.values()].filter((tab) => tab.state === 'rate_limited').length;
    counter($('#metric-tabs'), attached.length, ' <small>/ 16</small>');
    counter($('#metric-ready'), Math.max(0, attached.length - busy - cooling));
    counter($('#metric-busy'), busy);
    liveText($('#metric-heartbeat'), online ? heartbeatAge < 2500 ? (locale === 'de' ? 'jetzt' : 'now') : `${Math.floor(heartbeatAge/1000)}s` : '—');
    $('#connection-heartbeat').textContent = online ? `${Math.max(0, Math.floor(heartbeatAge/1000))}s` : '—';
    $('#connection-slots').textContent = `${attached.length} / 16`;
    liveText($('#route-extension-meta'), `${attached.length} ${attached.length === 1 ? 'slot' : 'slots'}${busy ? ` · ${busy} ${locale === 'de' ? 'aktiv' : 'busy'}` : ''}`);
    renderService(saved, service, attached);
    renderCurrent(saved, live, attached, pending);
    renderTabs(saved, tabs, live, attached);
    renderSettings(saved, service);
    renderDetection(saved, live);
  }

  function renderService(saved, service, attached) {
    const ok = service?.ok === true;
    const browser = service?.browser || {};
    const pool = service?.pool || {};
    const hardware = service?.runtime?.hardware || {};
    const gpus = hardware.gpus || [];
    const nodeLabel = pool.nodes_total ? `${pool.nodes_online || 0}/${pool.nodes_total} nodes · ${pool.slots_busy || 0}/${pool.slots_total || 0} slots`
      : ok ? `${locale === 'de' ? 'lokaler Node' : 'local node'} · ${attached.length} browser slots` : (locale === 'de' ? 'nicht geprüft' : 'not checked');
    liveText($('#route-service-meta'), nodeLabel);
    liveText($('#service-meta'), ok ? `v${service.version || ''}` : (locale === 'de' ? 'Nicht bereit' : 'Not ready'));
    liveText($('#service-title'), ok ? (locale === 'de' ? 'Lokaler Dienst bereit' : 'Local service ready') : (locale === 'de' ? 'Lokaler Dienst nicht geprüft' : 'Local service not checked'));
    $('#pairing-status').textContent = !saved.token ? 'missing' : service?.status === 401 ? 'invalid' : ok ? 'paired' : 'unknown';
    $('#service-copy').textContent = !saved.token ? (locale === 'de' ? 'Pairing-Token einfügen, dann Verbindung prüfen.' : 'Enter the pairing token, then test the connection.') : ok ? (locale === 'de' ? 'Authentifiziert; Browser-Jobs und Diagnose sind verfügbar.' : 'Authenticated; browser jobs and diagnostics are available.') : (locale === 'de' ? 'Prüfe Dienst, URL und Token.' : 'Check the service, URL and token.');
    let resources = $('.service-resource-line');
    if (!resources) { resources = document.createElement('div'); resources.className = 'service-resource-line'; $('#service-copy').after(resources); }
    const poolCapacity = pool.capability_view ? [
      pool.cpu_cores ? `${pool.cpu_cores} CPU cores` : '',
      pool.gpus ? `${pool.gpus} GPU${pool.gpus === 1 ? '' : 's'} · ${bytes(pool.vram_free_bytes)}/${bytes(pool.vram_total_bytes)} VRAM ${locale === 'de' ? 'frei' : 'free'}` : (pool.zero_gpu_nodes ? `${pool.zero_gpu_nodes} Zero-GPU node${pool.zero_gpu_nodes === 1 ? '' : 's'}` : ''),
      pool.memory_total_bytes ? `${bytes(pool.memory_free_bytes)}/${bytes(pool.memory_total_bytes)} RAM ${locale === 'de' ? 'frei' : 'free'}` : ''
    ].filter(Boolean).join(' · ') : '';
    const resourceText = poolCapacity || (gpus.length ? `${gpus.length} GPU${gpus.length === 1 ? '' : 's'} · ${bytes(gpus.reduce((sum,gpu)=>sum+Number(gpu.memory_free_bytes||0),0))} VRAM ${locale === 'de' ? 'frei' : 'free'}`
      : hardware.cpu_cores ? `${hardware.cpu_cores} CPU cores · ${bytes(hardware.memory_available_bytes)} RAM ${locale === 'de' ? 'frei' : 'free'}` : (locale === 'de' ? 'Ressourcen nach erfolgreicher Prüfung sichtbar' : 'Resources appear after a successful check'));
    liveText(resources, resourceText);
    $('#connection-popover .connection-detail-row strong').textContent = saved.bridgeUrl.replace(/^https?:\/\//,'');
    setSwitch('automatic_updates', Boolean(service?.updates?.enabled), !ok);
    $('#update-copy').textContent = service?.updates?.enabled ? (locale === 'de' ? 'An für diesen PC. Installation nur im Leerlauf.' : 'On for this PC. Installs only while idle.') : (locale === 'de' ? 'Aus für diesen PC.' : 'Off for this PC.');
    void browser;
  }

  function renderCurrent(saved, live, attached, pending) {
    const available = Boolean(current?.id && /^https?:/i.test(current.url || ''));
    const profile = profileFor(current, saved);
    const valid = Boolean(profile);
    const isAttached = available && attached.includes(current.id);
    const tab = live.get(current?.id);
    const caps = saved.tabCapabilities?.[current?.id] || {};
    const binding = Object.values(saved.sessionBindings || {}).find((item)=>Number(item?.tabId)===current?.id);
    $('#valid-page').hidden = !valid;
    $('#invalid-page').hidden = valid;
    $('#teach-page-link').hidden = !available || valid;
    liveText($('#scope-label'), isAttached ? (locale === 'de' ? 'Angehängt' : 'Attached') : available ? (locale === 'de' ? 'Nicht angehängt' : 'Not attached') : (locale === 'de' ? 'Kein Zugriff' : 'No access'));
    $('#access-tab-value').textContent = isAttached ? t('attached') : (locale === 'de' ? 'Nicht angehängt' : 'Not attached');
    $('#access-origin-value').textContent = available ? host(current.url) : (locale === 'de' ? 'Keine' : 'None');
    $('#invalid-title').textContent = available ? (locale === 'de' ? 'Diese AI-Seite ist noch nicht eingerichtet' : 'This AI page is not configured yet') : (locale === 'de' ? 'Hier ist keine unterstützte AI-Seite' : 'No supported AI page here');
    $('#invalid-copy').textContent = available ? (locale === 'de' ? 'Lehre ContextBridge diese HTTPS-Seite. Ohne Freigabe wird nichts angehängt.' : 'Teach ContextBridge this HTTPS page. Nothing is attached without approval.') : (locale === 'de' ? 'Öffne ChatGPT oder Gemini und danach ContextBridge erneut.' : 'Open ChatGPT or Gemini, then open ContextBridge again.');
    if (!valid) return;
    const provider = profile.name === 'gemini' ? 'gemini' : profile.name === 'chatgpt' ? 'chatgpt' : 'custom';
    $('#provider-icon').dataset.provider = provider;
    if (providerLogos[provider]) { $('#provider-logo').src = providerLogos[provider]; $('#provider-logo').hidden = false; } else $('#provider-logo').hidden = true;
    liveText($('#provider-name'), String(profile.label || profile.name).replace(/ \(auto-detected\)$/i,''));
    liveText($('#provider-meta'), `${host(current.url)} · ${locale === 'de' ? 'aktueller Tab' : 'current tab'}`);
    const state = tab?.busy ? 'working' : tab?.state === 'rate_limited' ? 'cooling' : isAttached ? 'ready' : 'detected';
    $('#page-state').dataset.state = state;
    liveText($('#page-state'), tab?.busy ? t('working') : tab?.state === 'rate_limited' ? (locale === 'de' ? 'Abkühlphase' : 'Cooling down') : isAttached ? t('attached') : (locale === 'de' ? 'Erkannt' : 'Detected'));
    const model = tab?.currentModel || caps.currentModel || '';
    const reasoning = caps.currentReasoning || '';
    const tags = [[locale === 'de' ? 'Modell' : 'Model',model],[locale === 'de' ? 'Denkstufe' : 'Reasoning',reasoning],['Session',binding?.label]].filter(([,value])=>value);
    $('#context-tags').replaceChildren(...tags.map(([label,value])=>{const node=document.createElement('span');node.className='context-tag';node.append(`${label} · `);const strong=document.createElement('strong');strong.textContent=value;node.append(strong);return node;}));
    const action = pending ? 'pending' : !isAttached ? 'attach' : !saved.running ? 'connect' : 'status';
    const button = $('#main-action'); button.dataset.action = action; button.disabled = action === 'pending' || action === 'status';
    button.classList.toggle('is-loading', action === 'pending'); button.classList.toggle('is-attached-status', action === 'status');
    const label = action === 'attach' ? (locale === 'de' ? 'Diesen Tab anhängen' : 'Attach this tab') : action === 'connect' ? (locale === 'de' ? 'Angehängte Tabs verbinden' : 'Connect attached tabs') : action === 'pending' ? t('connecting') : tab?.busy ? (locale === 'de' ? 'ContextBridge-Job läuft' : 'ContextBridge job is running') : (locale === 'de' ? 'Bereit für Jobs' : 'Ready for jobs');
    $('#main-action-label').textContent = label; $('#connect-progress-copy').textContent = label;
    $('.page-action-row').dataset.hasQuickDetach = String(isAttached); $('#quick-detach').hidden = !isAttached; $('#quick-detach').textContent = t('detach'); $('#page-menu-button').hidden = !isAttached;
    $('#action-explainer-copy').textContent = !isAttached ? (locale === 'de' ? 'Nur dieser Tab wird nach deiner Browser-Freigabe Teil des Pools.' : 'Only this tab joins the pool after browser permission.') : saved.running ? (locale === 'de' ? 'Der Tab bleibt bis zum Trennen verfügbar.' : 'This tab stays available until detached.') : (locale === 'de' ? 'Angehängt; der Relay ist noch nicht verbunden.' : 'Attached; the relay is not connected yet.');
    const health = advisory(tab?.pageHealth); $('#health-advisory').hidden = !health; $('#health-copy').textContent = health;
    $('#edit-switch').setAttribute('aria-checked', String(saved.tabEditModes?.[current.id] === true)); $('#edit-switch').disabled = !isAttached || tab?.busy || !['chatgpt','gemini'].includes(profile.name); $('#edit-state').textContent = saved.tabEditModes?.[current.id] ? (locale === 'de' ? 'An' : 'On') : (locale === 'de' ? 'Aus' : 'Off');
    $('#release-session').disabled = !isAttached || tab?.busy; $('#page-menu-context').textContent = `${profile.label || profile.name} · ${isAttached ? t('attached') : locale === 'de' ? 'Nicht angehängt' : 'Not attached'}`;
  }

  function renderTabs(saved, tabs, live, attached) {
    const rows = tabs.filter((tab)=>tab.id && /^https?:/i.test(tab.url||'') && (attached.includes(tab.id)||profileFor(tab,saved))).filter((tab)=>filter==='all'||(filter==='attached')===attached.includes(tab.id));
    $('#tabs-meta').textContent = locale === 'de' ? `${attached.length} angehängt` : `${attached.length} attached`;
    hasTabsPermission().then((granted)=>{ $('#tabs-permission').hidden=granted; });
    $('#tab-list').replaceChildren(...rows.map((tab)=>{
      const isAttached=attached.includes(tab.id), profile=profileFor(tab,saved), provider=profile?.name==='gemini'?'gemini':'chatgpt', state=live.get(tab.id), caps=saved.tabCapabilities?.[tab.id]||{};
      const row=document.createElement('div'); row.className='tab-row'; const logo=providerLogos[provider]?`<img src="${providerLogos[provider]}" alt="">`:(provider==='gemini'?'✦':'◉'); const label=state?.busy?t('working'):isAttached?t('attached'):t('available');
      row.innerHTML=`<div class="tab-logo ${provider==='gemini'?'gemini':''}">${logo}</div><div class="tab-copy"><strong>${esc(tab.title||host(tab.url))}</strong><span>${esc(state?.currentModel||caps.currentModel||profile?.label||host(tab.url))}</span></div><div class="tab-side"><span class="tab-status ${state?.busy?'working':isAttached?'ready':'available'}"><i></i>${esc(label)}</span><button class="tab-action" data-tab-id="${tab.id}" type="button" aria-label="${isAttached?'Detach':'Attach'}"><svg viewBox="0 0 24 24" fill="none"><path d="${isAttached?'M6 12h12':'M12 6v12M6 12h12'}" stroke="currentColor" stroke-width="1.6" stroke-linecap="round"/></svg></button></div>`;
      return row;
    }));
  }

  function renderSettings(saved, service) {
    setSwitch('reconnect',saved.autoReconnect); setSwitch('preserve_drafts',saved.preserveDrafts); setSwitch('auto_attach',saved.autoAttachFreshTabs); setSwitch('auto_close',saved.autoCloseFinishedChats); setSwitch('automatic_updates',Boolean(service?.updates?.enabled),!service?.ok);
    $$('#session-options .option-card').forEach((button)=>button.setAttribute('aria-checked',String(button.dataset.mode===saved.sessionMode)));
    const mode=saved.sessionMode||'manual', title=mode==='new_chat'?(locale==='de'?'Neuer Chat je Session':'New chat per session'):mode==='new_chat_per_job'?(locale==='de'?'Neuer Chat für jeden Job':'New chat for every job'):(locale==='de'?'Angehängten Chat wiederverwenden':'Reuse an attached chat');
    liveText($('#session-policy-value'),title); $('#session-policy-hint').textContent=mode==='manual'?(locale==='de'?'Du entscheidest, wann ein sauberer Chat wieder verfügbar ist.':'You decide when a clean chat becomes available again.'):(locale==='de'?'Sessions teilen nie still eine Unterhaltung.':'Sessions never silently share a conversation.');
  }

  function renderDetection(saved, live) {
    const profile=profileFor(current,saved), caps=saved.tabCapabilities?.[current?.id]||{}, scan=saved.tabCapabilityScans?.[current?.id]||{}, model=live.get(current?.id)?.currentModel||caps.currentModel||t('unknown'), reasoning=caps.currentReasoning||t('unknown');
    liveText($('#detection-meta'),profile?(model===t('unknown')?(locale==='de'?'Erkannt':'Detected'):model):(locale==='de'?'Nicht erkannt':'Not detected'));
    liveText($('#evidence-profile'),profile?.name||'—'); liveText($('#evidence-model'),model); liveText($('#evidence-reasoning'),reasoning); liveText($('#evidence-controls'),scan.scannedAt||scan.scanned_at?(locale==='de'?'Geprüft':'Scanned'):(locale==='de'?'Nicht geprüft':'Not scanned'));
    $('#profile-status').textContent=profile?(profile.source==='builtin'?(locale==='de'?'automatisch':'automatic'):(locale==='de'?'angepasst':'custom')):(locale==='de'?'nicht erkannt':'not detected');
  }

  function proxySwitch(button) {
    const ids={reconnect:'auto-reconnect',preserve_drafts:'preserve-drafts',auto_attach:'auto-attach-fresh',auto_close:'auto-close-finished',automatic_updates:'auto-update'}; const control=$(`#${ids[button.dataset.setting]}`); if(!control||control.disabled)return; control.checked=button.getAttribute('aria-checked')!=='true'; control.dispatchEvent(new Event('change'));
  }
  function setSwitch(name,checked,disabled=false){const button=$(`[data-setting="${name}"]`);if(!button)return;button.setAttribute('aria-checked',String(Boolean(checked)));button.disabled=disabled;const label=button.closest('.toggle-control')?.querySelector('.toggle-state');if(label)label.textContent=checked?(locale==='de'?'An':'On'):(locale==='de'?'Aus':'Off');}
  function profileFor(tab,saved){if(!tab?.url||!/^https?:/i.test(tab.url))return null;let origin='';try{origin=new URL(tab.url).origin;}catch(_){}return saved.taughtProfiles?.[origin]||globalThis.ContextBridgeProfiles?.forURL(tab.url)||null;}
  function advisory(health={}){const turns=Math.max(Number(health.assistantTurns)||0,Number(health.userTurns)||0);if(health.discarded)return locale==='de'?'Browser hat die Seite entladen. Jobs warten statt blind erneut zu senden.':'Browser discarded this page. Jobs wait instead of resending blindly.';if(['timeout','error'].includes(health.domStatus))return locale==='de'?'Die Seite antwortet nicht auf den begrenzten Steuerungs-Scan.':'The page is not answering the bounded control scan.';if(turns>=120)return `${locale==='de'?'Langer Chat':'Long conversation'}: ${turns} turns.`;return '';}

  function liveText(element,value){if(!element)return;const next=String(value??''),old=element.textContent||'';if(next===old)return;if(reduceMotion.matches||!old){element.textContent=next;return;}element.dataset.ghostText=old;element.textContent=next;element.classList.add('live-value');element.classList.remove('live-swap');void element.offsetWidth;element.classList.add('live-swap');clearTimeout(element._swap);element._swap=setTimeout(()=>{element.classList.remove('live-swap');delete element.dataset.ghostText;},320);}
  function counter(element,target,suffix=''){const next=Math.max(0,Number(target)||0),previous=Number(element.dataset.value);element.dataset.value=String(next);if(!Number.isFinite(previous)||previous===next||reduceMotion.matches){element.innerHTML=`${next}${suffix}`;return;}element.parentElement.dataset.direction=next>previous?'up':'down';const started=performance.now();const tick=(now)=>{const p=Math.min(1,(now-started)/280),e=1-Math.pow(1-p,3);element.innerHTML=`${Math.round(previous+(next-previous)*e)}${suffix}`;if(p<1)requestAnimationFrame(tick);};element.classList.remove('slide-in');void element.offsetWidth;element.classList.add('slide-in');requestAnimationFrame(tick);}
  function openDisclosure(name,force=null){const trigger=$(`[data-disclosure="${name}"] .disclosure-trigger`);if(!trigger)return;const open=force??trigger.getAttribute('aria-expanded')!=='true';$$('.disclosure-trigger[aria-expanded="true"]').forEach((other)=>{if(other!==trigger)other.setAttribute('aria-expanded','false');});trigger.setAttribute('aria-expanded',String(open));}
  function togglePopover(popover,anchor){const open=!popover.classList.contains('open');closePopovers(popover);popover.classList.toggle('open',open);anchor.setAttribute('aria-expanded',String(open));if(open)requestAnimationFrame(()=>positionPopover(popover,anchor));}
  function positionPopover(popover,anchor){const rect=anchor.getBoundingClientRect(),width=popover.offsetWidth;popover.style.left=`${Math.max(8,Math.min(innerWidth-width-8,rect.right-width))}px`;const header=anchor.closest('.app-header');popover.style.top=`${Math.max(8,Math.min(innerHeight-popover.offsetHeight-8,(header?header.getBoundingClientRect().bottom:rect.bottom)+7))}px`;}
  function closePopovers(except=null){$$('.popover.open').forEach((node)=>{if(node!==except)node.classList.remove('open');});$$('[aria-expanded="true"]').filter((node)=>!node.classList.contains('disclosure-trigger')).forEach((node)=>node.setAttribute('aria-expanded','false'));}
  function renderLanguages(){const list=$('#language-list');list.replaceChildren(...[['en','English'],['de','Deutsch']].map(([code,name])=>{const button=document.createElement('button');button.className='language-option';button.type='button';button.innerHTML=`<span class="language-code">${code.toUpperCase()}</span><span class="language-name"><strong>${name}</strong><span>${code==='de'?'Deutsch verwenden':'Use English'}</span></span>`;button.addEventListener('click',()=>saveUI({uiLocale:code}));return button;}));}
  async function saveUI(patch){await api.storage.local.set(patch);closePopovers();await refresh(false);}
  function applyUI(saved){document.documentElement.lang=locale;$('#language-code').textContent=locale.toUpperCase();const pref=['system','light','dark'].includes(saved.uiTheme)?saved.uiTheme:'system',theme=pref==='system'?(matchMedia('(prefers-color-scheme: dark)').matches?'dark':'light'):pref;document.documentElement.dataset.theme=theme;document.documentElement.dataset.themePref=pref;document.documentElement.dataset.text=['compact','comfortable','large'].includes(saved.uiTextSize)?saved.uiTextSize:'comfortable';$$('.theme-option').forEach((b)=>b.classList.toggle('active',b.dataset.themeOption===pref));$$('.appearance-option').forEach((b)=>b.classList.toggle('active',b.dataset.size===document.documentElement.dataset.text));}
  async function copyValue(value){try{await navigator.clipboard.writeText(String(value));toast(locale==='de'?'Kopiert':'Copied');}catch(_){toast(locale==='de'?'Kopieren fehlgeschlagen':'Copy failed');}}
  function toast(message){const node=document.createElement('div');node.className='toast';node.textContent=message;$('#toast-stack').append(node);setTimeout(()=>node.remove(),3500);}
  function host(value){try{return new URL(value).host;}catch(_){return '';}} function bytes(value){const n=Number(value)||0;if(!n)return '0 B';const u=['B','KiB','MiB','GiB','TiB'],i=Math.min(u.length-1,Math.floor(Math.log(n)/Math.log(1024)));return `${(n/1024**i).toFixed(i>2?1:0)} ${u[i]}`;} function esc(value){return String(value??'').replace(/[&<>"']/g,(c)=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));}
  async function hasTabsPermission(){try{return await api.permissions.contains({permissions:['tabs']});}catch(_){return false;}}
  async function storage(){return api.storage.local.get({bridgeUrl:'http://127.0.0.1:32145',token:'',tabIds:[],running:false,relayConnected:false,lastHeartbeatAt:0,connectionError:'',lastError:'',autoReconnect:true,autoAttachFreshTabs:false,preserveDrafts:false,autoCloseFinishedChats:false,sessionMode:'manual',tabEditModes:{},taughtProfiles:{},sessionBindings:{},tabCapabilities:{},tabCapabilityScans:{},uiLocale:'',uiTheme:'system',uiTextSize:'comfortable'});}
})();
