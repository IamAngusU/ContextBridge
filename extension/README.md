# ContextBridge browser extension

Use the ready package for your browser:

- `chromium`: Chrome, Edge, Opera, Brave, Vivaldi, and other Chromium browsers
- `firefox`: Firefox 128 or newer

ChatGPT and Gemini are detected automatically after you select their tab. Other supported AI pages can be taught visually without writing selectors. The extension requests access only to the page you select and the local ContextBridge endpoint. The optional tabs permission is requested only when you choose to browse tabs from every browser window.

Run `./scripts/package-extensions.sh` from the repository root after changing files in `extension/src` or either manifest.
