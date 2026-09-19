# Limits and measured ContextBridge overhead

ContextBridge separates protocol limits from provider limits. A browser AI may
choose to create fewer images, refuse a request, or impose its own quota. The
limits below describe what ContextBridge accepts and verifies after the
provider has responded.

## Images and artifacts

| Boundary | ContextBridge behavior |
| --- | --- |
| Visual input | One input image per job, up to exactly 8 MiB after base64 decoding. The CLI flag is singular: `--attach-image`. |
| Generated series | Up to 12 response artifacts total. `min_images`, `min_media`, and `min_artifacts` each accept 0–12. |
| Embedded response bytes | 12 MiB maximum in aggregate across the whole response, not 12 MiB per image. `max_artifact_bytes` may lower that shared budget to 1 KiB–12 MiB. |
| Text result | 64 KiB by default and up to 1 MiB when requested. A longer result is UTF-8 safely bounded and explicitly returns `truncated: true`. |
| Cluster job/result transport | 12 MiB cleartext job payload and 24 MiB cleartext result envelope. E2EE wire expansion does not reduce those cleartext budgets. |

For example, three generated images of 4 MiB each fit exactly. Three images
whose decoded sizes total 12 MiB plus one byte do not. If a job requires all
three with `min_images: 3`, an over-budget third image makes the requirement
fail as a whole: the normalized failure contains neither a successful answer
prefix nor a partial artifact list. Exact and one-byte-over series boundaries,
the 12/13 count boundary, and this atomic failure behavior are regression
tested in `internal/bridge/artifact_series_test.go`.

Without a minimum requirement, ContextBridge may still return the valid
bounded subset and safe HTTPS references for resources it could not embed.
That is intentional best-effort collection, not proof that every requested
file was transferred. Use `--image`, `--min-images N`, or `--min-artifacts N`
when downstream work must not start from a partial series.

Artifact size and count limits apply to the combined response. They are not
reset for each image type, each download control, or each model request inside
one job. Images and audio/video are disjoint types, so their two typed minimums
may not add up to more than 12; impossible combinations are rejected before
dispatch. `min_artifacts` may overlap those typed minimums. Separate jobs
receive separate budgets.

## Text and JSON integrity at the byte boundary

Limits are bytes, not characters. Plain-text output accepts exactly 64 KiB at
the default and exactly 1 MiB when that maximum is requested. One byte beyond
either limit is returned only as a bounded prefix with `truncated: true`.
Truncation walks back to a valid UTF-8 boundary, so a split emoji or other
multibyte character is never returned as corrupt text.

JSON deliberately behaves differently: ContextBridge never turns an oversized
object into a syntactically valid-looking prefix. Exact-limit JSON is accepted;
one byte over returns `invalid_json`, with no JSON value, text prefix, artifact
subset, or truncation flag presented as success. The default and 1 MiB text
boundaries are covered in `internal/bridge/types_test.go`; emoji safety and
exact/+1 fail-closed JSON are covered in
`internal/bridge/output_boundaries_test.go`.

## Reproducible overhead benchmark

### One auditable release-binary report

Run the built-in measurement when comparing releases or hardware:

```bash
contextbridge benchmark
contextbridge benchmark --json > contextbridge-performance.json
```

The default command performs 128 measured samples after eight untimed warmup samples
at each concurrency level **1, 4, 16, and 64**. It reports nearest-rank p50,
p95, and p99 latency for every logical operation plus throughput over the
complete concurrent sample window. The JSON document records the executable
version, OS, architecture, Go version, logical CPU count, settings, paths,
scopes, exclusions, and warnings. Use `--samples`, `--warmup`,
`--idle-duration`, `--database-jobs`, `--binary`, or `--extension-root` to make
an intentional alternate run; keep those settings with any quoted result. If a
custom sample count is below a concurrency level, that level is omitted rather
than mislabeled—for example, `--samples 4` reports only 1 and 4.
Sub-millisecond crypto and artifact rows batch 16 real operations inside each
timed sample, divide the elapsed time by 16 for per-operation latency, and use
all completed operations for throughput. Queue rows use a batch of one. The
`operations_per_sample` JSON field and `BATCH` table column make this explicit.
Very small sample counts can complete within the host clock's resolution; in
that case the report keeps a zero latency or throughput value instead of
emitting a non-JSON infinity. Increase `--samples` for a publishable run.

The three timed operations are deliberately narrow:

- `relay_queue_submit_read_cancel` starts a fresh isolated relay in the same
  benchmark process and a fresh Bolt database for each concurrency. One sample
  includes client JSON encoding,
  three authenticated loopback HTTP exchanges, strict decoding, durable job
  admission, compact readback, and durable cancellation.
