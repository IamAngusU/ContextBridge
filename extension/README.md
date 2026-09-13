# ContextBridge browser extension

Use the ready package for your browser:

- `chromium`: Chrome, Edge, Opera, Brave, Vivaldi, and other Chromium browsers
- `firefox`: Firefox 128 or newer

ChatGPT and Gemini are detected automatically after you select their tab. Other supported AI pages can be taught visually without writing selectors. The extension requests access only to the page you select and the local ContextBridge endpoint. The optional tabs permission is requested only when you choose to browse tabs from every browser window.

Browser jobs can opt in to response artifacts. The extension reads generated images, download links, and code blocks only from the newly completed assistant response. Readable data is returned with a verified hash; protected or oversized HTTPS resources remain references. Follow-up jobs sent with the same cluster `session_id` are routed back to this browser conversation.

Run `./scripts/package-extensions.sh` from the repository root after changing files in `extension/src` or either manifest.
