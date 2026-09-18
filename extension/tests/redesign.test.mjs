import assert from 'node:assert/strict';
import fs from 'node:fs';

const popup = fs.readFileSync(new URL('../src/popup.html', import.meta.url), 'utf8');
const live = fs.readFileSync(new URL('../src/popup-live-ui.js', import.meta.url), 'utf8');
const wizard = fs.readFileSync(new URL('../src/wizard.html', import.meta.url), 'utf8');
const wizardScript = fs.readFileSync(new URL('../src/wizard.js', import.meta.url), 'utf8');

assert.match(popup, /<span class="alpha-badge"[^>]*>a<\/span>/, 'alpha marker must remain styled text, not a control');
assert.doesNotMatch(popup, /cb_pair_demo|state-data|State lab/i, 'prototype secrets and state controls must not ship');
assert.match(popup, /id="pairing-token"/);
assert.match(popup, /id="show-token"/);
assert.match(live, /pool\.capability_view/);
assert.match(live, /requestAnimationFrame/, 'live values should use motion-aware updates');

assert.equal((wizard.match(/class="step/g) || []).length, 3, 'setup should stay a concise three-step flow');
assert.match(wizardScript, /permissions\.request/);
assert.match(wizardScript, /type:'test'/);
assert.match(wizardScript, /type:'start'/);
assert.doesNotMatch(wizard, /demo token|cb_pair_demo/i);

for (const browser of ['chromium', 'firefox']) {
  const manifest = JSON.parse(fs.readFileSync(new URL(`../manifests/${browser}.json`, import.meta.url), 'utf8'));
  assert.equal(manifest.version, '0.5.72');
}

console.log('V10 popup and reduced setup wizard contracts verified');