- `e2ee_small_job_and_result` creates and opens a small X25519/AES-256-GCM job
  envelope and then seals and opens its response. It includes fresh ephemeral
  cryptographic randomness for each sample.
- `artifact_verification_64kib` runs the production artifact normalization path
  for one deterministic 64 KiB file: base64 decode, media and size policy, and
  SHA-256 recomputation. It excludes provider generation and download time; the
  separate dated boundary benchmark below retains the exact 12 MiB case.

Concurrency here means simultaneous **client operations**, not AI slots.
Bolt's durable write transactions remain serialized by design, so the queue
throughput rows are useful capacity observations rather than a promise of
linear scaling. Sixty-four is the protocol's maximum worker concurrency and
is a stress point, not a recommendation for every machine.

The same report measures resource footprint without contacting a provider:

- The running executable is measured as a regular file. Chromium, Firefox,
  and shared extension source trees are summed from regular files without
  following symlinks. An installed release normally auto-detects its extension
  directory; an unusual layout must pass `--extension-root` and receives a
  warning rather than a made-up zero.
- A fresh relay is hosted inside the benchmark process, warmed through its
  health endpoint, and left idle for two seconds by default. Whole-process CPU
  time is shown both as a
  percentage of one core and normalized across the host's logical CPUs. Linux
  reports current RSS, Windows reports the current working set, and macOS
  explicitly labels `ru_maxrss` as peak RSS. Go heap and OS-reserved runtime
  memory are separate fields. This is the footprint of the **benchmark CLI
  process while it hosts one fresh idle relay**, not a separately spawned
  relay, and not the RAM consumption of Ollama, a browser, a model, or a full
  multi-role production deployment.
- Database growth uses a new Bolt file, records its allocated size, creates and
  cancels 1,000 small jobs, syncs, and records the resulting file size. The
  report includes raw growth, bytes per job, and normalized bytes per 1,000
  jobs. Bolt reuses freed pages and does not compact on delete, so this is a
  reproducible fresh-file observation, not a forecast for every retained job
  mix or an existing database.
- Heartbeat rows serialize fixed, typed representative payloads: one idle
  browser tab, and one worker with one GPU and one model. `bytes/minute` and
  `bytes/hour` are the JSON application payload at the normal five-second
  interval. They exclude
  HTTP/WebSocket, TLS, and TCP framing; actual bytes scale with attached tabs,
  bounded diagnostics, GPUs, models, capabilities, reconnects, and configured
  intervals. The payload names and scopes in JSON prevent these fixtures from
  being mistaken for captured user traffic.

The command creates only temporary databases and loopback listeners, removes
them afterward, and never attaches a tab, submits an AI prompt, starts a model,
or contacts the Internet. Regression tests validate arithmetic, schema,
ordering, bounds, and operation completion; they intentionally assert no
latency, throughput, CPU, or RAM threshold because shared CI performance is
not stable enough to make such thresholds honest.

The JSON and table also list metrics that this bounded workload cannot report
honestly: RAM per active inference job, operating-system disk-write bytes per
job, and complete wire bytes per job. Browser/model memory cannot be assigned
to one bridge job without starting inference; Bolt file growth is not the same
as filesystem/device writes; and job bytes vary with caller payloads, results,
and transport framing. These remain explicit `unavailable_metrics` rather than
silent omissions or fabricated zeros.

### v0.5.80 release-binary snapshot

The following tables are direct output observations from the published-shape
v0.5.80 `windows/amd64` and `linux/amd64` binaries built from the same tree on
2026-09-19. Both used the default 128 measured samples, eight warmups, 16 real
operations per crypto/artifact sample, 1,000 cancelled jobs for database
growth, and an intentional ten-second idle window (rather than the two-second
default) for more useful OS process sampling. Windows was the 24-logical-CPU Intel Core
i9-12900K workstation; Linux was the two-logical-CPU AMD EPYC 9354P VPS
allocation. They are one dated host observation, not an SLA or a claim of
linear scaling. The unrounded machine-readable reports are attached to the
v0.5.80 GitHub release as `contextbridge_benchmark_windows_amd64.json` and
`contextbridge_benchmark_linux_amd64.json`; both identify the exact version,
settings, binary and extension sizes, scopes, exclusions, and warnings.

#### Latency and throughput — Windows/amd64

