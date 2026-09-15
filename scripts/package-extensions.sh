#!/bin/sh
set -eu

root="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
source_dir="$root/extension/src"
manifests="$root/extension/manifests"

for browser in chromium firefox; do
  target="$root/extension/$browser"
  rm -rf "$target"
  mkdir -p "$target/icons"
  cp "$source_dir/background.js" "$target/background.js"
  cp "$source_dir/profiles.js" "$target/profiles.js"
  cp "$source_dir/picker.js" "$target/picker.js"
  cp "$source_dir/popup.html" "$target/popup.html"
  cp "$source_dir/popup.css" "$target/popup.css"
  cp "$source_dir/popup.js" "$target/popup.js"
  cp "$source_dir/icons/"*.png "$target/icons/"
  cp "$source_dir/icons/"*.webp "$target/icons/"
  cp "$manifests/$browser.json" "$target/manifest.json"
done

echo "Built extension/chromium and extension/firefox"
