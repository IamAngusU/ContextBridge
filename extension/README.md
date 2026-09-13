# ContextBridge browser extension

Use the ready package for your browser:

- `chromium`: Chrome, Edge, Opera, Brave, Vivaldi, and other Chromium browsers
- `firefox`: Firefox 128 or newer

ChatGPT and Gemini are detected automatically after you explicitly attach their tabs. Highlighting a tab does not authorize it. The fresh-chat helper attaches only confirmed empty new chats; existing conversations require **Allow this tab**. Detach one tab or all tabs whenever you want; already-submitted website work may still finish. Select several tabs to expose parallel browser slots; a session stays pinned to one conversation. Other supported AI pages can be taught visually without writing selectors. The extension requests access only to pages you allow and the local ContextBridge endpoint. The optional tabs permission is requested for browsing every window or for fresh-chat discovery.

The popup's All/Attached/Not attached filter shows tabs from every window of the current browser once its optional tabs permission is granted. State and model labels update while the popup is open; provider pages are not reloaded. Separate browser installations keep separate extension storage, so a Chrome popup does not yet control an Opera extension's tabs. Gemini mode choices are scanned automatically on an idle attached tab with an empty composer, at most once per 30 minutes, or immediately with the scan button. A selected incompatible Gemini tool (for example Music during a plain-text job) is cleared before typing; a failed submit is never treated as an answer.

The worker console reports the visible model for each attached tab and logs changes without repeating unchanged heartbeats. A job's "requested" model or reasoning level is distinct from the model or level the browser extension reports after a successful selection. If the web UI does not reveal a separate reasoning level, ContextBridge leaves it unknown instead of guessing from the model name.

Browser jobs can opt in to response artifacts. The extension reads up to twelve generated images, download links, and code blocks only from the newly completed assistant turn, including image-only ChatGPT turns. Readable data is returned with a verified hash; protected or oversized HTTPS resources remain references. Set `output.min_artifacts` when files of any type are required, or `output.min_images` when transferred image bytes are required. `cluster chat --image` requests images through the prompt without changing the web chat's tool selection; `metadata.contextbridge_image_tool: true` remains an explicit option for selecting the visible ChatGPT image tool. Hidden ChatGPT file inputs can receive `image_base64` alongside prompt text. Image progress, rate limits, provider errors, and reload recovery are reported separately from answer text. Follow-up jobs sent with the same cluster `session_id` are routed back to this browser conversation. `contextbridge browser inspect` reads the latest local, content-free selector snapshot without relaying the page DOM.

Run `./scripts/package-extensions.sh` from the repository root after changing files in `extension/src` or either manifest.