| Operation | Concurrency | p50 | p95 | p99 | operations/s |
| --- | ---: | ---: | ---: | ---: | ---: |
| Durable relay submit/read/cancel | 1 | 2.041 ms | 3.596 ms | 4.097 ms | 463.8 |
| Durable relay submit/read/cancel | 4 | 7.621 ms | 9.253 ms | 9.839 ms | 538.5 |
| Durable relay submit/read/cancel | 16 | 30.363 ms | 32.962 ms | 33.407 ms | 520.8 |
| Durable relay submit/read/cancel | 64 | 136.194 ms | 138.514 ms | 138.875 ms | 463.6 |
| Small E2EE job + result | 1 | 0.108 ms | 0.169 ms | 0.193 ms | 8,702.7 |
| Small E2EE job + result | 4 | 0.127 ms | 0.169 ms | 0.215 ms | 30,121.7 |
| Small E2EE job + result | 16 | 0.201 ms | 0.585 ms | 0.698 ms | 52,003.3 |
| Small E2EE job + result | 64 | 0.923 ms | 2.021 ms | 2.108 ms | 53,931.9 |
| Verify one 64 KiB artifact | 1 | 0.065 ms | 0.114 ms | 0.136 ms | 13,899.0 |
| Verify one 64 KiB artifact | 4 | 0.095 ms | 0.129 ms | 0.149 ms | 41,700.0 |
| Verify one 64 KiB artifact | 16 | 0.201 ms | 0.339 ms | 0.382 ms | 71,782.3 |
| Verify one 64 KiB artifact | 64 | 0.593 ms | 1.149 ms | 1.335 ms | 82,903.9 |

#### Latency and throughput — Linux/amd64 VPS

| Operation | Concurrency | p50 | p95 | p99 | operations/s |
| --- | ---: | ---: | ---: | ---: | ---: |
| Durable relay submit/read/cancel | 1 | 4.303 ms | 14.282 ms | 18.953 ms | 178.4 |
| Durable relay submit/read/cancel | 4 | 16.180 ms | 25.881 ms | 35.126 ms | 234.3 |
| Durable relay submit/read/cancel | 16 | 85.790 ms | 121.012 ms | 128.724 ms | 175.5 |
| Durable relay submit/read/cancel | 64 | 550.576 ms | 638.155 ms | 643.871 ms | 110.7 |
| Small E2EE job + result | 1 | 0.227 ms | 0.295 ms | 0.306 ms | 4,253.7 |
| Small E2EE job + result | 4 | 0.258 ms | 1.228 ms | 1.586 ms | 7,415.5 |
| Small E2EE job + result | 16 | 0.251 ms | 4.102 ms | 10.145 ms | 7,542.4 |
| Small E2EE job + result | 64 | 0.247 ms | 4.889 ms | 8.399 ms | 7,856.1 |
| Verify one 64 KiB artifact | 1 | 0.127 ms | 0.246 ms | 0.344 ms | 6,987.4 |
| Verify one 64 KiB artifact | 4 | 0.265 ms | 0.596 ms | 0.746 ms | 12,755.8 |
| Verify one 64 KiB artifact | 16 | 0.773 ms | 2.218 ms | 2.965 ms | 14,175.4 |
| Verify one 64 KiB artifact | 64 | 2.389 ms | 7.016 ms | 8.036 ms | 13,318.6 |

#### Resource footprint

| Measurement | Windows/amd64 | Linux/amd64 VPS | Scope |
| --- | ---: | ---: | --- |
| Core binary | 10.2 MiB | 9.9 MiB | stripped release executable |
| Chromium extension | 684.0 KiB | 684.0 KiB | regular packaged files |
| Firefox extension | 684.2 KiB | 684.2 KiB | regular packaged files |
| Benchmark process + idle relay CPU | 0.000% observed | 0.082% of one core | whole-process, ten-second warmed window; Windows was below counter resolution |
| Benchmark process + idle relay resident memory | 13.9 MiB | 10.5 MiB | whole-process working set / current RSS |
| Benchmark process + idle relay Go heap / reserved | 1.0 MiB / 11.1 MiB | 751.3 KiB / 6.6 MiB | same benchmark process |
| Fresh Bolt growth / 1,000 jobs | 2.0 MiB | 2.0 MiB | small submitted then cancelled jobs |
| Browser heartbeat | 544 B / interval; 6,528 B/min; 382.5 KiB/hour | same typed fixture | JSON payload, one idle tab |
| Worker heartbeat | 704 B / interval; 8,448 B/min; 495.0 KiB/hour | same typed fixture | JSON payload, one GPU + one model |

The benchmark deliberately reports `RAM per active inference job`, operating-
system disk-write bytes per job, and complete wire bytes per job as
**unavailable**. Assigning browser/model process memory to one job, equating a
Bolt file allocation with device writes, or pretending every caller sends the
same payload would produce attractive but false numbers.

### v0.5.80 development end-to-end pool snapshot

The following is a separate live routing observation from 2026-09-19. Unlike
the bridge-only tables above, it includes the VPS HTTPS relay, a remote Windows
worker, a hot-plug ModelKit Ollama endpoint, model loading, and inference. It is
therefore useful product evidence but not a ContextBridge overhead benchmark or
an SLA.

