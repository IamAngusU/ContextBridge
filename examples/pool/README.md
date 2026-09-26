<!-- SPDX-License-Identifier: Apache-2.0 -->

# Send a pool job. Keep control.

A shared-hosting application can be a **producer**. It sends an authenticated
HTTPS request to a relay; an approved worker runs the model. No CB executable,
GPU, inbound worker port, root access or background daemon is needed on the
web host. The relay and workers still need to run elsewhere.

```text
Your website -> server-side PHP -> HTTPS relay -> approved worker -> model
                       ^                |
                       +-- job/result --+
```

These are bounded AI jobs, not arbitrary shell commands or remote filesystem
access. Examples here and the PHP client are Apache-2.0 integration materials;
see [the license boundary](../../LICENSING.md).

## 1. Prepare a relay and a producer credential

Follow [device pairing and token setup](../../README.md#connect-your-devices).
On the running relay host, issue a separate credential for this application:

```sh
contextbridge cluster token --role producer --subject my-web-app --allowed-tenants my-web-app --max-queued-jobs 8 --max-jobs-per-hour 120
```

Keep the returned token server-side. Workers pair separately; they do not need
this producer token. Never distribute the relay admin credential to an app.
An application using one producer token must still authorize its own users
and map their requests to their own jobs. A caller-supplied `tenant_id` or job
ID is not a replacement for application authentication.
The single credential-bound tenant is applied automatically to requests that
omit `tenant_id`; another label is rejected before policy selection.

## 2. What goes to the pool

[text-job.json](text-job.json) is a complete native Job Contract v1 request.
It asks an Ollama worker to summarize text and caps the returned text at
4,096 bytes. It does not pin a model: the worker uses its configured model or
a compatible model selected by its local runtime configuration.

From a configured, authenticated CLI client, save the example as `job.json`:

```sh
contextbridge cluster contract validate --file ./job.json --json
contextbridge route explain --file ./job.json
contextbridge cluster submit --file ./job.json
```

The first two commands submit no model work. A successful dry run is not a
reservation or a promise that capacity will still be available on submission.
The last command submits a real job. `max_attempts` bounds assignment
generations only. ContextBridge retries automatically only when a non-adapter
worker reports capacity or shutdown refusal before it reports `started` and
without any execution evidence. It never grants permission to replay a job
whose execution state became ambiguous.

The equivalent HTTP submission is a JSON POST to
`/v1/cluster/jobs?compact=1`, with `Content-Type: application/json`,
`Authorization: Bearer <producer-token>` and `Idempotency-Key: <operation-id>`.
Use [Job Contract v1](../../docs/schemas/job-contract-v1.schema.json), not the
local `/v1/jobs` endpoint, for this relay workflow.

## 3. Submit from PHP shared hosting

The [PHP client](../php/ContextBridgeClient.php) requires **PHP 8.1+, ext-curl,
working TLS trust and outbound HTTPS to your relay**. It needs no Composer
packages. A host that disables cURL or blocks the relay cannot use this
transport unchanged. Keep certificate verification enabled.

Copy `ContextBridgeClient.php` and `text-job.json` next to your server-side
integration code, outside the public document root. Set the relay URL and
producer token through the hosting panel or an existing private configuration
loader. Do not publish a credential file or create an unauthenticated public
proxy to your pool.

This fragment belongs inside your authenticated application, not in a publicly
callable demo endpoint. The idempotency key must be created and persisted for
one logical application operation before submission, and reused with the same
request on any explicit retry. Here it is read from configuration to keep the
example independent of your database/framework:

```php
<?php
// SPDX-License-Identifier: Apache-2.0

declare(strict_types=1);

require __DIR__ . '/ContextBridgeClient.php';

$relay = getenv('CONTEXTBRIDGE_RELAY_URL') ?: '';
$token = getenv('CONTEXTBRIDGE_PRODUCER_TOKEN') ?: '';
$key = getenv('CONTEXTBRIDGE_IDEMPOTENCY_KEY') ?: '';
if ($relay === '' || $token === '' || $key === '') {
    throw new RuntimeException('Missing private relay configuration or operation key.');
}

$raw = file_get_contents(__DIR__ . '/text-job.json');
if ($raw === false) {
    throw new RuntimeException('Cannot read the example request.');
}
$request = json_decode($raw, true, 64, JSON_THROW_ON_ERROR);
if (!is_array($request)) {
    throw new RuntimeException('Expected a job object.');
}

$client = new \ContextBridge\ContextBridgeClient($relay, $token);
$accepted = $client->submit($request, $key);
$jobId = $accepted['id'] ?? null;
if (!is_string($jobId) || $jobId === '') {
    throw new RuntimeException('Relay returned no job ID.');
}
// Persist $jobId against the authenticated user's operation before returning.
// Admission is not completion. Fetch status in a later application request.
```

In a later authenticated request or supported cron task, recreate the client,
load the job ID from your application storage and poll it:

```php
$job = $client->job($jobId);
if (($job['status'] ?? '') === 'completed') {
    $text = $job['result']['output']['text'] ?? null;
    // Validate the result for your use case; escape text when rendering HTML.
}
// Handle failed/cancelled separately; other states are still pending.
```

Avoid holding a normal web request open with a long `wait()` loop. Hosting and
PHP request limits still apply. A client timeout does not cancel the durable
job. `$client->cancel($jobId)` requests cancellation; it cannot undo an action
already taken by an upstream provider. Do not automatically resubmit after an
ambiguous timeout with a new key.

**Privacy boundary:** this example uses HTTPS, not end-to-end encryption. The
PHP helper has no sealing/reservation helper; the relay can read this plaintext
payload. E2EE requires the full producer-to-reserved-worker protocol, not an
invented `e2ee: true` JSON field. The CLI supports `cluster chat --e2ee`.
Even with E2EE, a PHP server that receives an upload already sees its plaintext.

## 4. Choose what a job may use and return

A natural-language instruction requests model behavior. A structured job field
constrains execution or output. The operator's relay policy and the worker
owner's allowlists set the ceiling; a prompt cannot enlarge that authority.

| Choice | Current field | Boundary |
| --- | --- | --- |
| Provider or model | `requirements.provider`, `requirements.model` | Must be configured, advertised and allowed. Keep matching payload fields consistent. |
| Pool segment | `requirements.group`, `requirements.required_tags` | Hard filters over worker evidence; not proof of hardware ownership or location. |
| Prefer a worker | `requirements.preferred_nodes` | Preference, not an exclusive-node constraint. |
| Free GPU memory | `requirements.min_free_vram_bytes` | Hard minimum over reported evidence, not physical GPU reservation. |
| Local vs remote provider | `requirements.egress` | `local_only` or `remote_allowed`; requires enabled execution policy and correct classification. |
| Remote cost ceiling | `requirements.max_cost_usd` | Positive USD cap needs reviewed upper-bound support. `0` means no requested cap, not free. |
| Text vs JSON | `payload.output.mode` | `text` or `json`; see [json-job.json](json-job.json). |
| Required JSON fields | `payload.output.required_keys` | Checks key presence, not a complete schema, field types or factual accuracy. |
| Result size | `payload.output.max_bytes` | Text may be marked truncated; oversized JSON fails rather than becoming a valid-looking prefix. |
| Returned files | `payload.output.artifacts`, `max_artifact_bytes`, `min_artifacts`, `min_images`, `min_media` | Requires a capable provider/adapter; asking for files does not create that capability. |

For example, adding `"group": "private"` to `requirements` limits placement
to workers advertising that group. For confidential work, `local_only` means
an operator-classified local provider, **not** "never crossed the network" or
"the shared-hosting provider cannot see it". A local-looking provider name is
not proof that its configured URL stays local.

### Enable the operator policy before relying on it

The shipped example configuration has execution policy **disabled**. A
`policy.disabled` result is not proof that egress or cost restrictions were
enforced. For an intentionally Ollama-only relay, merge this into its existing
configuration, preserving unrelated settings and avoiding duplicate YAML keys:

```yaml
cluster:
  policies:
    execution:
      enabled: true
      local_providers: [ollama]
      remote_providers: []
      cost_bounded_providers: []
      default:
        egress: local_only
        allowed_providers: [ollama]
```

Review the actual Ollama endpoint on each worker before classifying it local.
Restart the affected service through its normal operator/service-manager
path, then validate/explain the job again. This example deliberately disallows
remote providers for every job covered by the default rule. It is a routing
policy, not an OS firewall or a sandbox around untrusted worker software.

Device owners can independently restrict `cluster.worker.allowed_providers`,
`allowed_models`, `allowed_tasks` and `max_concurrent` in the worker config.
That is separate from a producer asking for a group/model in a request.
Named multi-step agent authority and approval rules are documented separately
in [bounded agents](../../docs/bounded-agent.md); a normal text job does not
gain shell or filesystem privileges by mentioning an action in its prompt.

## 5. Text, files and output artifacts

| Input | Supported path today |
| --- | --- |
| Typed text | Instruction in `payload.prompt`; content in `payload.text`. |
| UTF-8 text files | The app reads an authorized, size-bounded file and supplies its text, not a path on the worker. |
| Several text files | Submit separate jobs, or combine selected text with clear file boundaries within the request and model limits. There is no generic `files[]` field. |
| One image | Legacy `payload.image_base64` + `payload.image_media_type`, or one entry in `payload.images`; plus matching vision requirements. |
| Several images | `payload.images[]` with `media_type` + `data_base64`; up to 12 images and 8 MiB decoded in aggregate. Set `requirements.input_image_count`, `input_image_bytes`, `input_image_max_bytes`, and `input_image_media_types` so the relay can preflight known model limits. |
| PDF / DOCX / spreadsheet | Extract text or convert selected pages in your application first, or use a separately implemented integration. The public native job is not a universal document-upload API. |
| Audio / video input | Do not confuse output-artifact support or advertised capabilities with a generic native audio/video upload endpoint. |

A real CLI image request, against a pool with a compatible Ollama vision model:

```sh
contextbridge cluster chat --provider ollama --model auto --attach-image ./invoice.png --artifacts off --prompt "Read the invoice date and total. Do not guess missing values."
```

Repeat `--attach-image` for a bounded comparison batch. The CLI derives the
routing evidence automatically; hand-built cluster contracts must declare it.

For web uploads, check application authorization, count, aggregate size,
encoding/type and allowed input paths before creating jobs. PHP's
[upload and request limits](https://www.php.net/manual/en/features.file-upload.common-pitfalls.php)
still apply; base64 makes the HTTP body larger than the decoded file. Keep the
whole request within the PHP client's 12 MiB limit. A path or URL in prompt
text does not authorize CB to read/fetch it. Uploaded content remains untrusted
model input; prompt instructions are not a prompt-injection security boundary.

Returned artifacts are a different channel from uploaded files. A capable
adapter can return up to 12 artifacts within the aggregate 12 MiB decoded-byte
limit (or a smaller requested limit). After a completed job,
`ContextBridgeClient::saveArtifacts($job, $privateDirectory)` verifies embedded
bytes and saves without overwriting existing files. URL-only references are
returned separately and not fetched by this helper. Keep saved outputs outside
the web root and expose them only through authorized downloads.

See [exact limits](../../docs/limits-and-performance.md),
[other integrations](../../docs/integrations.md), and the
[implementation's job fields](../../internal/bridge/types.go).
