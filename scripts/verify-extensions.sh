#!/bin/sh
set -eu

root="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"

same_file() {
  node -e "const fs=require('fs'); process.exit(Buffer.compare(fs.readFileSync(process.argv[1]),fs.readFileSync(process.argv[2])) === 0 ? 0 : 1)" "$1" "$2"
}

node --check "$root/extension/src/background.js"
node --check "$root/extension/src/picker.js"
node --check "$root/extension/src/popup.js"
node --check "$root/extension/src/profiles.js"
node "$root/extension/tests/profiles.test.mjs"
node "$root/extension/tests/browser-state.test.mjs"
node "$root/extension/tests/recovery-guard.test.mjs"
node "$root/extension/tests/edit-last-message.test.mjs"
node "$root/extension/tests/tab-selection.test.mjs"
node "$root/extension/tests/tab-consent.test.mjs"
node "$root/extension/tests/session-isolation.test.mjs"

for browser in chromium firefox; do
  package="$root/extension/$browser"
  node -e "JSON.parse(require('fs').readFileSync(process.argv[1], 'utf8'))" "$package/manifest.json"
  for file in background.js profiles.js picker.js popup.html popup.css popup.js manifest.json icons/icon-16.png icons/icon-32.png icons/icon-48.png icons/icon-128.png; do
    test -s "$package/$file" || { echo "Missing $browser/$file" >&2; exit 1; }
  done
  same_file "$root/extension/src/background.js" "$package/background.js"
  same_file "$root/extension/src/profiles.js" "$package/profiles.js"
  same_file "$root/extension/src/picker.js" "$package/picker.js"
  same_file "$root/extension/src/popup.html" "$package/popup.html"
  same_file "$root/extension/src/popup.css" "$package/popup.css"
  same_file "$root/extension/src/popup.js" "$package/popup.js"
  same_file "$root/extension/manifests/$browser.json" "$package/manifest.json"
done

grep -q '"service_worker"' "$root/extension/chromium/manifest.json"
grep -q '"scripts"' "$root/extension/firefox/manifest.json"

echo "Browser extension packages verified"
