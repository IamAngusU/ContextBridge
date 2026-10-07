# Optional lazy work style

`lazy` means understand first, reuse existing code and make the smallest correct
change. It does **not** mean skip requirements, tests, approvals or safety checks.
This is prompt guidance, not a new model, autonomous agent, sandbox or permission.

## Use

Flags precede the natural-language prompt:

```sh
cb do --mode lazy --egress local_only --prompt "Simplify this function without changing its behavior: ..."
cb cluster chat --mode lazy --egress local_only
cb cluster agent plan --mode lazy --policy my-project --goal "Fix the reported bug and verify the affected behavior" --out ./plan.json
cb cluster agent auto --mode lazy --goal "Review the supplied function for a simpler equivalent implementation"
```

Use an existing, explicitly configured project policy; `my-project` is a
placeholder, not an installed authority. Plan preview still runs an authorized
planner job. The local-only auto tier remains text-only and grants no file or
shell access. Creating files requires separately authorized adapters.

In interactive chat, `/mode lazy` enables the style, `/mode normal` sends a reset
instruction on subsequent model requests, and `/mode` or `/settings` shows it.
A mode switch does not erase conversation history or guarantee that a model will
forget earlier instructions. Start a fresh chat if a clean context is required.
No global hooks, settings or cross-session defaults are installed. Omitting
`--mode` preserves the previous prompt bytes. Explicit `normal` adds only the
style-reset instruction.

## Contract and limits

- The reuse order is project code, standard library, native platform features,
  installed dependencies, then a small readable implementation.
- Correctness, security, boundary checks, data-loss handling, accessibility,
  useful explanations and risk-appropriate tests remain required.
- Mode is operator-owned, recorded in the plan and covered by its approval hash.
  Unsupported saved modes and planner-supplied mode fields are rejected.
- The planner and non-adapter work steps receive the guidance. Exact adapter
  requests stay byte-for-byte unchanged, including in direct adapter chat. A
  dedicated adapter would need its own typed, explicitly supported style option.
- Provider, tenant, group, cost, offline policy, attempts, credentials and
  approval rules are unchanged. `lazy` is not an internet or execution opt-in.
- Deterministic arithmetic and random-number tools still run before LLM routing.

Tests prove prompt propagation, unchanged authority and typed adapter contracts,
not better code from every model. No latency, token, cost or coding-quality
improvement has been measured for ContextBridge from this style. The previous
local coding qualification is not superseded by these tests.

## Provenance

The decision ladder is adapted from [DietrichGebert/ponytail](https://github.com/DietrichGebert/ponytail)
at [e15862bb04d04285233a164460ced063941d9ef5](https://github.com/DietrichGebert/ponytail/tree/e15862bb04d04285233a164460ced063941d9ef5).
The [MIT notice](third-party/ponytail-LICENSE.txt) is retained. ContextBridge does
not install Ponytail's global hooks or adopt an always-on/ultra mode. Upstream's
own benchmark claims are not evidence about ContextBridge or its local models.
