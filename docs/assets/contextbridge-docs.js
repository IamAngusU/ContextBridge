(() => {
  'use strict';

  const $ = (selector, root = document) => root.querySelector(selector);
  const $$ = (selector, root = document) => [...root.querySelectorAll(selector)];
  const html = document.documentElement;
  const reduceMotion = matchMedia('(prefers-reduced-motion: reduce)');
  const systemDark = matchMedia('(prefers-color-scheme: dark)');

  const toastStack = $('#toast-stack');

  function escapeHTML(value) {
    return String(value ?? '').replace(/[&<>"']/g, char => ({
      '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;'
    }[char]));
  }

  function toast(message) {
    const node = document.createElement('div');
    node.className = 'toast';
    node.textContent = message;
    toastStack.append(node);
    window.setTimeout(() => {
      node.classList.add('is-leaving');
      window.setTimeout(() => node.remove(), 220);
    }, 2200);
  }

  async function copyText(value, label = 'Copied') {
    try {
      await navigator.clipboard.writeText(value);
      toast(label);
    } catch (_) {
      const area = document.createElement('textarea');
      area.value = value;
      area.setAttribute('readonly', '');
      area.style.position = 'fixed';
      area.style.opacity = '0';
      document.body.append(area);
      area.select();
      document.execCommand('copy');
      area.remove();
      toast(label);
    }
  }

  // Theme ---------------------------------------------------------------
  function savedTheme() {
    try {
      const value = localStorage.getItem('contextbridge-docs-theme');
      return ['system', 'light', 'dark'].includes(value) ? value : 'system';
    } catch (_) {
      return 'system';
    }
  }

  function applyTheme(preference, announce = false) {
    const pref = ['system', 'light', 'dark'].includes(preference) ? preference : 'system';
    const resolved = pref === 'system' ? (systemDark.matches ? 'dark' : 'light') : pref;
    html.dataset.theme = resolved;
    html.dataset.themePreference = pref;
    $$('.theme-options [data-theme-choice]').forEach(button => {
      button.setAttribute('aria-checked', String(button.dataset.themeChoice === pref));
    });
    try { localStorage.setItem('contextbridge-docs-theme', pref); } catch (_) {}
    if (announce) toast(`Appearance · ${pref}`);
  }

  applyTheme(savedTheme());
  systemDark.addEventListener?.('change', () => {
    if (html.dataset.themePreference === 'system') applyTheme('system');
  });

  // Popovers ------------------------------------------------------------
  const popovers = $$('.popover');
  const popoverAnchors = new Map();

  function positionPopover(popover, anchor, options = {}) {
    if (!popover || !anchor) return;
    popover.style.left = '-9999px';
    popover.style.top = '-9999px';
    popover.hidden = false;
    popover.classList.add('is-open');

    const anchorRect = anchor.getBoundingClientRect();
    const popRect = popover.getBoundingClientRect();
    const gap = options.gap ?? 8;
    let left = options.center
      ? anchorRect.left + (anchorRect.width - popRect.width) / 2
      : anchorRect.right - popRect.width;
    left = Math.max(12, Math.min(window.innerWidth - popRect.width - 12, left));

    let top = anchorRect.bottom + gap;
    if (top + popRect.height > window.innerHeight - 12) {
      top = Math.max(12, anchorRect.top - popRect.height - gap);
    }

    popover.style.left = `${Math.round(left)}px`;
    popover.style.top = `${Math.round(top)}px`;
  }

  function closePopover(popover) {
    if (!popover?.classList.contains('is-open')) return;
    popover.classList.remove('is-open');
    const anchor = popoverAnchors.get(popover);
    anchor?.setAttribute('aria-expanded', 'false');
    window.setTimeout(() => {
      if (!popover.classList.contains('is-open')) popover.hidden = true;
    }, reduceMotion.matches ? 0 : 210);
  }

  function closePopovers(except = null) {
    popovers.forEach(popover => {
      if (popover !== except) closePopover(popover);
    });
  }

  function togglePopover(popover, anchor, options = {}) {
    if (!popover || !anchor) return;
    const willOpen = !popover.classList.contains('is-open');
    closePopovers(willOpen ? popover : null);
    if (!willOpen) {
      closePopover(popover);
      return;
    }
    popoverAnchors.set(popover, anchor);
    positionPopover(popover, anchor, options);
    anchor.setAttribute('aria-expanded', 'true');
    if (options.focus) window.setTimeout(() => options.focus.focus(), 20);
  }

  const themeButton = $('#theme-button');
  const themePopover = $('#theme-popover');
  themeButton?.addEventListener('click', event => {
    event.stopPropagation();
    togglePopover(themePopover, themeButton);
  });

  $$('.theme-options [data-theme-choice]').forEach(button => {
    button.addEventListener('click', () => {
      applyTheme(button.dataset.themeChoice, true);
      positionPopover(themePopover, themeButton);
    });
  });

  const tokenButton = $('#token-info-button');
  const tokenPopover = $('#token-popover');
  tokenButton?.addEventListener('click', event => {
    event.stopPropagation();
    togglePopover(tokenPopover, tokenButton);
  });

  const mobileNavButton = $('#mobile-nav-button');
  const mobileNavPopover = $('#mobile-nav-popover');
  mobileNavButton?.addEventListener('click', event => {
    event.stopPropagation();
    togglePopover(mobileNavPopover, mobileNavButton, { center: true });
  });
  $$('#mobile-nav-popover a').forEach(link => link.addEventListener('click', () => closePopovers()));

  document.addEventListener('pointerdown', event => {
    const path = event.composedPath();
    const insidePopover = path.some(node => node?.classList?.contains?.('popover'));
    const onAnchor = [...popoverAnchors.values()].some(anchor => path.includes(anchor));
    if (!insidePopover && !onAnchor) closePopovers();
  });
  window.addEventListener('resize', () => closePopovers());
  document.addEventListener('keydown', event => {
    if (event.key === 'Escape') closePopovers();
  });

  // Tabs ----------------------------------------------------------------
  function setupTabs(buttonSelector, panelSelector, dataKey) {
    const buttons = $$(buttonSelector);
    const panels = $$(panelSelector);
    buttons.forEach(button => {
      button.addEventListener('click', () => {
        const value = button.dataset[dataKey];
        buttons.forEach(item => {
          const active = item === button;
          item.classList.toggle('is-active', active);
          item.setAttribute('aria-selected', String(active));
        });
        panels.forEach(panel => {
          const active = panel.dataset[dataKey.replace('Tab', 'Panel')] === value;
          panel.classList.toggle('is-active', active);
          panel.hidden = !active;
        });
      });
    });
  }

  setupTabs('[data-install-tab]', '[data-install-panel]', 'installTab');
  setupTabs('[data-job-tab]', '[data-job-panel]', 'jobTab');

  // Syntax highlighting -------------------------------------------------
  const tokenPatterns = {
    powershell: /(#.*$)|("(?:\\.|[^"\\])*"|'(?:[^']|'')*')|(\-\-[\w-]+|\-[A-Za-z]\b)|(\||@'|'@)|\b(irm|iex|contextbridge|cb)\b/gm,
    bash: /(#.*$)|("(?:\\.|[^"\\])*"|'[^']*')|(\-\-[\w-]+|\-[A-Za-z]\b)|(\||<<|>>)|\b(curl|sh|contextbridge|cb)\b/gm
  };

  function highlightShell(raw, language) {
    const pattern = tokenPatterns[language] || tokenPatterns.bash;
    let output = '';
    let lastIndex = 0;
    for (const match of raw.matchAll(pattern)) {
      const index = match.index ?? 0;
      output += escapeHTML(raw.slice(lastIndex, index));
      const token = match[0];
      let className = 'tok-command';
      if (match[1]) className = 'tok-comment';
      else if (match[2]) className = 'tok-string';
      else if (match[3]) className = 'tok-flag';
      else if (match[4]) className = 'tok-pipe';
      output += `<span class="${className}">${escapeHTML(token)}</span>`;
      lastIndex = index + token.length;
    }
    output += escapeHTML(raw.slice(lastIndex));
    return output;
  }

  $$('.code-block').forEach(block => {
    const code = $('pre code', block);
    if (!code) return;
    const raw = code.textContent.replace(/^\n+|\n+$/g, '');
    block.dataset.rawCode = raw;
    code.innerHTML = highlightShell(raw, block.dataset.language || 'bash');
    $('.copy-button', block)?.addEventListener('click', () => copyText(raw, 'Command copied'));
  });

  $$('[data-copy-command]').forEach(button => {
    button.addEventListener('click', () => copyText(button.dataset.copyCommand, `${button.dataset.copyCommand} copied`));
  });

  // Interactive docs terminal ------------------------------------------
  const terminalOutput = $('#terminal-output');
  const terminalInput = $('#terminal-input');
  const terminalForm = $('#terminal-form');

  const terminalHelp = [
    ['help', 'show this command list'],
    ['install', 'installer commands and first choices'],
    ['token', 'where the local pairing token comes from'],
    ['version', 'check the installed runtime version'],
    ['doctor', 'explain the real setup diagnostic'],
    ['status', 'explain the one-time service snapshot'],
    ['console', 'read-only live view of the running service'],
    ['dashboard', 'open the local graphical view'],
    ['details 1', 'toggle node 1 GPU and model details'],
    ['gpus 1', 'toggle only node 1 GPU details'],
    ['models 1', 'toggle only node 1 model details'],
    ['first-job', 'jump to the first BRIDGE-OK job'],
    ['clear', 'clear this docs terminal'],
    ['exit', 'explain what exit does in the real console']
  ];

  function terminalHelpHTML() {
    return `<div class="term-help-grid">${terminalHelp.map(([command, description]) => `<code>${escapeHTML(command)}</code><span>${escapeHTML(description)}</span>`).join('')}</div>`;
  }

  const terminalResponses = {
    help: () => terminalHelpHTML(),
    install: () => '<span class="info">Windows</span>  irm https://angusu.de/contextbridge/install.ps1 | iex\n<span class="info">Linux/macOS</span>  curl -fsSL https://angusu.de/contextbridge/install.sh | sh\n\nFirst browser setup: target <strong>3</strong>, device mode <strong>1</strong>.',
    token: () => '<span class="ok">local pairing secret</span>\nWindows: copied to the clipboard by the installer during browser setup.\nLinux/macOS: read it from the private config.yml path printed by the installer.\n\nIt is not a provider password. Keep it out of screenshots, issues, videos, and logs.',
    version: () => 'Run <strong>contextbridge version</strong> in your real shell to see the installed release.',
    doctor: () => '<span class="ok">doctor</span> validates config, local service, token, default route, relay, and worker identity.\nThe real command prints a concrete fix for each blocking check.\n\n$ contextbridge doctor',
    status: () => '<span class="ok">status</span> is a one-time snapshot of service, browser, routes, and resources.\n\n$ contextbridge status',
    console: () => '<span class="ok">console</span> attaches read-only to the already running service. Closing it does not stop jobs.\n\n$ contextbridge console\n$ cb console',
    dashboard: () => 'Open the local graphical operator view:\n\n$ <strong>contextbridge dashboard</strong>',
    'details 1': () => 'In the real live console, <strong>details 1</strong> toggles both GPU and model details for node 1.',
    'gpus 1': () => 'In the real live console, <strong>gpus 1</strong> toggles GPU details for node 1.',
    'models 1': () => 'In the real live console, <strong>models 1</strong> toggles model details for node 1.',
    'first-job': () => 'Jumping to the first structured submit example. Expected marker: <span class="ok">BRIDGE-OK</span>.',
    exit: () => 'In the real <strong>contextbridge console</strong>, exit closes only the read-only view. The managed service keeps running.\nUse <strong>contextbridge stop</strong> when you actually intend to stop the local service.',
    clear: () => ''
  };

  function normalizeTerminalCommand(input) {
    let command = input.trim().replace(/\s+/g, ' ').toLowerCase();
    command = command.replace(/^(contextbridge|cb)\s+/, '');
    return command;
  }

  function appendTerminalCommand(input, responseHTML) {
    const block = document.createElement('div');
    block.className = 'term-block';
    block.innerHTML = `<div class="term-command"><b>docs ›</b><span>${escapeHTML(input)}</span></div>${responseHTML ? `<div class="term-response">${responseHTML}</div>` : ''}`;
    terminalOutput.append(block);
    terminalOutput.scrollTop = terminalOutput.scrollHeight;
  }

  function runTerminalCommand(input, echo = true) {
    const raw = input.trim();
    if (!raw) return;
    const command = normalizeTerminalCommand(raw);

    if (command === 'clear') {
      terminalOutput.replaceChildren();
      return;
    }

    if (command === 'first-job') {
      if (echo) appendTerminalCommand(raw, terminalResponses['first-job']());
      $('#first-job')?.scrollIntoView({ behavior: reduceMotion.matches ? 'auto' : 'smooth', block: 'start' });
      return;
    }

    const response = terminalResponses[command];
    if (response) {
      if (echo) appendTerminalCommand(raw, response());
    } else {
      appendTerminalCommand(raw, `<span class="warn">unknown docs command</span>\nType <strong>help</strong> for the available documentation commands.`);
    }
  }

  function resetTerminal() {
    terminalOutput.replaceChildren();
    const intro = document.createElement('div');
    intro.className = 'term-block';
    intro.innerHTML = '<div class="term-response"><span class="ok">ContextBridge docs terminal</span>\nInteractive help only. Nothing on your computer is executed.\nType <strong>help</strong> or choose a command below.</div>';
    terminalOutput.append(intro);
  }

  resetTerminal();

  terminalForm?.addEventListener('submit', event => {
    event.preventDefault();
    const value = terminalInput.value;
    terminalInput.value = '';
    runTerminalCommand(value);
  });

  $('#terminal-reset')?.addEventListener('click', () => {
    resetTerminal();
    terminalInput.focus();
  });

  $$('[data-terminal-command]').forEach(button => {
    button.addEventListener('click', () => {
      const command = button.dataset.terminalCommand;
      runTerminalCommand(command);
      terminalInput.focus();
    });
  });

  // Search --------------------------------------------------------------
  const searchButton = $('#search-button');
  const searchPopover = $('#search-popover');
  const searchInput = $('#search-input');
  const searchResults = $('#search-results');

  const searchIndex = [
    { title: 'Start here', detail: 'local runtime · route overview', target: '#start', terms: 'start getting started local runtime 127 loopback' },
    { title: 'Installation · Windows', detail: 'PowerShell installer · target 3 · device 1', target: '#install', terms: 'install windows powershell irm installer localappdata' },
    { title: 'Installation · Linux / macOS', detail: 'shell installer · ~/.local/bin', target: '#install', terms: 'install linux macos bash zsh curl shell' },
    { title: 'Pairing token', detail: 'clipboard on Windows · private config.yml elsewhere', target: '#pair', terms: 'token pairing secret clipboard config yml extension browser' },
    { title: 'Load browser extension', detail: 'developer mode · Load unpacked', target: '#pair', terms: 'chromium chrome edge opera extension load unpacked' },
    { title: 'contextbridge doctor', detail: 'actionable setup checks', target: '#commands', terminal: 'doctor', terms: 'doctor diagnostics checks config service route worker token' },
    { title: 'contextbridge status', detail: 'one-time service snapshot', target: '#commands', terminal: 'status', terms: 'status snapshot service browser resources' },
    { title: 'contextbridge console', detail: 'read-only live view', target: '#terminal', terminal: 'console', terms: 'console terminal live help cb read only' },
    { title: 'Console help', detail: 'details · gpus · models · exit · clear', target: '#terminal', terminal: 'help', terms: 'help details gpus models clear exit console commands' },
    { title: 'First BRIDGE-OK job', detail: 'structured JSON submit', target: '#first-job', terms: 'first job submit json bridge ok generation prompt file' }
  ];

  function renderSearch(query = '') {
    const needle = query.trim().toLowerCase();
    const matches = searchIndex.filter(item => !needle || `${item.title} ${item.detail} ${item.terms}`.toLowerCase().includes(needle)).slice(0, 8);
    searchResults.replaceChildren(...matches.map((item, index) => {
      const button = document.createElement('button');
      button.type = 'button';
      button.className = `search-result${index === 0 ? ' is-selected' : ''}`;
      button.innerHTML = `<span><strong>${escapeHTML(item.title)}</strong><small>${escapeHTML(item.detail)}</small></span><kbd>↵</kbd>`;
      button.addEventListener('click', () => {
        closePopovers();
        $(item.target)?.scrollIntoView({ behavior: reduceMotion.matches ? 'auto' : 'smooth', block: 'start' });
        if (item.terminal) window.setTimeout(() => runTerminalCommand(item.terminal), 420);
      });
      return button;
    }));
    if (!matches.length) {
      const empty = document.createElement('div');
      empty.style.padding = '18px 10px';
      empty.style.color = 'var(--muted)';
      empty.style.fontSize = '.72rem';
      empty.textContent = 'No match. Try install, token, doctor, console, or first job.';
      searchResults.append(empty);
    }
  }

  function openSearch() {
    renderSearch(searchInput.value);
    togglePopover(searchPopover, searchButton, { focus: searchInput });
  }

  searchButton?.addEventListener('click', event => {
    event.stopPropagation();
    openSearch();
  });
  searchInput?.addEventListener('input', () => renderSearch(searchInput.value));
  searchInput?.addEventListener('keydown', event => {
    if (event.key === 'Enter') {
      const first = $('.search-result', searchResults);
      if (first) { event.preventDefault(); first.click(); }
    }
  });

  document.addEventListener('keydown', event => {
    const target = event.target;
    const typing = target instanceof HTMLInputElement || target instanceof HTMLTextAreaElement || target?.isContentEditable;
    if (event.key === '/' && !typing) {
      event.preventDefault();
      openSearch();
    }
  });

  // Navigation state ----------------------------------------------------
  const navLinks = new Map($$('.side-nav [data-nav]').map(link => [link.dataset.nav, link]));
  const sections = $$('[data-section]');
  const observer = new IntersectionObserver(entries => {
    const visible = entries
      .filter(entry => entry.isIntersecting)
      .sort((a, b) => Math.abs(a.boundingClientRect.top) - Math.abs(b.boundingClientRect.top));
    if (!visible.length) return;
    const key = visible[0].target.dataset.section;
    navLinks.forEach((link, name) => link.classList.toggle('is-active', name === key));
  }, { rootMargin: '-22% 0px -60% 0px', threshold: [0, .1, .3] });
  sections.forEach(section => observer.observe(section));

  // Keep popover placement fresh while scrolling without leaving stale UI.
  let scrollTimer = 0;
  window.addEventListener('scroll', () => {
    window.clearTimeout(scrollTimer);
    scrollTimer = window.setTimeout(() => closePopovers(), 90);
  }, { passive: true });
})();
