#!/bin/sh
set -eu

root="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
brand="$root/assets/brand"

for target in "$root/extension/assets" "$root/docs/assets"; do
  mkdir -p "$target"
  cp "$brand/contextbridge-mark.svg" "$target/contextbridge-mark.svg"
  cp "$brand/contextbridge-wordmark.svg" "$target/contextbridge-wordmark.svg"
  cp "$brand/contextbridge-mark.webp" "$target/contextbridge-mark.webp"
  cp "$brand/contextbridge-wordmark.webp" "$target/contextbridge-wordmark.webp"
done

cp "$brand/contextbridge-mark-16.png" "$root/extension/assets/contextbridge-mark-16.png"
cp "$brand/contextbridge-mark-24.png" "$root/extension/assets/contextbridge-mark-24.png"
cp "$brand/contextbridge-mark-32.png" "$root/extension/assets/contextbridge-mark-32.png"
cp "$brand/contextbridge-mark.svg" "$root/internal/bridge/dashboard/mark.svg"

echo "ContextBridge brand assets synchronized"
