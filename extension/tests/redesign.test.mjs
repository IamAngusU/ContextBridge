import assert from 'node:assert/strict';
import fs from 'node:fs';

const popup = fs.readFileSync(new URL('../src/popup.html', import.meta.url), 'utf8');
const live = fs.readFileSync(new URL('../src/popup-live-ui.js', import.meta.url), 'utf8');
const styles = fs.readFileSync(new URL('../src/popup.css', import.meta.url), 'utf8');
const badge = fs.readFileSync(new URL('../src/badge.js', import.meta.url), 'utf8');
const wizard = fs.readFileSync(new URL('../src/wizard.html', import.meta.url), 'utf8');
const wizardScript = fs.readFileSync(new URL('../src/wizard.js', import.meta.url), 'utf8');

assert.match(popup, /<span class="alpha-badge"[^>]*>a<\/span>/, 'alpha marker must remain styled text, not a control');
assert.match(popup, /id="alpha-tooltip" role="tooltip"/, 'alpha marker should explain the preview state on hover or focus');
assert.doesNotMatch(popup, /<button[^>]+(?:alpha-badge|alpha-mark)/, 'alpha marker must not become a command button');
assert.doesNotMatch(popup, /cb_pair_demo|state-data|State lab/i, 'prototype secrets and state controls must not ship');
assert.match(popup, /id="pairing-token"/);
assert.match(popup, /id="show-token"/);
assert.match(live, /pool\.capability_view/);
assert.match(live, /requestAnimationFrame/, 'live values should use motion-aware updates');
assert.match(popup, /id="preferences-button"/);
assert.match(popup, /id="preferences-popover"/);
assert.doesNotMatch(popup, /id="(?:language|appearance)-button"/, 'language and appearance belong to one preference control');
assert.match(styles, /\.app-body\s*\{[^}]*overflow-y:\s*auto/s, 'the content region must own the only scrollbar');
assert.match(styles, /\.popup\s*\{[^}]*overflow:\s*hidden/s, 'the popup card must not create a second scrollbar');
assert.match(styles, /\.popup\s*\{[^}]*width:\s*100%[^}]*margin:\s*0[^}]*border:\s*0/s, 'the extension must use the complete browser popup surface');
assert.match(styles, /scrollbar-color:\s*#171916\s+transparent/, 'the sole content scrollbar should use the ContextBridge graphite color');
assert.match(badge, /Chantal W\.', role: 'Marketing & Brainstorming'/);

assert.equal((wizard.match(/class="step/g) || []).length, 3, 'setup should stay a concise three-step flow');
assert.match(wizardScript, /permissions\.request/);
assert.match(wizardScript, /type:'test'/);
assert.match(wizardScript, /type:'start'/);
assert.doesNotMatch(wizard, /demo token|cb_pair_demo/i);

for (const browser of ['chromium', 'firefox']) {
  const manifest = JSON.parse(fs.readFileSync(new URL(`../manifests/${browser}.json`, import.meta.url), 'utf8'));
  assert.equal(manifest.version, '0.5.74');
}

console.log('V10 popup and reduced setup wizard contracts verified');
