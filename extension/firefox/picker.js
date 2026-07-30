(() => {
  const api = globalThis.browser || globalThis.chrome;
  const rootKey = '__contextBridgeVisualPicker';
  if (globalThis[rootKey]) return;

  const roles = ['input', 'submit', 'response', 'file_input'];
  const labels = {
    input: ['Prompt input', 'Click the field where prompts are entered.'],
    submit: ['Send action', 'Click the button that sends a prompt. You can skip this when Enter sends.'],
    response: ['Response area', 'Click one complete answer from the assistant.'],
    file_input: ['Image upload', 'Click the upload control, or skip it when this page is text only.']
  };
  let state = null;

  api.runtime.onMessage.addListener((message, _sender, sendResponse) => {
    if (message?.type !== 'contextbridge-picker-start') return false;
    start(message.existing || null);
    sendResponse({ ok: true });
    return true;
  });

  function start(existing) {
    cleanup();
    state = {
      index: 0,
      selectors: existing?.selectors ? cloneSelectors(existing.selectors) : emptySelectors(),
      selected: {},
      host: null,
      shadow: null,
      outline: null,
      current: null
    };
    buildInterface();
    document.addEventListener('pointermove', onPointerMove, true);
    document.addEventListener('click', onClick, true);
    document.addEventListener('keydown', onKeyDown, true);
    render();
  }

  function buildInterface() {
    const host = document.createElement('div');
    host.id = 'contextbridge-visual-picker';
    host.style.cssText = 'all:initial;position:fixed;inset:0;z-index:2147483647;pointer-events:none';
    const shadow = host.attachShadow({ mode: 'closed' });
    shadow.innerHTML = `
      <style>
        :host{all:initial}
        *{box-sizing:border-box}
        .bar{pointer-events:auto;position:fixed;top:14px;left:50%;width:min(680px,calc(100vw - 28px));transform:translateX(-50%);display:grid;grid-template-columns:auto minmax(0,1fr) auto;align-items:center;gap:14px;padding:12px 14px;border:1px solid #20231f;border-radius:6px;background:#f7f7f1;color:#20231f;box-shadow:0 14px 42px rgba(20,24,20,.22);font:13px/1.35 ui-monospace,SFMono-Regular,Consolas,monospace}
        .mark{display:grid;place-items:center;width:34px;height:34px;border:1px solid #20231f;border-radius:4px;font-weight:800;color:#138f83}
        .copy{min-width:0}.eyebrow{margin:0 0 2px;color:#667069;font-size:10px;text-transform:uppercase}.title{margin:0;font-size:14px}.help{margin:2px 0 0;color:#5d655e;font-size:11px}
        .actions{display:flex;gap:6px}.button{min-height:34px;padding:7px 10px;border:1px solid #9ea49d;border-radius:4px;background:#fff;color:#20231f;font:11px/1 ui-monospace,SFMono-Regular,Consolas,monospace;cursor:pointer}.button:hover{border-color:#20231f}.button.primary{border-color:#20231f;background:#20231f;color:#fff}.button[hidden]{display:none}
        .progress{position:fixed;top:0;left:0;height:3px;background:#2ec4b6;transition:width .25s ease}
        .outline{position:fixed;border:2px solid #2ec4b6;background:rgba(46,196,182,.10);box-shadow:0 0 0 3px rgba(247,247,241,.85);border-radius:3px;pointer-events:none;transition:top 70ms ease,left 70ms ease,width 70ms ease,height 70ms ease}
        .tag{position:fixed;max-width:260px;padding:5px 7px;border-radius:3px;background:#20231f;color:#fff;font:10px/1.25 ui-monospace,SFMono-Regular,Consolas,monospace;pointer-events:none;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
        @media(max-width:620px){.bar{grid-template-columns:auto 1fr}.actions{grid-column:1/-1;justify-content:flex-end}.help{display:none}}
        @media(prefers-color-scheme:dark){.bar{border-color:#e7e9e2;background:#222722;color:#f3f4ee}.mark{border-color:#e7e9e2}.help,.eyebrow{color:#adb5ad}.button{border-color:#626a62;background:#2c322c;color:#f3f4ee}.button.primary{border-color:#f3f4ee;background:#f3f4ee;color:#20231f}}
        @media(prefers-reduced-motion:reduce){.outline,.progress{transition:none}}
      </style>
      <div class="progress"></div>
      <section class="bar" role="dialog" aria-label="ContextBridge visual teaching">
        <div class="mark">CB</div>
        <div class="copy"><p class="eyebrow"></p><h2 class="title"></h2><p class="help"></p></div>
        <div class="actions">
          <button class="button back" type="button">Back</button>
          <button class="button skip" type="button">Skip</button>
          <button class="button cancel" type="button">Cancel</button>
        </div>
      </section>
      <div class="outline" hidden></div><div class="tag" hidden></div>`;
    document.documentElement.append(host);
    state.host = host;
    state.shadow = shadow;
    state.outline = shadow.querySelector('.outline');
    shadow.querySelector('.back').addEventListener('click', back);
    shadow.querySelector('.skip').addEventListener('click', skip);
    shadow.querySelector('.cancel').addEventListener('click', cancel);
  }

  function render() {
    if (!state) return;
    const role = roles[state.index];
    const [title, help] = labels[role];
    state.shadow.querySelector('.eyebrow').textContent = `Teach this page  ${state.index + 1} / ${roles.length}`;
    state.shadow.querySelector('.title').textContent = title;
    state.shadow.querySelector('.help').textContent = help;
    state.shadow.querySelector('.progress').style.width = `${((state.index + 1) / roles.length) * 100}%`;
    state.shadow.querySelector('.back').hidden = state.index === 0;
    state.shadow.querySelector('.skip').hidden = role === 'input' || role === 'response';
    clearOutline();
  }

  function onPointerMove(event) {
    if (!state || isPickerNode(event)) return;
    const role = roles[state.index];
    const candidate = normalizeTarget(event.composedPath?.()[0] || event.target, role);
    state.current = candidate;
    if (!candidate) return clearOutline();
    showOutline(candidate, role);
  }

  function onClick(event) {
    if (!state || isPickerNode(event)) return;
    event.preventDefault();
    event.stopImmediatePropagation();
    const role = roles[state.index];
    const target = normalizeTarget(event.composedPath?.()[0] || event.target, role);
    if (!target) {
      flash('Choose a matching page control');
      return;
    }
    const selectors = buildSelectors(target, role);
    if (!selectors.length) {
      flash('A stable selector could not be created for that element');
      return;
    }
    state.selectors[role] = selectors;
    state.selected[role] = target;
    next();
  }

  function onKeyDown(event) {
    if (!state) return;
    if (event.key === 'Escape') {
      event.preventDefault();
      cancel();
    }
  }

  function next() {
    if (state.index >= roles.length - 1) return finish();
    state.index += 1;
    render();
  }

  function back() {
    if (!state || state.index === 0) return;
    state.index -= 1;
    render();
  }

  function skip() {
    if (!state) return;
    state.selectors[roles[state.index]] = [];
    next();
  }

  async function finish() {
    const profile = {
      label: document.title || location.hostname,
      selectors: state.selectors
    };
    const snapshot = state;
    showSuccess(snapshot);
    removeListeners();
    state = null;
    try {
      await api.runtime.sendMessage({ type: 'picker-complete', profile });
    } catch (_) {}
    setTimeout(() => snapshot.host?.remove(), 1400);
  }

  async function cancel() {
    cleanup();
    try {
      await api.runtime.sendMessage({ type: 'picker-cancelled' });
    } catch (_) {}
  }

  function cleanup() {
    removeListeners();
    state?.host?.remove();
    state = null;
  }

  function removeListeners() {
    document.removeEventListener('pointermove', onPointerMove, true);
    document.removeEventListener('click', onClick, true);
    document.removeEventListener('keydown', onKeyDown, true);
  }

  function isPickerNode(event) {
    return event.composedPath?.().includes(state?.host);
  }

  function normalizeTarget(node, role) {
    if (!(node instanceof Element)) return null;
    if (role === 'input') {
      return node.closest('textarea,input:not([type]),input[type="text"],input[type="search"],[contenteditable="true"],[role="textbox"]')
        || node.querySelector?.('textarea,input:not([type]),input[type="text"],input[type="search"],[contenteditable="true"],[role="textbox"]');
    }
    if (role === 'submit') {
      return node.closest('button,[role="button"],input[type="submit"]')
        || node.querySelector?.('button,[role="button"],input[type="submit"]');
    }
    if (role === 'file_input') return findFileInput(node);
    return responseContainer(node);
  }

  function findFileInput(node) {
    if (node.matches('input[type="file"]')) return node;
    const label = node.closest('label');
    if (label) {
      const linked = label.htmlFor ? document.getElementById(label.htmlFor) : label.querySelector('input[type="file"]');
      if (linked?.matches('input[type="file"]')) return linked;
    }
    let container = node.parentElement;
    for (let depth = 0; container && depth < 7; depth += 1, container = container.parentElement) {
      const local = container.querySelector('input[type="file"]');
      if (local) return local;
    }
    const all = document.querySelectorAll('input[type="file"]');
    return all.length === 1 ? all[0] : null;
  }

  function responseContainer(node) {
    let current = node;
    for (let depth = 0; current && depth < 7; depth += 1, current = current.parentElement) {
      if (current.matches('[data-message-author-role="assistant"],[data-testid*="assistant"],[data-role="assistant"],[role="article"],article')) return current;
    }
    return node.closest('article,[role="article"],li,section,div') || node;
  }

  function buildSelectors(element, role) {
    const candidates = [];
    const tag = element.tagName.toLowerCase();
    const add = (selector, requireUnique = false) => {
      if (!selector || candidates.includes(selector)) return;
      try {
        const matches = [...document.querySelectorAll(selector)];
        if (!matches.includes(element)) return;
        if (requireUnique && matches.length !== 1) return;
        candidates.push(selector);
      } catch (_) {}
    };

    if (element.id && stableValue(element.id)) add(`#${cssEscape(element.id)}`, true);
    for (const attr of ['data-message-author-role', 'data-testid', 'data-qa', 'data-role', 'name', 'aria-label', 'role', 'type']) {
      const value = element.getAttribute(attr);
      if (!value || !stableValue(value)) continue;
      const selector = `${tag}[${attr}="${cssString(value)}"]`;
      add(selector, role !== 'response' && attr !== 'data-message-author-role');
      add(`[${attr}="${cssString(value)}"]`, role !== 'response' && attr !== 'data-message-author-role');
    }
    if (element.isContentEditable) add(`${tag}[contenteditable="true"]`, true);

    const classes = [...element.classList].filter(stableClass).slice(0, 3);
    if (classes.length) add(`${tag}.${classes.map(cssEscape).join('.')}`, role !== 'response');

    let parent = element.parentElement;
    for (let depth = 0; parent && depth < 4; depth += 1, parent = parent.parentElement) {
      const parentAnchor = stableAnchor(parent);
      if (!parentAnchor) continue;
      add(`${parentAnchor} > ${tag}:nth-of-type(${nthOfType(element)})`, true);
      add(`${parentAnchor} ${tag}`, role !== 'response');
      break;
    }
    add(absoluteSelector(element), true);
    return candidates.slice(0, 8);
  }

  function stableAnchor(element) {
    if (element.id && stableValue(element.id)) return `#${cssEscape(element.id)}`;
    for (const attr of ['data-testid', 'data-qa', 'data-role', 'aria-label', 'role']) {
      const value = element.getAttribute(attr);
      if (value && stableValue(value)) return `${element.tagName.toLowerCase()}[${attr}="${cssString(value)}"]`;
    }
    return '';
  }

  function absoluteSelector(element) {
    const parts = [];
    let current = element;
    while (current && current !== document.body && parts.length < 7) {
      if (current.id && stableValue(current.id)) {
        parts.unshift(`#${cssEscape(current.id)}`);
        break;
      }
      parts.unshift(`${current.tagName.toLowerCase()}:nth-of-type(${nthOfType(current)})`);
      current = current.parentElement;
    }
    return parts.join(' > ');
  }

  function nthOfType(element) {
    let index = 1;
    let sibling = element.previousElementSibling;
    while (sibling) {
      if (sibling.tagName === element.tagName) index += 1;
      sibling = sibling.previousElementSibling;
    }
    return index;
  }

  function stableValue(value) {
    const clean = String(value).trim();
    return clean.length > 0 && clean.length <= 100 && !/[a-f0-9]{16,}/i.test(clean) && !/\d{8,}/.test(clean);
  }

  function stableClass(value) {
    return stableValue(value) && !/^(css|jsx|sc|_[a-z])[-_]/i.test(value) && value.length <= 50;
  }

  function cssEscape(value) {
    return globalThis.CSS?.escape ? CSS.escape(value) : String(value).replace(/[^a-z0-9_-]/gi, (char) => `\\${char}`);
  }

  function cssString(value) {
    return String(value).replace(/\\/g, '\\\\').replace(/"/g, '\\"');
  }

  function showOutline(element, role) {
    const rect = element.getBoundingClientRect();
    const outline = state.outline;
    const tag = state.shadow.querySelector('.tag');
    outline.hidden = false;
    outline.style.top = `${Math.max(0, rect.top)}px`;
    outline.style.left = `${Math.max(0, rect.left)}px`;
    outline.style.width = `${Math.max(1, rect.width)}px`;
    outline.style.height = `${Math.max(1, rect.height)}px`;
    tag.hidden = false;
    tag.textContent = `${labels[role][0]}  ${element.tagName.toLowerCase()}`;
    tag.style.top = `${Math.max(5, Math.min(innerHeight - 30, rect.bottom + 7))}px`;
    tag.style.left = `${Math.max(5, Math.min(innerWidth - 265, rect.left))}px`;
  }

  function clearOutline() {
    if (!state) return;
    state.outline.hidden = true;
    state.shadow.querySelector('.tag').hidden = true;
  }

  function flash(text) {
    if (!state) return;
    const help = state.shadow.querySelector('.help');
    const original = labels[roles[state.index]][1];
    help.textContent = text;
    help.style.color = '#d7422f';
    setTimeout(() => {
      if (!state) return;
      help.textContent = original;
      help.style.color = '';
    }, 1400);
  }

  function showSuccess(snapshot) {
    snapshot.shadow.querySelector('.eyebrow').textContent = 'Page taught';
    snapshot.shadow.querySelector('.title').textContent = 'The local browser profile is ready.';
    snapshot.shadow.querySelector('.help').textContent = 'Open ContextBridge and start the connection.';
    snapshot.shadow.querySelector('.actions').remove();
    snapshot.shadow.querySelector('.progress').style.width = '100%';
    snapshot.shadow.querySelector('.progress').style.background = '#2ec4b6';
    snapshot.outline.hidden = true;
    snapshot.shadow.querySelector('.tag').hidden = true;
  }

  function emptySelectors() {
    return { input: [], submit: [], response: [], file_input: [] };
  }

  function cloneSelectors(selectors) {
    return {
      input: [...(selectors.input || [])],
      submit: [...(selectors.submit || [])],
      response: [...(selectors.response || [])],
      file_input: [...(selectors.file_input || [])]
    };
  }

  globalThis[rootKey] = { start, cleanup };
})();
