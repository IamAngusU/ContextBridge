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
assert.equal(chatgpt.selectors.response[0], 'section[data-turn="assistant"][data-testid^="conversation-turn-"]');
assert.equal(chatgpt.selectors.file_input[0], '#upload-photos');

const legacyChatGPT = context.ContextBridgeProfiles.forURL('https://chat.openai.com/c/example');
assert.equal(legacyChatGPT.name, 'chatgpt');
assert.equal(legacyChatGPT.match_url, 'https://chat.openai.com/*');

const gemini = context.ContextBridgeProfiles.forURL('https://gemini.google.com/app/example');
assert.equal(gemini.name, 'gemini');
assert.ok(gemini.selectors.input.some((selector) => selector.includes('rich-textarea')));
assert.ok(gemini.selectors.response.some((selector) => selector.includes('model-response')));
assert.equal(gemini.selectors.response[0], 'model-response');

assert.equal(context.ContextBridgeProfiles.forURL('https://chatgpt.com.example.test/'), null);
assert.equal(context.ContextBridgeProfiles.forURL('https://example.test/'), null);

const blankBuiltIn = context.ContextBridgeProfiles.verification(chatgpt, { input: 1, submit: 0, response: 0, enterFallback: true });
assert.equal(blankBuiltIn.ok, true);
assert.equal(blankBuiltIn.responsePending, true);

const blankCustom = context.ContextBridgeProfiles.verification({ ...chatgpt, source: 'visual' }, { input: 1, submit: 1, response: 0, enterFallback: true });
assert.equal(blankCustom.ok, false);
assert.equal(blankCustom.responsePending, false);

console.log('Built-in browser profiles verified');
