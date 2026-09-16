# Privacy and compliance readiness

This document maps ContextBridge 0.5.70 behavior to common privacy and control
questions. It is an engineering aid, **not legal advice, a data processing
agreement, a GDPR/DSGVO declaration, or a SOC 2 report**. Compliance depends on
the deployment, purposes, people, contracts, configuration, and operating
procedures around the software.

The [MIT license](../LICENSE) grants rights to use the source code. GDPR/DSGVO
compliance and SOC 2 are not additional software “licenses.” SOC 2 is an
independent auditor's attestation over an organization's controls for a defined
scope and, depending on report type, a date or period. No repository file can
award it, and ContextBridge does not claim to have a SOC 2 examination or
report.

## Decide the roles before deployment

| Context | Likely responsibility to evaluate |
| --- | --- |
| A person runs ContextBridge only for their own private work | Household or other applicable exemption may be relevant; the operator still controls provider accounts and local files. |
| An organization submits its employees' or customers' data | That organization commonly determines purpose and means and should assess its controller obligations. |
| A service operator runs a relay or workers for a customer's instructions | The operator may be a processor and may need an Article 28 DPA, documented instructions, confidentiality commitments, and deletion/return terms. |
| A self-hosted relay and worker remain inside one organization | The ContextBridge maintainer does not become a processor merely because open-source software is installed. |
| ChatGPT, Gemini, a cloud VM, backup, proxy, or telemetry service receives deployment data | The deploying organization must classify each recipient or subprocessor under its own contract and data-flow record. |

Roles can differ per flow. Obtain qualified privacy counsel for the actual use
case, especially for special-category data, children, employment decisions,
systematic monitoring, or international transfers.

## Data-flow inventory

Prompt and result content may contain personal data even when ContextBridge
does not interpret it. Node names, IP-derived installer identifiers, account
URLs, job ownership, timestamps, and hardware metadata may also be personal or
pseudonymous data in context.

| Plane | Data handled | Default destination and visibility |
| --- | --- | --- |
| Local job service | Trusted prompt, submitted text, optional image, result, artifacts, job state, metrics | Files under the configured local data/inbox directories; available to the local OS account and authenticated local API. |
| Browser worker | Selected prompt/image, visible provider controls, newest attributable response, verified artifact bytes | The explicitly attached provider tab and that provider's service. ContextBridge does not forward cookies, full DOM, old chat history, or account credentials to the relay. |
| Cluster relay | Producer subject, requirements, node assignment, routing metadata, status/events, and job payload/result | Relay BoltDB. Ordinary TLS jobs are readable by the relay operator. For optional E2EE jobs, payload and result are ciphertext; routing metadata remains visible. |
| Worker identity | Node token and X25519 private key | Owner-only identity file on the worker. It should not enter shared backups or support bundles. |
| Worker heartbeat | Node identity/name, capabilities, models, browser slots, load, RAM/GPU/temperature, clocks, queue and job state | Authenticated relay and authorized status views. It contains no prompt or response body. |
| Browser extension storage | Approved origins and tabs, control profiles, instance/session state, connection preferences | Local browser profile. Optional unsent-draft preservation writes plaintext local history, bounded to 1 MiB, and is off by default. |
| Local RAG | Tenant ID, document text/metadata, embeddings | Configured local vector store. Applications must derive tenant IDs server-side. |
| Managed downloads and updates | Version/platform request and public artifact metadata | Configured release/model hosts when the operator invokes or enables those features. Prompt and result content is not required. |
| Public installer endpoint on `angusu.de` | Aggregate request count and daily HMAC of a masked network prefix | Hosted website metric only. No raw IP, cookie, prompt, result, node name, or model activity is stored by this counter. Self-hosted runtime telemetry is not sent there. |

Browser AI providers receive the content submitted to their pages under their
own account terms and retention settings. E2EE against the ContextBridge relay
cannot hide a browser prompt from the web provider that must process it.

## Retention and deletion behavior

