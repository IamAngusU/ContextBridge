# ContextBridge extension UI state matrix

Target: `wip` / 0.5.71 alpha

The visual layer must handle every state below without inventing runtime facts. The same state can be rendered in light/dark and DE/EN. Unknown model/reasoning values stay unknown.

## Setup and idle

- `first-run` — Setup · pairing token required
- `permission-needed` — Idle · explicit page access pending
- `permission-requesting` — Connecting · browser permission requested
- `idle-empty` — Idle · no supported page
- `invalid-browser-tab` — Idle · browser page cannot be accessed
- `idle-ready` — Idle · page detected
- `idle-attached` — Idle · attached, connection stopped

## Connection lifecycle

- `connecting-tabs` — Connecting · checking tabs
- `connecting-service` — Connecting · local handshake
- `connected-scanning` — Connected · page controls scanning
- `service-offline` — Reconnecting · local service unreachable
- `reconnect-manual` — Stopped · automatic reconnect is off
- `compatibility-mismatch` — Reconnecting · component version mismatch
- `permission-denied` — Needs attention · page permission denied
- `detaching` — Connected · detaching current page
- `session-releasing` — Connected · releasing session reservation
- `disconnecting` — Disconnecting · browser bridge stopping
- `reconnecting` — Reconnecting · heartbeat stale
- `pairing-error` — Needs attention · pairing rejected
- `teaching-active` — Connected · visual teaching active
- `profile-verifying` — Connected · verifying visual profile
- `model-scanning` — Connected · model choices scanning

## Connected / page / pool

- `connected-idle` — Connected · tabs idle
- `connected-page-detached` — Connected · current page not attached
- `working` — Connected · page working
- `rate-limited` — Connected · provider cooling down
- `taught-ready` — Connected · taught page ready
- `controls-pending` — Connected · provider controls unavailable
- `slow-scan` — Connected · repeated slow page scan
- `growing-conversation` — Connected · growing conversation notice
- `last-job-failure` — Connected · last browser job failed safely
- `legacy-session` — Connected · old chat needs reassignment
- `edit-followup` — Connected · edit-last follow-up enabled
- `draft-protected` — Connected · personal draft protected
- `tab-limit` — Connected · 16-tab safety limit
- `fresh-autoattach` — Connected · fresh chat auto-attached
- `per-job-grace` — Connected · per-job tab cleanup grace
- `model-unknown` — Connected · current model unknown
- `current-window-only` — Connected · current window only
- `unsupported` — Connected · teach another AI page
- `page-health` — Connected · page health warning
- `long-conversation` — Connected · long conversation notice
- `profile-verification-failed` — Connected · custom profile needs repair
- `draft-save-failed` — Connected · local draft save failed safely
- `edit-guard-failed` — Connected · edit-last guard blocked the job
- `tab-list-permission-denied` — Connected · all-windows access declined
- `release-blocked` — Connected · chat cannot be released yet

## Local service and updates

- `update-locked` — Connected · automatic updates locked off
- `update-pending` — Connected · verified update waiting for idle

## Motion contract

When an authoritative value changes, the previous value becomes a short-lived ghost that moves upward and fades while the new value enters from below. This applies to:

- attached/free/working counters
- heartbeat
- connection state
- provider/page state
- page scope
- profile/detection status
- model name
- reasoning/effort level
- control-scan state
- session policy
- toggle state labels
- local-service and pairing state

Recommended timings:

- value in: ~320 ms
- value out: ~280 ms
- disclosure open/close: ~220 ms
- wizard step transition: ~300 ms

`prefers-reduced-motion: reduce` removes ghost transitions, glint, pulses, sheen and wizard movement.

## Safety/presentation rules

- Connected does not imply the current provider page is ready to send.
- A prior browser-job failure is not a connection failure.
- Detach is explicit and directly reachable.
- Release-session and Detach are different operations.
- Permission request, attach, detach, release, teaching, verification, connect/reconnect and disconnect each have distinct pending states.
- Existing drafts, attachments, ambiguous edit ownership and unsafe recovery remain fail-closed and should say what was preserved.
- A hidden but responsive background tab is healthy.
- Provider/internal-page unsupported states explain what the user can do next rather than showing a dead end.
