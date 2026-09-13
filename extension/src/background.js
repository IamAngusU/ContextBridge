if (!globalThis.ContextBridgeProfiles && typeof importScripts === 'function') {
  importScripts('profiles.js');
}

const api = globalThis.browser || globalThis.chrome;

let polling = false;
let stopRequested = false;
let heartbeatTimer = 0;

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
    case 'remove-profile':
      return removeTaughtProfile(String(message.origin || ''));
    default:
      return { ok: false, error: 'Unknown extension message' };
  }
}

async function startPairing() {
  const cfg = await settings();
  if (!cfg.token) throw new Error('Enter the local pairing token first');
  if (!cfg.tabId) throw new Error('Select an AI tab first');
  const tab = await api.tabs.get(cfg.tabId);
  if (!tab?.url || !/^https?:/i.test(tab.url)) throw new Error('The selected tab is not a supported web page');
  if (cfg.useVisualProfile && !profileForTab(cfg, tab)) throw new Error('Detect or customize this page before starting the bridge');

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
  let tab = null;
  if (cfg.tabId) {
    try {
      tab = await api.tabs.get(cfg.tabId);
    } catch (_) {}
  }
  const profile = tab ? profileForTab(cfg, tab) : null;
  return {
    ok: true,
    running: cfg.running,
    tab: tab ? tabSummary(tab) : null,
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
  await api.storage.local.set({ tabId, teachingTabId: tabId, running: false });
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
  if (polling) return;
  polling = true;
  try {
    while (!stopRequested) {
      const cfg = await settings();
      if (!cfg.running || !cfg.token || !cfg.tabId) break;
      await flushPendingCompletions(cfg);
      try {
        let requestedProfile = cfg.profile || '';
        if (cfg.useVisualProfile) {
          try {
            const selectedTab = await api.tabs.get(cfg.tabId);
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
        await processWork(cfg, work);
      } catch (error) {
        await api.storage.local.set({ lastError: error.message || String(error) });
        await delay(2000);
      }
    }
  } finally {
    polling = false;
  }
}

async function processWork(cfg, work) {
  let decision;
  let tab;
  let effectiveProfile = work.profile || {};
  let leaseTimer = 0;
  let progressTimer = 0;
  let progressBusy = false;
  let progressSequence = 0;
  let baselineText = '';
  let latestProgressText = '';
  await api.storage.local.set({ lastError: '' });
  try {
    tab = await api.tabs.get(cfg.tabId);
    const taught = cfg.useVisualProfile ? profileForTab(cfg, tab) : null;
    if (taught) effectiveProfile = taught;
    if (!effectiveProfile?.selectors) throw new Error('No usable page profile is available');
    if (!matches(tab.url || '', effectiveProfile.match_url || '')) throw new Error('The selected tab no longer matches its taught page');
    await sendHeartbeat('working');
    leaseTimer = setInterval(() => renewLease(cfg, work.job.id), 25000);
    const initial = await captureTabProgress(cfg.tabId, effectiveProfile.selectors);
    baselineText = initial.text || '';
    latestProgressText = baselineText;
    const sample = async () => {
      if (progressBusy) return;
      progressBusy = true;
      try {
        const snapshot = await captureTabProgress(cfg.tabId, effectiveProfile.selectors);
        const text = String(snapshot.text || '').trim();
        if (text && text !== baselineText && text !== latestProgressText) {
          latestProgressText = text;
          progressSequence += 1;
          await reportProgress(cfg, work.job.id, {
            sequence: progressSequence,
            text: text.slice(0, 1024 * 1024),
            phase: snapshot.busy ? 'generating' : 'stabilizing',
            busy: Boolean(snapshot.busy)
          });
        }
      } catch (_) {
      } finally {
        progressBusy = false;
      }
    };
    progressTimer = setInterval(sample, 800);
    const results = await api.scripting.executeScript({
      target: { tabId: cfg.tabId },
      func: automate,
      args: [work.job, effectiveProfile, work.deadline]
    });
    const answer = results?.[0]?.result;
    if (!answer?.ok) throw new Error(answer?.error || 'No browser response was captured');
    if (answer.text && answer.text !== latestProgressText) {
      progressSequence += 1;
      await reportProgress(cfg, work.job.id, {
        sequence: progressSequence,
        text: String(answer.text).slice(0, 1024 * 1024),
        phase: 'final',
        busy: false
      });
    }
    decision = parseOutput(answer.text, work.job.output || {}, effectiveProfile.label || 'browser', answer.artifacts || []);
  } catch (error) {
    const mode = outputMode(work.job.output || {});
    decision = mode === 'decision'
      ? { verdict: 'review', flags: ['browser_automation_error'], confidence: 0.4, model: effectiveProfile?.label || 'browser' }
      : { mode, error: 'browser_automation_error', model: effectiveProfile?.label || 'browser' };
    await api.storage.local.set({ lastError: error.message || String(error) });
  } finally {
    if (leaseTimer) clearInterval(leaseTimer);
    if (progressTimer) clearInterval(progressTimer);
  }

  const pendingCompletions = { ...cfg.pendingCompletions, [work.job.id]: decision };
  await api.storage.local.set({ pendingCompletions });
  await completeWork(await settings(), work.job.id, decision);
  await sendHeartbeat('waiting');
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
  const pageBusy = () => {
    for (const selector of [
      '[aria-busy="true"]', '[data-is-streaming="true"]', '.result-streaming',
      'button[data-testid*="stop" i]', 'button[aria-label*="stop" i]',
      'button[aria-label*="beenden" i]', 'button[aria-label*="abbrechen" i]'
    ]) {
      try {
        if ([...document.querySelectorAll(selector)].some(isVisible)) return true;
      } catch (_) {}
    }
    return false;
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
		const maxBytes = Math.max(1024, Math.min(Number(spec.max_artifact_bytes) || 6 * 1024 * 1024, 6 * 1024 * 1024));
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
		for (const candidate of candidates.slice(0, 4)) {
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
		for (const code of [...responseElement.querySelectorAll('pre code')].slice(0, Math.max(0, 4 - artifacts.length))) {
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
      const previousText = before.length ? visibleText(before[before.length - 1]) : '';
      if (job.image_base64) {
        const fileInput = first(selectors.file_input);
        if (!fileInput) throw new Error('This job has an image, but no image input was taught');
        addImage(fileInput, job.image_base64, job.image_media_type);
        await wait(1000);
      }
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

		const suppliedDeadline = Date.parse(jobDeadline || '');
		const defaultWait = job.output?.artifacts ? 300000 : 180000;
		const deadline = Number.isFinite(suppliedDeadline)
			? Math.min(Date.now() + defaultWait, suppliedDeadline - 1000)
			: Date.now() + defaultWait;
      let stableText = '';
      let stableSince = 0;
      let sawBusy = false;
      while (Date.now() < deadline) {
        await wait(650);
        const responses = all(selectors.response);
        const latestElement = responses.length ? responses[responses.length - 1] : null;
        const latest = visibleText(latestElement);
        const changedResponse = latestElement !== previousElement || responses.length > before.length || latest !== previousText;
        const busy = pageBusy();
        sawBusy = sawBusy || busy;
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
        if (Date.now() - stableSince >= stableFor && structured && !busy) {
		  const artifacts = await collectArtifacts(latestElement, job.output || {});
		  resolve({ ok: true, text: latest || `Generated ${artifacts.length} artifact(s).`, artifacts });
          return;
        }
      }
      throw new Error('Timed out while waiting for a stable response');
    } catch (error) {
      resolve({ ok: false, error: error.message || String(error) });
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
  for (const selector of [
    '[aria-busy="true"]', '[data-is-streaming="true"]', '.result-streaming',
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
  return { text: responses.length ? visibleText(responses[responses.length - 1]) : '', busy };
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
  let tab = null;
  try {
    if (cfg.tabId) tab = await api.tabs.get(cfg.tabId);
  } catch (_) {}
  const profile = tab ? profileForTab(cfg, tab) : null;
  try {
    await fetch(`${cfg.bridgeUrl}/v1/browser/heartbeat`, {
      method: 'POST',
      headers: { Authorization: `Bearer ${cfg.token}`, 'Content-Type': 'application/json' },
      body: JSON.stringify({
        state,
        origin: tab?.url && /^https?:/i.test(tab.url) ? new URL(tab.url).origin : '',
        tab_title: tab?.title || '',
        profile_label: profile?.label || cfg.profile || '',
        selectors_ready: cfg.useVisualProfile ? Boolean(profile) : Boolean(cfg.profile),
        extension_version: api.runtime.getManifest().version,
        browser: navigator.userAgent.includes('Firefox/') ? 'firefox' : 'chromium'
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
    running: false,
    useVisualProfile: true,
    taughtProfiles: {},
    pendingCompletions: {},
    lastError: ''
  });
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