| Store | Current behavior in 0.5.70 | Operator action |
| --- | --- | --- |
| Relay terminal-job detail, events, terminal pipeline runs | Mandatory age-and-count sweep; defaults are at most 30 days and the newest 500 jobs, 5,000 events, and 200 runs. Only exact terminal states are removed. | Lower the guarded `cluster.relay.retention_*` limits where the purpose permits. Monitor sweeps and account for backups. |
| Opaque relay session placements | Mandatory age-and-count sweep; defaults are at most 30 days and the newest 5,000 placements. Records contain a hashed owner/session/scope key, node ID, optional tab ID, and update time—not the public session name or conversation URL. | Lower `cluster.relay.max_session_placements` where the purpose permits; expiry may require live session rediscovery. |
| Active, queued, reserved, assigned, running, or unknown relay work | Never deleted by the retention sweep because deletion could corrupt live state. | Resolve or cancel work through supported APIs before a planned data disposal. Do not edit BoltDB directly while the relay runs. |
| Relay lifetime summaries | Prompt/result detail is removed, but cumulative state, compute, and cost counters remain. | Treat retained timestamps/labels as metadata in the data inventory; do not promise erasure of aggregate counters without verifying identifiability. |
| Local jobs/results, artifacts, schedules, engine logs, and local RAG | No universal age-based purge covers all of these stores. | Set an OS/storage lifecycle appropriate to the purpose, stop affected writers before whole-store disposal, and include replicas/backups. |
| Optional draft history | Plaintext, local, capped at 1 MiB with oldest records removed by size rather than age. | Keep disabled for sensitive/shared accounts, or delete the file under the configured user home as part of a verified request. |
| Extension profiles and session state | Persist in the browser profile until explicitly detached/forgotten, browser data is cleared, or the extension is removed. | Include every browser profile in offboarding and erasure procedures. Detaching prevents new work but cannot unsend provider work. |
| Provider conversations and provider-generated files | Controlled by the selected external provider and account. | Apply that provider's export, retention, and deletion tools separately. |
| Hosted installer pseudonymous window | Daily rotating identity with a bounded 31-day uniqueness window; aggregate request totals may remain. | Document the website operator's lawful basis and notice. Do not describe the HMAC as anonymous merely because raw IP is not stored. |

Deletion is not guaranteed secure erasure from SSDs, filesystem journals,
snapshots, or backups. BoltDB can reuse freed pages without immediately
shrinking its file. Define backup expiry and restoration procedures so deleted
data is not silently reintroduced.

## Data-subject request runbook

ContextBridge 0.5.70 does **not** provide a universal one-command DSAR export or
per-person erasure endpoint. The deploying controller should implement and
test a procedure appropriate to its application:

1. Authenticate the requester and record scope, jurisdiction, and deadline.
2. Map the person to identifiers owned by the calling application, such as its
   authenticated producer subject and server-derived tenant ID. A public
   `session_id`, model response, or browser title is not an identity source.
3. Inventory local data directories, relay records, RAG stores, artifacts,
   schedules, extension profiles, logs, backups, and relevant provider
   conversations. Avoid exposing another producer's data during search/export.
4. Preserve records that have a documented legal hold; otherwise export,
   correct, restrict, or delete through supported application/provider
   operations. Stop writers before deleting an entire local store. Never
   manipulate a live BoltDB file ad hoc.
5. Apply the same decision to processors and backups under their contracts,
   then record what was completed, retained, or technically unavailable and
   why.
6. Verify the result by reading back the authorized views and by testing that
   a restored backup follows the same deletion policy.

Applications that need routine per-user deletion should add an authorized
application-layer index and deletion workflow before production. Do not infer a
person from prompt content or introduce a global content search that bypasses
producer and tenant isolation.

## Recipient and subprocessor register

There is no correct universal subprocessor list for self-hosted software. The
operator should maintain a deployment-specific register that covers at least:

- relay, worker, reverse-proxy, DNS, and hosting operators;
- selected browser AI providers and their optional tools;
- model, release, and package download hosts;
- backup, logging, monitoring, support, and incident-response services;
- the configured MCP host and any model/provider to which that host forwards
  tool input or output, when the local MCP adapter is enabled;
- any future remote MCP/client integration, database, folder, SSH, or custom
  source adapter added later; and
- the `angusu.de` installer endpoint, only when that hosted endpoint is used.

For each entry record purpose, data categories, location, transfer mechanism,
retention, security measures, DPA status, subprocessors, and deletion/export
route. Local-only Ollama or `llama.cpp` processing need not become a cloud
recipient merely because ContextBridge supports a cluster.

## GDPR/DSGVO readiness checklist

Technical controls already present can support, but do not establish,
compliance:

- localhost-by-default services, outbound worker connections, scoped roles,
  group policy, producer ownership, and tenant-separated local RAG;
- TLS in transit and optional worker-targeted E2EE for payload/result, with
  documented visible routing metadata;
- explicit browser-tab attachment and origin grants instead of permanent
  all-page access;
- bounded inputs/outputs, artifacts verified by size and SHA-256, fail-closed
  provider ambiguity, and no shell execution of model output;
- owner-only storage where supported, token hashing at the relay, expiring
  pairing codes, verified updates, and rollback; and
