#!/bin/sh
set -eu

root="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"

node --check "$root/extension/src/background.js"
node --check "$root/extension/src/picker.js"
node --check "$root/extension/src/popup.js"

for browser in chromium firefox; do
  package="$root/extension/$browser"
  node -e "JSON.parse(require('fs').readFileSync(process.argv[1], 'utf8'))" "$package/manifest.json"
  for file in background.js picker.js popup.html popup.css popup.js manifest.json icons/icon-16.png icons/icon-32.png icons/icon-48.png icons/icon-128.png; do
    test -s "$package/$file" || { echo "Missing $browser/$file" >&2; exit 1; }
  done
  cmp "$root/extension/src/background.js" "$package/background.js"
  cmp "$root/extension/src/picker.js" "$package/picker.js"
  cmp "$root/extension/src/popup.html" "$package/popup.html"
  cmp "$root/extension/src/popup.css" "$package/popup.css"
  cmp "$root/extension/src/popup.js" "$package/popup.js"
  cmp "$root/extension/manifests/$browser.json" "$package/manifest.json"
done

grep -q '"service_worker"' "$root/extension/chromium/manifest.json"
grep -q '"scripts"' "$root/extension/firefox/manifest.json"

echo "Browser extension packages verified"
