# Connect one PC to several relays

One ContextBridge process can serve the local models and browser extension. Run a separate **worker process and identity per relay** to decide which server may use which part of that PC. Do not reuse a node identity or producer token on another relay.

Pair the second relay without changing the first relay's config:

```powershell
contextbridge pair --config C:\ContextBridge\config.yml --relay https://relay-b.example --identity C:\ContextBridge\data\relay-b-identity.json --name My-PC-B
```

Approve that pairing on relay B. With the primary `contextbridge run` still running, start a second terminal:

```powershell
contextbridge worker --config C:\ContextBridge\config.yml --relay https://relay-b.example --identity C:\ContextBridge\data\relay-b-identity.json --slots 1 --providers browser --tasks generation,vision --groups web --no-updates
```

This worker uses the **same local bridge**, but offers relay B only one job at a time and only its listed tasks/providers. Use `--models "3.1 Pro"` if relay B must be limited to one exact model. A submitted provider/model outside the allow-list is rejected by the worker even if the relay advertises it incorrectly; an unspecified provider/model is forced to the first permitted entry. `cluster.worker.allowed_tasks`, `allowed_providers`, and `allowed_models` provide persistent equivalents in separate worker config files. The existing `groups` and `tags` participate in relay-side placement.

`--slots N` on `run` or `worker` is a session-only override of `cluster.worker.max_concurrent`. `--topmost` asks Windows to keep a classic CMD console in front until the process exits. Without `--topmost`, `run` and `worker` already stay in the current terminal and Ctrl+C stops them. Windows Terminal may not provide a pinnable console window.

Set `cluster.worker.node_name` in a worker config to rename it persistently, or pass `worker --name "Studio PC"` for one session. Identical display names are allowed: the relay routes by the worker's full identity ID, while the dashboard and CMD append a short gray discriminator (`Studio PC#d5ca35`) with an orange `#` in color terminals. Renaming does not re-pair or change that identity.

There is **no cross-process hardware semaphore yet**: two workers on the same PC do not know each other's configured job budgets. Keep the sum of their `--slots` within a safe limit for your RAM/VRAM and local models. Browser jobs are additionally serialized per attached tab, and a busy browser tab no longer hides capacity for independent local-model work. Explicit `min_free_vram_bytes` is a hard scheduling requirement; other hardware measurements are placement hints, not an out-of-memory guarantee. A single-process multi-relay coordinator with a shared hardware budget is planned, not claimed implemented.

Disable the secondary worker's updater with `--no-updates` so the primary service remains the only local update owner. This setup does not replicate a relay database or make two relays one active-active server.
