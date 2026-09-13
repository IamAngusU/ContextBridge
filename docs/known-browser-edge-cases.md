# Known browser edge cases

ContextBridge drives third-party web chats through their visible UI. Their layouts and provider states can change without notice. The following cases have been observed or reported by the maintainer but have not yet been captured with a reliable, privacy-safe reproduction. They are **not** claimed fixed:

- ChatGPT can temporarily block a conversation while it checks whether a request violated its rules. That interstitial is not a normal answer. Detection should keep it out of successful output and distinguish a recoverable pause from a terminal rejection once a reproducible sample exists.
- ChatGPT can ask for a brief pause when too many jobs run in parallel. This is not necessarily an account rate limit. The bridge should classify it separately and retry after the pause rather than imposing the full rate-limit cooldown; the exact DOM/text variants still need reproducible captures.

If you encounter either case, record the provider, locale, selected model, approximate time, whether the composer/send button was disabled, and a redacted DOM fragment of the status element. Do not post whole chat HTML, account details, tokens, or private prompts. A small reproduction with the affected control's role, `aria-label`, `data-testid`, and state transitions is most useful. Until these cases can be validated, treat ambiguous provider UI as a failed/needs-attention job, never as model output.
