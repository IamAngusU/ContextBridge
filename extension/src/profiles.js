(() => {
  const profiles = [
    {
      name: 'chatgpt',
      label: 'ChatGPT (auto-detected)',
      hosts: ['chatgpt.com', 'chat.openai.com'],
      match_url: 'https://chatgpt.com/*',
      selectors: {
        input: [
          '#prompt-textarea',
          'div[contenteditable="true"][aria-label*="ChatGPT" i]',
          'div[contenteditable="true"][data-virtualkeyboard]'
        ],
        submit: [
          'button[data-testid="send-button"]',
          '#composer-submit-button',
          'button[aria-label*="send" i]',
          'button[aria-label*="senden" i]'
        ],
        response: [
          '[data-message-author-role="assistant"]',
          'article[data-testid^="conversation-turn-"] .markdown'
        ],
        file_input: ['input[type="file"]']
      }
    },
    {
      name: 'gemini',
      label: 'Gemini (auto-detected)',
      hosts: ['gemini.google.com'],
      match_url: 'https://gemini.google.com/*',
      selectors: {
        input: [
          'rich-textarea div[contenteditable="true"][role="textbox"]',
          'div[contenteditable="true"][aria-label*="Prompt für Gemini" i]',
          'div[contenteditable="true"][aria-label*="prompt for Gemini" i]',
          'div.ql-editor[contenteditable="true"][role="textbox"]'
        ],
        submit: [
          'button[data-test-id="send-button"]',
          'button[data-testid="send-button"]',
          'button[aria-label*="prompt senden" i]',
          'button[aria-label*="send message" i]',
          'button[aria-label="Send"]'
        ],
        response: [
          'model-response message-content .markdown[aria-live="polite"]',
          'model-response .model-response-text message-content .markdown',
          'model-response .model-response-text'
        ],
        file_input: ['input[type="file"]']
      }
    }
  ];

  function forURL(value) {
    try {
      const url = new URL(value);
      const selected = profiles.find((profile) => profile.hosts.includes(url.hostname.toLowerCase()));
      if (!selected) return null;
      const matchURL = selected.name === 'chatgpt' ? `${url.origin}/*` : selected.match_url;
      return {
        name: selected.name,
        label: selected.label,
        origin: url.origin,
        match_url: matchURL,
        selectors: Object.fromEntries(Object.entries(selected.selectors).map(([key, values]) => [key, [...values]])),
        source: 'builtin'
      };
    } catch (_) {
      return null;
    }
  }

  globalThis.ContextBridgeProfiles = Object.freeze({
    forURL,
    all: () => profiles.map((profile) => ({ ...profile, hosts: [...profile.hosts] }))
  });
})();