| Flow | Result | Observed completion time |
| --- | --- | --- |
| VPS → relay → Windows worker → ModelKit `qwen2.5:latest`, eight concurrent exact-marker jobs | 8/8 completed with the exact requested marker | 2.3–18.2 s, including queue/model work |
| Offline toolbox → local OpenAI-compatible ingress → ContextBridge → ModelKit `qwen2.5:latest`, twelve jobs at concurrency four | 12/12 completed with the exact requested marker | warm responses about 0.69–0.73 s; first cold group 5.7–6.2 s |
| PHP 8.1 shared-hosting client → public HTTPS relay → Windows worker → ModelKit | completed with `PHP-POOL-OK` | 0.298 s provider latency on the warm live run |

The exact-marker checks prove routing, transport, provider selection, and full
result return for a deliberately narrow task; they do not establish general
model quality. A smaller 1.5B model returned all twelve responses but copied
only 3/12 markers exactly, which is why transport success and answer quality
are reported separately. A second candidate was rejected after 0/12 compliant
wrapped responses. `qwen2.5:latest` became the measured local default only
after the full wrapper path passed 12/12.

Portable discovery was also repeated 100 times with 100/100 stable pack IDs.
The two marker SHA-256 values were unchanged afterwards, and the toolbox's own
read-only release verifier matched all 847 golden entries with no issue. This
tests passive discovery; it does not claim that unplugging a mounted drive is
safe while another application is actively using it.

### Dated development snapshot

The benchmarks below measure ContextBridge code only. No Ollama model,
ChatGPT/Gemini tab, provider request, Internet request, or inference time is in
the timed region. Results are a dated machine snapshot, not an SLA.

Method, 2026-09-15:

- Go 1.25.0, `windows/amd64`, Intel Core i9-12900K (24 logical CPUs).
- Go 1.25.0, `linux/amd64`, AMD EPYC 9354P VPS allocation (`-2`, two logical CPUs).
- Small benchmarks: two-second adaptive runs, repeated five times; table shows
  the median `ns/op`.
- Large fixed-boundary benchmarks: three operations per run, repeated five
  times; table shows the median.
- The relay queue cycle performs three authenticated loopback HTTP requests:
  durable submit, compact readback, and cancellation. It includes JSON, auth,
  networking, and Bolt persistence, but no worker execution.
- The small E2EE cycle seals and opens both a job and its result. The 8 MiB E2EE
  row seals and opens the exact maximum visual payload.
- Artifact normalization decodes base64, checks policy and size, and recomputes
  SHA-256 for one exact 12 MiB artifact. It excludes download time.
- The idle row is an already-warmed scheduler scan after the adaptive
  maintenance change. Production sleeps between scans and separately performs
  bounded maintenance at its lower cadence.

| ContextBridge-only operation | Windows median | Linux VPS median |
| --- | ---: | ---: |
| Authenticated relay queue submit + read + cancel | 2.09 ms | 3.70 ms |
| Small E2EE job + result cryptographic round trip | 0.124 ms | 0.241 ms |
| Warm idle dispatch scan | 0.445 µs | 1.16 µs |
| Decode, validate, and hash exact 12 MiB artifact | 13.6 ms (926 MB/s) | 22.8 ms (552 MB/s) |
| Seal + open exact 8 MiB E2EE payload | 117.7 ms (71 MB/s) | 37.6 ms (223 MB/s) |

Run the same measurements on your hardware:

```bash
go test ./internal/cluster -run '^$' \
  -bench '^(BenchmarkContextBridgeRelayQueueRoundTrip|BenchmarkContextBridgeE2EESmallTextRoundTrip|BenchmarkContextBridgeIdleDispatchTick)$' \
  -benchmem -benchtime=2s -count=5

go test ./internal/bridge -run '^$' \
  -bench '^BenchmarkContextBridgeNormalize12MiBArtifact$' \
  -benchmem -benchtime=3x -count=5

go test ./internal/cluster -run '^$' \
  -bench '^BenchmarkContextBridgeE2EE8MiBPayload$' \
  -benchmem -benchtime=3x -count=5
```

The honest headline is therefore not “AI answers in two milliseconds.” It is:
on these two machines, the measured ContextBridge queue and small-message
cryptographic work was in the microsecond-to-low-millisecond range before any
provider work began. Provider latency remains visible separately in normal job
metrics.

## Why the ChatGPT → VPS → Gemini demo is useful

The image demo is a strong communication test because it proves a complete,
inspectable chain: a VPS submits a job, a remote Windows browser creates real
bytes, ContextBridge saves those bytes back on the VPS, and a different
provider receives that exact saved file. The smallest worthwhile improvement
is to print the image's SHA-256 before the Gemini step and the same SHA-256 in
the job metadata or narration. That makes “the same artifact crossed the
bridge” visually verifiable without adding another AI task to the demo.