- mandatory bounded relay detail retention plus content-free heartbeat
  telemetry.

Before processing personal data in production, the operator should also:

- document purposes, lawful bases, categories, recipients, and a Record of
  Processing Activities where required;
- publish an accurate privacy notice and execute DPAs with processors;
- minimize prompts and node labels, configure retention, and test deletion,
  access, correction, restriction, portability, and objection workflows;
- assess international transfers and provider account settings;
- perform a DPIA where risk triggers it, including browser-provider and model
  output risks;
- define breach detection, escalation, notification, and evidence handling;
- control administrator access, rotate/revoke credentials, review permissions,
  and train operators; and
- document automated-decision safeguards and human review for consequential
  uses. ContextBridge's `review` fallback is not by itself an Article 22
  assessment.

## SOC 2 readiness map

The following is an engineering crosswalk, not an auditor-approved control
matrix. Control ownership and operating evidence belong to the organization
seeking an attestation.

| Control area | Useful ContextBridge evidence | Remaining organizational work or product gap |
| --- | --- | --- |
| Logical access and confidentiality | Separate admin/observer/producer/node credentials, producer-scoped reads/cancel, group scopes, owner-only secrets, TLS, optional E2EE | Identity lifecycle, MFA/SSO around deployed endpoints, periodic access reviews, key rotation evidence, and customer commitments |
| System operations and monitoring | Status, bounded events/history, provider/model failures, hardware/clock state, explicit unknown/needs-attention outcomes | Central alerting, on-call procedures, log access controls, retention approval, and incident exercises; privacy-default OTel is only planned |
| Change management | Automated tests, deterministic extension packaging, checksums, staged health check, rollback | Reviewed change tickets, segregation of duties, production approvals, emergency-change process, and retained CI/release evidence |
| Risk and vulnerability management | Private reporting channel, trust-boundary documentation, dependency verification, fail-closed routing | Asset/risk register, recurring scans, remediation SLAs, penetration testing, supplier reviews, and tracked exceptions |
| Availability and recovery | Durable single-relay queue/state, restart semantics, bounded schedules, update rollback | Measured SLOs, capacity tests, backup/restore exercises, recovery objectives, and failover; active-active relay HA is not shipped |
| Processing integrity | Validated job envelope/output, size limits, artifact hashes, node/attempt binding, at-most-once handling after ambiguous execution | Business-level input/output reconciliation, approval rules, exception ownership, and completeness testing per integration |
| Privacy | Data-flow inventory, explicit tabs, minimal heartbeat, no self-hosted runtime telemetry to `angusu.de`, bounded relay detail | Formal notice, consent/lawful-basis decisions, DSAR automation or runbook evidence, DPA/subprocessor register, DPIA, and privacy incident workflow |

An audit-ready evidence pack normally needs dated configuration baselines,
access-review records, release approvals, security-test results, vulnerability
and incident tickets, retention/deletion logs, vendor reviews, backup restore and
disaster-recovery results, change history, staff training, and management
sign-off. Repository tests demonstrate software behavior; they do not prove
that a control operated throughout an audit period.

## Product gaps to close before stronger claims

- deployment-specific privacy notice and DPA templates reviewed by counsel;
- an authenticated, producer/tenant-scoped export and deletion API where the
  intended customers need repeatable DSAR handling;
- documented secret rotation, access review, audit-log export, backup expiry,
  incident response, and disaster recovery procedures;
- privacy-default OpenTelemetry with content export separately opt-in;
- a reviewed subprocessor and international-transfer process for any hosted
  ContextBridge offering; and
- independent legal assessment and, if pursued, an independent SOC 2 audit.

Until those are done for a specific deployment, say **“technical controls that
support GDPR/SOC 2 readiness,”** not “GDPR certified,” “DSGVO licensed,” or
“SOC 2 compliant.” Security issues should be reported through
[SECURITY.md](../SECURITY.md).

## Primary references

- [European Commission: GDPR principles](https://commission.europa.eu/law/law-topic/data-protection/information-business-and-organisations/principles-gdpr_en)
- [European Commission: business and organisation obligations](https://commission.europa.eu/law/law-topic/data-protection/information-business-and-organisations/obligations_en)
- [European Data Protection Board: controller and processor concepts](https://www.edpb.europa.eu/documents/guideline/guidelines-072020-on-the-concepts-of-controller-and-processor-in-the-gdpr_en)
- [AICPA & CIMA: SOC 2 and the SOC suite](https://www.aicpa-cima.com/topic/audit-assurance/audit-and-assurance-greater-than-soc-2)
