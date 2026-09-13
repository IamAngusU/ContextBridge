import assert from 'node:assert/strict';
import fs from 'node:fs';
import vm from 'node:vm';

const source = fs.readFileSync(new URL('../src/profiles.js', import.meta.url), 'utf8');
const context = vm.createContext({ URL });
vm.runInContext(source, context);

const chatgpt = context.ContextBridgeProfiles.forURL('https://chatgpt.com/c/example');
assert.equal(chatgpt.name, 'chatgpt');
assert.ok(chatgpt.selectors.input.includes('#prompt-textarea'));
assert.ok(chatgpt.selectors.response.includes('[data-message-author-role="assistant"]'));

const legacyChatGPT = context.ContextBridgeProfiles.forURL('https://chat.openai.com/c/example');
assert.equal(legacyChatGPT.name, 'chatgpt');
assert.equal(legacyChatGPT.match_url, 'https://chat.openai.com/*');

const gemini = context.ContextBridgeProfiles.forURL('https://gemini.google.com/app/example');
assert.equal(gemini.name, 'gemini');
assert.ok(gemini.selectors.input.some((selector) => selector.includes('rich-textarea')));
assert.ok(gemini.selectors.response.some((selector) => selector.includes('model-response')));

assert.equal(context.ContextBridgeProfiles.forURL('https://chatgpt.com.example.test/'), null);
assert.equal(context.ContextBridgeProfiles.forURL('https://example.test/'), null);

console.log('Built-in browser profiles verified');
