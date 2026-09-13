# ContextBridge browser extension

Use the ready package for your browser:

- `chromium`: Chrome, Edge, Opera, Brave, Vivaldi, and other Chromium browsers
- `firefox`: Firefox 128 or newer

ChatGPT and Gemini are detected automatically after you explicitly attach their tabs. Highlighting a tab does not authorize it. The fresh-chat helper attaches only confirmed empty new chats; existing conversations require **Allow this tab**. Detach one tab or all tabs whenever you want; already-submitted website work may still finish. Select several tabs to expose parallel browser slots; a session stays pinned to one conversation. Other supported AI pages can be taught visually without writing selectors. The extension requests access only to pages you allow and the local ContextBridge endpoint. The optional tabs permission is requested for browsing every window or for fresh-chat discovery.

Browser jobs can opt in to response artifacts. The extension reads up to twelve generated images, download links, and code blocks only from the newly completed assistant turn, including image-only ChatGPT turns. Readable data is returned with a verified hash; protected or oversized HTTPS resources remain references. Set `output.min_artifacts` when files of any type are required, or `output.min_images` when transferred image bytes are required. `cluster chat --image` requests images through the prompt without changing the web chat's tool selection; `metadata.contextbridge_image_tool: true` remains an explicit option for selecting the visible ChatGPT image tool. Hidden ChatGPT file inputs can receive `image_base64` alongside prompt text. Image progress, rate limits, provider errors, and reload recovery are reported separately from answer text. Follow-up jobs sent with the same cluster `session_id` are routed back to this browser conversation. `contextbridge browser inspect` reads the latest local, content-free selector snapshot without relaying the page DOM.

Run `./scripts/package-extensions.sh` from the repository root after changing files in `extension/src` or either manifest.
