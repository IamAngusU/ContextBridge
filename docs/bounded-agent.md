# Reviewed bounded agent plans

ContextBridge can use one explicitly selected model to **propose** a short
text workflow. It does not turn the relay or browser extension into an
unrestricted autonomous agent.

The contract is deliberately two-phase:

```text
untrusted planner output
        ↓
strict local schema + policy validation
        ↓
human-readable plan + route previews + SHA-256
        ↓
separate command containing that exact SHA-256
        ↓
reviewed text-only jobs through the normal pool
```

The planner cannot add a provider, browser profile, credential, model override,
tool, file operation, shell command, retry, or extra step. ContextBridge binds
the goal and allowlists locally after decoding the planner response. A plan is
never executed directly from the response that created it.

## 1. Create a plan

This example lets DeepSeek propose at most two steps. Approved steps may use
local Ollama or a fresh temporary Gemini conversation:

```bash
contextbridge cluster agent plan \
  --config /var/lib/contextbridge/config.yml \
  --goal "Draft one concise release note locally, then ask Gemini to check it for unsupported claims." \
  --planner-provider deepseek \
  --allow-providers ollama,browser \
  --allow-browser-profiles gemini \
  --max-steps 2 \
  --out ./reviewed-plan.json
```

Use one line in Windows CMD. PowerShell uses a backtick—not a backslash—for
line continuation. A long or reusable goal can instead be supplied with
`--goal-file`.

Planning prints:

- every proposed instruction and whether it receives a previous result;
- the exact provider/profile chosen for each step;
- current non-executing route previews;
- the hard per-step and total runtime limits; and
- an approval value such as `sha256:...`.

If a route has no capacity at that moment, the plan can still be reviewed for
later execution. Planning itself is one ordinary attributable cluster job and
therefore uses the selected planner provider's normal egress, balance, and cost
policy.

## 2. Review the file

Open `reviewed-plan.json`. Check the goal, summary, every instruction,
provider/profile allowlist, timeouts, and planner evidence. The plan format is
strict: unknown JSON fields, duplicate or unsafe step IDs, unapproved targets,
oversized text, a browser step without an approved profile, and more than six
steps are rejected.

Changing even one policy value or instruction changes the SHA-256. If you edit
the file, generate or otherwise obtain a new reviewed hash; the old approval
will no longer match.

## 3. Execute exactly that plan

Copy the complete hash printed by the planning command:

```bash
contextbridge cluster agent run \
  --config /var/lib/contextbridge/config.yml \
  --plan ./reviewed-plan.json \
  --approve sha256:REPLACE_WITH_THE_PRINTED_DIGEST
```

Each step is an ordinary ContextBridge job with `max_attempts: 1`. Browser
steps use separate ContextBridge-owned ephemeral chats. When a later step uses
the previous result, those bytes go into the normal untrusted submitted-content
field; they do not silently become a system instruction. A truncated response
stops the run and is never passed onward.

The command reports tracked cost evidence when it exists. A browser,
subscription, or local job without monetary evidence is shown as **cost
unknown**, never `$0`. Provider-side balance guards and reservations still
apply to configured paid API routes.

## Current alpha limits

- text-only sequential plans; one to six steps;
- no artifacts, images, audio, arbitrary URLs, tools, shell, code execution,
  automatic downloads, recursive agents, or dynamic provider registration;
- no automatic replanning or retries;
- no planner E2EE yet—the relay and executing planner provider can see the
  goal; use native E2EE jobs manually when that boundary is required;
- no single cross-provider monetary reservation: each paid provider enforces
  its own configured guard, while unknown costs stay explicitly unknown;
- if waiting is interrupted after submission, the job may still finish.
  ContextBridge prints its job ID and tells the operator to inspect it before
  considering a retry.

This feature is an optional producer above the deterministic relay. The normal
job protocol, schedules, pipelines, MCP adapter, OpenAI-compatible API, and
pool remain fully usable without any planner model.
