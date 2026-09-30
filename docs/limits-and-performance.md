# Limits and measured ContextBridge overhead

ContextBridge separates protocol limits from engine and provider limits. A
model may refuse a request, return fewer artifacts, or impose a smaller quota.
The boundaries below describe what the public core accepts and verifies.

## Payload and result boundaries

| Boundary | Core behavior |
| --- | --- |
| Input images | Up to 12 PNG/JPEG/WebP/GIF inputs per job, sharing exactly 8 MiB after base64 decoding. The legacy singular field remains accepted but cannot be mixed with `images[]`. |
| Speech input | One complete Ogg/Opus recording for a `speech_to_text` job; 8 MiB and five minutes maximum. Ogg checksums, page order, Opus headers and duration evidence are verified before dispatch. Remote audio URLs are never fetched. |
| Response artifacts | Up to 12 verified artifacts, sharing one aggregate decoded-byte budget. |
| Embedded artifact bytes | 12 MiB maximum in aggregate; `max_artifact_bytes` may lower it to 1 KiB–12 MiB. |
| Text result | 64 KiB by default; callers may request 256 B–1 MiB. Oversize text is UTF-8-safely bounded and marked `truncated: true`. |
| JSON result | Never exposed as a valid-looking truncated prefix. Oversize or invalid JSON fails validation. |
| Cleartext cluster job | 12 MiB maximum. Encryption overhead does not reduce this cleartext budget. |
| Cleartext cluster result | 24 MiB maximum, with a separate bounded encrypted wire envelope. |
| Worker concurrency | 64 advertised slots maximum per worker. |

Typed minimums (`min_images`, `min_media`, and `min_artifacts`) accept 0–12.
Images and other media are disjoint, so `min_images + min_media` cannot exceed
12. `min_artifacts` may overlap them. If a required minimum cannot be satisfied,
the normalized result fails as a whole instead of presenting a partial artifact
set as success.

Limits are bytes, not characters. Text truncation walks backward to a valid
UTF-8 boundary. Artifact normalization rechecks decoded size, media type where
supported, and SHA-256. Exact-limit and one-byte-over behavior is covered by
unit and integration tests in `internal/bridge` and `internal/cluster`.

## Reproduce the bridge-only benchmark

```sh
contextbridge benchmark
contextbridge benchmark --json > contextbridge-performance.json
```

The default run uses 128 measured samples after eight warmups at concurrency
1, 4, 16, and 64. It measures three narrow operations:

- `relay_queue_submit_read_cancel`: authenticated loopback HTTP, JSON
  encode/decode, durable admission, compact readback, and durable cancellation
  against a fresh isolated Bolt database;
- `e2ee_small_job_and_result`: fresh X25519/AES-256-GCM job envelope plus a
  sealed/opened response;
- `artifact_verification_64kib`: base64 decode, media/size policy, and SHA-256
  verification through the production normalization path.

Crypto and artifact samples batch 16 real operations and report per-operation
latency. Queue samples batch one. Concurrency means simultaneous client
operations, not AI slots. Durable Bolt writes serialize by design, so the
64-client row is a stress observation rather than a promise of linear scaling.

The command also measures the running executable, a fresh relay hosted inside
the benchmark process, and fixed typed heartbeat payloads. Database reporting
separates three different quantities: Bolt's stepwise allocated file size,
live bytes occupied by branch/leaf bucket records, and cumulative transaction
page allocation. Only live bucket growth is normalized per job; a 4 MiB to
8 MiB file-allocation jump is capacity evidence, not proof that each record
doubled. It creates temporary databases and loopback listeners, removes them
afterward, sends no model request, and contacts no Internet host.

JSON benchmark schema 2 introduced the explicit
`allocated_file_*`, `live_bucket_*`, and `transaction_page_*` fields. Older
published schema-1 reports remain immutable evidence; their `growth_*` fields
describe allocated-file growth and must not be interpreted as logical bytes
retained per job.

The JSON report records the measured executable's base name and byte size, not
its absolute local path. This keeps a publishable report from disclosing a
workstation username or checkout layout.

## Dated Windows/amd64 current desktop snapshot

This snapshot was measured on 2026-09-28 from source commit
`2b32824c3ba570e756bcd0504edeb8a9e0472a33`, after removing an unnecessary
empty durable transaction from queued-job cancellation. The binary was built
with Go 1.27.0 for Windows/amd64 using `-trimpath -buildvcs=true -ldflags "-s
-w -X main.version=v0.7.0-dev"`. The host was an Acer Predator PO7-640 with
an Intel Core i9-12900K (16 physical cores and 24 logical CPUs), 32 GiB of
RAM, and the Windows High performance power scheme.

Five sequential reports used 128 measured samples, eight warmups, concurrency
1/4/16/64, 1,000 cancelled jobs, and a ten-second idle window. Run 1 is shown
because it had the smallest summed normalized deviation from the five-run
median across every operation/concurrency p50, p95, p99, and throughput value.
The [complete reports, selection details, schema-2 database evidence, and
same-toolchain alternating A/B](benchmarks/2026-09-28-windows-i9-12900k-2b32824/)
are published together.

Normal workstation background activity remained enabled. The benchmark uses
no model/provider inference and no GPU. This is one workstation observation,
not an SLA.

### Durable relay submit/read/cancel

| Clients | p50 | p95 | p99 | Throughput |
| ---: | ---: | ---: | ---: | ---: |
| 1 | 2.547 ms | 5.071 ms | 10.177 ms | 389.6 ops/s |
| 4 | 8.767 ms | 10.661 ms | 11.616 ms | 464.1 ops/s |
| 16 | 38.956 ms | 44.761 ms | 47.643 ms | 407.4 ops/s |
| 64 | 144.467 ms | 147.382 ms | 149.466 ms | 435.5 ops/s |

### Small E2EE job and result

| Clients | p50 | p95 | p99 | Throughput |
| ---: | ---: | ---: | ---: | ---: |
| 1 | 0.128 ms | 0.181 ms | 0.244 ms | 8,047.2 ops/s |
| 4 | 0.125 ms | 0.218 ms | 0.446 ms | 28,272.8 ops/s |
| 16 | 0.221 ms | 0.648 ms | 0.904 ms | 46,982.2 ops/s |
| 64 | 0.990 ms | 2.308 ms | 2.441 ms | 46,511.2 ops/s |

### Verify one 64 KiB artifact

| Clients | p50 | p95 | p99 | Throughput |
| ---: | ---: | ---: | ---: | ---: |
| 1 | 0.065 ms | 0.131 ms | 0.137 ms | 13,736.3 ops/s |
| 4 | 0.095 ms | 0.136 ms | 0.162 ms | 43,698.9 ops/s |
| 16 | 0.239 ms | 0.372 ms | 0.398 ms | 62,842.3 ops/s |
| 64 | 0.632 ms | 1.333 ms | 1.459 ms | 81,190.9 ops/s |

### Resource footprint

| Measurement | Observation | Scope |
| --- | ---: | --- |
| Stripped core binary | 13.3 MiB | Built executable only |
| Idle benchmark process + relay RSS | 15.4 MiB | Current Windows working set after warmup |
| Go heap allocated / runtime reserved | 1.4 MiB / 11.8 MiB | Same process and sample window |
| Idle CPU | below sampling resolution | No process CPU tick recorded during the 10 s window; not claimed as literal zero |
| Bolt allocated-file growth | 8.0 MiB / 1,000 jobs | Stepwise capacity growth; deliberately not normalized per job |
| Bolt live bucket growth | 1.60 MiB / 1,000 jobs | Branch/leaf bytes occupied by retained cancellation evidence |
| Generic adapter status payload | 245 B / heartbeat; 172.3 KiB/hour | One idle endpoint at 5 s; application JSON only |
| Worker heartbeat payload | 728 B / heartbeat; 511.9 KiB/hour | One GPU and one model at 5 s; WebSocket JSON only |

Concurrency-1 durable queue throughput across runs 1 through 5 was
389.6/379.5/399.4/360.1/395.9 ops/s. A controlled prior/fixed ABBA sequence,
using Go 1.27.0 and identical flags for both binaries, measured median
throughput gains of 22.0% at one client and 10.9% at 64 clients. The fix skips
a recovery-probe write transaction only when the job has no assigned node and
therefore cannot own such a probe; no durability or recovery evidence was
removed.

## Dated Windows/amd64 pre-fix desktop snapshot

This snapshot was measured on 2026-09-28 from source commit
`0c5812f25b872b14dd58e3e0d3e4ac4847340754`. The binary was built with Go
1.27.1 for Windows/amd64 using `-trimpath -buildvcs=true -ldflags "-s -w -X
main.version=v0.7.0-dev"`. The host was an Acer Predator PO7-640 desktop with
an Intel Core i9-12900K (16 physical cores and 24 logical CPUs), 32 GiB of
DDR5 memory configured at 4000 MT/s, an NVIDIA GeForce RTX 3080, and Windows
11 Home build 26200. The active power scheme was `High performance`.

Five sequential reports used 128 measured samples, eight warmups, concurrency
1/4/16/64, 1,000 cancelled jobs for database growth, and a ten-second idle
window. The first three were the planned set; two diagnostic repeats were
added after run 2 showed a visible durable-queue outlier. Run 3 is shown below
because it had the smallest summed normalized deviation from the five-run
median across every operation/concurrency p50, p95, p99, and throughput value.
The [complete reports and selection details](benchmarks/2026-09-28-windows-i9-12900k/)
are published together.

Normal workstation background services remained active. No process was
stopped to improve the result, and host load was not controlled. The benchmark
performs no model inference and does not use the GPU. This is one workstation
observation, not an SLA or an isolated comparison with an older snapshot.

### Durable relay submit/read/cancel

| Clients | p50 | p95 | p99 | Throughput |
| ---: | ---: | ---: | ---: | ---: |
| 1 | 3.075 ms | 4.468 ms | 5.247 ms | 315.3 ops/s |
| 4 | 10.893 ms | 13.410 ms | 14.771 ms | 369.3 ops/s |
| 16 | 48.381 ms | 50.971 ms | 51.243 ms | 330.1 ops/s |
| 64 | 180.682 ms | 194.423 ms | 197.553 ms | 339.7 ops/s |

### Small E2EE job and result

| Clients | p50 | p95 | p99 | Throughput |
| ---: | ---: | ---: | ---: | ---: |
| 1 | 0.127 ms | 0.192 ms | 0.223 ms | 7,796.0 ops/s |
| 4 | 0.128 ms | 0.194 ms | 0.230 ms | 28,804.2 ops/s |
| 16 | 0.291 ms | 0.545 ms | 0.673 ms | 49,753.5 ops/s |
| 64 | 0.815 ms | 1.971 ms | 2.285 ms | 50,062.7 ops/s |

### Verify one 64 KiB artifact

| Clients | p50 | p95 | p99 | Throughput |
| ---: | ---: | ---: | ---: | ---: |
| 1 | 0.089 ms | 0.195 ms | 0.405 ms | 11,240.4 ops/s |
| 4 | 0.096 ms | 0.193 ms | 0.556 ms | 34,508.6 ops/s |
| 16 | 0.268 ms | 0.399 ms | 0.513 ms | 55,349.4 ops/s |
| 64 | 0.790 ms | 1.439 ms | 1.857 ms | 65,436.3 ops/s |

### Resource footprint

| Measurement | Observation | Scope |
| --- | ---: | --- |
| Stripped core binary | 13.3 MiB | Built executable only |
| Idle benchmark process + relay RSS | 15.3 MiB | Current Windows working set after warmup |
| Go heap allocated / runtime reserved | 1.4 MiB / 15.8 MiB | Same process and sample window |
| Idle CPU | below sampling resolution | No process CPU tick recorded during the 10 s window; not claimed as literal zero |
| Fresh Bolt allocation growth | 8.0 MiB / 1,000 jobs | 1,000 small jobs created and cancelled; allocated file growth, not device writes |
| Generic adapter status payload | 245 B / heartbeat; 172.3 KiB/hour | One idle endpoint at 5 s; application JSON only |
| Worker heartbeat payload | 728 B / heartbeat; 511.9 KiB/hour | One GPU and one model at 5 s; WebSocket JSON only |

Across runs 1 through 5, concurrency-1 durable queue throughput was
297.9/173.3/315.3/318.2/302.4 ops/s and idle RSS was
15.4/15.3/15.3/15.3/15.3 MiB. All five runs completed without a benchmark
warning. The outlier is retained rather than silently averaged away.

## Dated Windows/amd64 historical development snapshot

This snapshot was measured on 2026-09-20 from source commit
`a54ff6f9c8ce69a993a5960a73c51698ffdec330`, before documentation-only changes
on this branch. The binary was built with Go 1.25.14 for Windows/amd64 using
`-trimpath -ldflags "-s -w"` and version label `v0.7.0-dev`. The host was
Windows 11 build 26200 with an Intel Core i9-12900K and 24 logical CPUs.

Settings: 128 measured samples, eight warmups, concurrency 1/4/16/64, 1,000
cancelled jobs for database growth, and a ten-second idle window. This is one
host observation, not an SLA.

### Durable relay submit/read/cancel

| Clients | p50 | p95 | p99 | Throughput |
| ---: | ---: | ---: | ---: | ---: |
| 1 | 2.402 ms | 3.516 ms | 3.740 ms | 418.5 ops/s |
| 4 | 7.704 ms | 9.381 ms | 10.118 ms | 517.1 ops/s |
| 16 | 31.334 ms | 34.438 ms | 34.946 ms | 499.9 ops/s |
| 64 | 135.273 ms | 136.983 ms | 138.319 ms | 466.8 ops/s |

### Small E2EE job and result

| Clients | p50 | p95 | p99 | Throughput |
| ---: | ---: | ---: | ---: | ---: |
| 1 | 0.098 ms | 0.168 ms | 0.193 ms | 8,582.9 ops/s |
| 4 | 0.125 ms | 0.190 ms | 0.215 ms | 31,809.2 ops/s |
| 16 | 0.137 ms | 0.742 ms | 1.149 ms | 55,590.8 ops/s |
| 64 | 0.779 ms | 2.130 ms | 2.196 ms | 50,719.9 ops/s |

### Verify one 64 KiB artifact

| Clients | p50 | p95 | p99 | Throughput |
| ---: | ---: | ---: | ---: | ---: |
| 1 | 0.065 ms | 0.125 ms | 0.134 ms | 13,834.8 ops/s |
| 4 | 0.096 ms | 0.157 ms | 0.180 ms | 41,037.6 ops/s |
| 16 | 0.229 ms | 0.299 ms | 0.327 ms | 65,106.2 ops/s |
| 64 | 0.605 ms | 0.971 ms | 1.066 ms | 88,210.8 ops/s |

### Resource footprint

| Measurement | Observation | Scope |
| --- | ---: | --- |
| Stripped core binary | 10.6 MiB | Built executable only |
| Idle benchmark process + relay RSS | 14.4 MiB | Current Windows working set after warmup |
| Go heap allocated / runtime reserved | 1.0 MiB / 11.1 MiB | Same process and sample window |
| Idle CPU | below sampling resolution | No process CPU tick recorded during the 10 s window; not claimed as literal zero |
| Fresh Bolt allocation growth | 4.0 MiB / 1,000 jobs | 1,000 small jobs created and cancelled; allocated file growth, not device writes |
| Generic adapter status payload | 245 B / heartbeat; 172.3 KiB/hour | One idle endpoint at 5 s; application JSON only |
| Worker heartbeat payload | 704 B / heartbeat; 495.0 KiB/hour | One GPU and one model at 5 s; WebSocket JSON only |

Heartbeat figures exclude HTTP/WebSocket, TLS, and TCP framing and scale with
advertised endpoints, diagnostics, GPUs, models, capabilities, reconnects, and
configured intervals. Bolt reuses pages and does not compact on deletion, so
fresh-file growth is not a forecast for every retention mix.

## Dated Windows/amd64 current laptop snapshot

This snapshot was measured on 2026-09-28 from exact clean source commit
`3856f26f5366b1a93ee0ce25a81de640c8101a88`. The binary was built with Go
1.27.1 for Windows/amd64 using `-trimpath -buildvcs=true -ldflags "-s -w -X
main.version=v0.8.0-dev"`; embedded provenance confirmed the commit and
`vcs.modified=false`. The host was a Samsung 750XGK laptop with an Intel Core
i7-1355U (10 physical cores and 12 logical CPUs), 16 GB of RAM, and Windows 11
Home build 26200. It was on AC power at 98% battery with the active `Samsung
Mode` power scheme.

Three sequential runs used 128 measured samples, eight warmups, concurrency
1/4/16/64, 1,000 cancelled jobs, and a ten-second idle window. Run 1 is shown
for operation timings because it had the smallest summed normalized deviation
from the three-run median across every operation/concurrency p50, p95, p99,
and throughput value. The [complete raw reports and selection
details](benchmarks/2026-09-28-windows-i7-1355u-3856f26/) are published
together.

### Durable relay submit/read/cancel

| Clients | p50 | p95 | p99 | Throughput |
| ---: | ---: | ---: | ---: | ---: |
| 1 | 10.525 ms | 12.171 ms | 12.568 ms | 94.9 ops/s |
| 4 | 39.906 ms | 42.902 ms | 45.904 ms | 99.1 ops/s |
| 16 | 159.220 ms | 162.185 ms | 162.357 ms | 100.1 ops/s |
| 64 | 650.943 ms | 659.167 ms | 659.988 ms | 97.6 ops/s |

### Small E2EE job and result

| Clients | p50 | p95 | p99 | Throughput |
| ---: | ---: | ---: | ---: | ---: |
| 1 | 0.125 ms | 0.158 ms | 0.188 ms | 8,655.5 ops/s |
| 4 | 0.189 ms | 0.344 ms | 0.375 ms | 18,942.4 ops/s |
| 16 | 0.497 ms | 0.855 ms | 1.025 ms | 26,075.1 ops/s |
| 64 | 1.997 ms | 4.323 ms | 4.556 ms | 25,655.6 ops/s |

### Verify one 64 KiB artifact

| Clients | p50 | p95 | p99 | Throughput |
| ---: | ---: | ---: | ---: | ---: |
| 1 | 0.067 ms | 0.126 ms | 0.163 ms | 11,940.0 ops/s |
| 4 | 0.125 ms | 0.209 ms | 0.220 ms | 28,311.5 ops/s |
| 16 | 0.445 ms | 0.646 ms | 0.718 ms | 33,942.7 ops/s |
| 64 | 1.135 ms | 2.059 ms | 2.302 ms | 46,430.0 ops/s |

### Resource footprint

| Measurement | Observation | Scope |
| --- | ---: | --- |
| Stripped core binary | 13.3 MiB | Built executable only |
| Idle benchmark process + relay RSS | 15.1 MiB | Three-run median current Windows working set after warmup |
| Go heap allocated / runtime reserved | 1.2 MiB / 11.6 MiB | Three-run median from the same sample window |
| Idle CPU | below sampling resolution | No process CPU tick recorded during any 10 s window; not claimed as literal zero |
| Bolt allocated-file growth | 8.0 MiB / 1,000 jobs | Stepwise capacity growth; deliberately not normalized per job |
| Bolt live bucket growth | 1.60 MiB / 1,000 jobs | Branch/leaf bytes occupied by retained cancellation evidence |
| Bolt transaction page allocation | 100.6 MiB / 1,000 jobs | Cumulative pages allocated by write transactions, not retained database size or device writes |
| Generic adapter status payload | 245 B / heartbeat; 172.3 KiB/hour | One idle endpoint at 5 s; application JSON only |
| Worker heartbeat payload | 728 B / heartbeat; 511.9 KiB/hour | One GPU and one model at 5 s; WebSocket JSON only |

Concurrency-1 durable queue throughput across runs 1/2/3 was
94.91/94.59/94.72 ops/s. Its median was 24.5% above the 76.09 ops/s median of
the prior laptop snapshot. This is a cross-commit observation, not a controlled
attribution. Idle RSS was 55.8/15.1/15.0 MiB: the timing-selection algorithm
does not include resources, so the run-1 working-set outlier remains in the raw
evidence while this summary reports the 15.1 MiB median. All three runs
completed without a benchmark warning. Background activity and laptop
thermals were not controlled; this is not an SLA.

## Dated Windows/amd64 prior laptop snapshot

This snapshot was measured on 2026-09-27 from source commit
`120387fcaaa63dd597cac498f3c490af186ad60f`. The binary was built with Go
1.27.1 for Windows/amd64 using `-trimpath -buildvcs=true -ldflags "-s -w -X
main.version=v0.7.0-dev"`. The host was a Samsung 750XGK laptop with an Intel
Core i7-1355U (10 physical cores and 12 logical CPUs), 16 GB of RAM, and
Windows 11 Home build 26200. It was on AC power with a full battery; the active
power scheme was `Samsung Mode`.

Three sequential runs used 128 measured samples, eight warmups, concurrency
1/4/16/64, 1,000 cancelled jobs for database growth, and a ten-second idle
window. Run 2 is shown below because it had the smallest summed normalized
deviation from the three-run median across every operation/concurrency p50,
p95, p99, and throughput value. The [complete reports and selection
details](benchmarks/2026-09-27-windows-i7-1355u/) are published together. This
is one laptop observation, not an SLA; thermal behavior and background load
were not controlled beyond the recorded power state.

### Durable relay submit/read/cancel

| Clients | p50 | p95 | p99 | Throughput |
| ---: | ---: | ---: | ---: | ---: |
| 1 | 13.109 ms | 15.010 ms | 15.510 ms | 76.1 ops/s |
| 4 | 50.002 ms | 54.098 ms | 63.363 ms | 79.3 ops/s |
| 16 | 197.841 ms | 202.257 ms | 202.975 ms | 80.2 ops/s |
| 64 | 803.013 ms | 805.901 ms | 808.310 ms | 79.2 ops/s |

### Small E2EE job and result

| Clients | p50 | p95 | p99 | Throughput |
| ---: | ---: | ---: | ---: | ---: |
| 1 | 0.125 ms | 0.157 ms | 0.158 ms | 8,608.5 ops/s |
| 4 | 0.187 ms | 0.344 ms | 0.375 ms | 19,835.6 ops/s |
| 16 | 0.469 ms | 0.939 ms | 1.063 ms | 26,370.8 ops/s |
| 64 | 1.519 ms | 4.347 ms | 4.512 ms | 26,144.3 ops/s |

### Verify one 64 KiB artifact

| Clients | p50 | p95 | p99 | Throughput |
| ---: | ---: | ---: | ---: | ---: |
| 1 | 0.065 ms | 0.126 ms | 0.157 ms | 12,807.9 ops/s |
| 4 | 0.136 ms | 0.174 ms | 0.178 ms | 28,037.9 ops/s |
| 16 | 0.467 ms | 0.625 ms | 0.721 ms | 32,666.7 ops/s |
| 64 | 1.155 ms | 2.035 ms | 2.545 ms | 45,605.2 ops/s |

### Resource footprint

| Measurement | Observation | Scope |
| --- | ---: | --- |
| Stripped core binary | 13.3 MiB | Built executable only |
| Idle benchmark process + relay RSS | 15.3 MiB | Current Windows working set after warmup |
| Go heap allocated / runtime reserved | 1.2 MiB / 15.6 MiB | Same process and sample window |
| Idle CPU | below sampling resolution | No process CPU tick recorded during the 10 s window; not claimed as literal zero |
| Fresh Bolt allocation growth | 8.0 MiB / 1,000 jobs | 1,000 small jobs created and cancelled; allocated file growth, not device writes |
| Generic adapter status payload | 245 B / heartbeat; 172.3 KiB/hour | One idle endpoint at 5 s; application JSON only |
| Worker heartbeat payload | 728 B / heartbeat; 511.9 KiB/hour | One GPU and one model at 5 s; WebSocket JSON only |

Across runs 1/2/3, concurrency-1 durable queue throughput was 75.8/76.1/76.8
ops/s and idle RSS was 15.1/15.3/15.1 MiB. All runs completed without a
benchmark warning. The same heartbeat, Bolt allocation, and exclusion caveats
as the other snapshots apply.

## Bounded history and control traffic

The relay job-list endpoint is a bounded summary index: at most 200 records
per request, without request bodies, result bodies, sealed envelopes, progress
text, or per-node routing candidates. Retrieve one exact job when its retained
result is required. Queued jobs use a small durable owner/priority index, so
admission and fair dispatch do not deserialize every queued payload.

Local service job/result pairs are retained together and pruned by all three
configured limits:

```yaml
storage:
  job_retention_days: 30
  max_job_records: 1000
  max_job_storage_bytes: 4294967296 # 4 GiB
```

Incomplete jobs are not pruned by this history policy. Files delivered to the
operator-facing inbox are separate artifacts and are never silently deleted by
job-history retention. Authenticated worker reconnects are rate-limited per
node identity, and heartbeat control traffic has a per-connection count and
byte budget; different workers behind one IP do not consume each other's
allowance.

## Dated Linux/amd64 current VPS snapshot

This VPS snapshot was measured on 2026-09-28 from exact clean source commit
`1300b4b5c649ab3b6cc40626deedbf38ad5f5907`. The static Linux/amd64 binary was
built with Go 1.27.0, `CGO_ENABLED=0`, `-trimpath -buildvcs=true`, and stripped
linker flags. Embedded build provenance confirmed the commit and
`vcs.modified=false`. The host was Ubuntu 22.04.5 LTS on two virtual AMD EPYC
9354P CPUs with 7.75 GiB RAM.

Five sequential reports used 128 measured samples, eight warmups, concurrency
1/4/16/64, 1,000 cancelled jobs, and a ten-second idle window. Run 3 is shown
because it had the smallest summed normalized deviation from the five-run
median across every operation/concurrency p50, p95, p99, and throughput value.
The [complete raw reports and selection details](benchmarks/2026-09-28-linux-vps-1300b4b/)
are published together.

### Durable relay submit/read/cancel

| Clients | p50 | p95 | p99 | Throughput |
| ---: | ---: | ---: | ---: | ---: |
| 1 | 7.498 ms | 19.955 ms | 26.753 ms | 114.2 ops/s |
| 4 | 18.689 ms | 42.253 ms | 52.658 ms | 193.5 ops/s |
| 16 | 64.701 ms | 74.966 ms | 75.281 ms | 242.9 ops/s |
| 64 | 440.236 ms | 483.541 ms | 494.306 ms | 135.3 ops/s |

### Small E2EE job and result

| Clients | p50 | p95 | p99 | Throughput |
| ---: | ---: | ---: | ---: | ---: |
| 1 | 0.207 ms | 0.246 ms | 0.277 ms | 4,717.6 ops/s |
| 4 | 0.225 ms | 0.778 ms | 1.615 ms | 8,778.2 ops/s |
| 16 | 0.226 ms | 3.360 ms | 4.590 ms | 8,508.4 ops/s |
| 64 | 0.280 ms | 9.801 ms | 10.860 ms | 7,537.2 ops/s |

### Verify one 64 KiB artifact

| Clients | p50 | p95 | p99 | Throughput |
| ---: | ---: | ---: | ---: | ---: |
| 1 | 0.118 ms | 0.183 ms | 0.246 ms | 7,887.9 ops/s |
| 4 | 0.311 ms | 0.788 ms | 1.063 ms | 10,307.5 ops/s |
| 16 | 0.848 ms | 3.433 ms | 4.308 ms | 11,269.0 ops/s |
| 64 | 2.689 ms | 8.590 ms | 9.488 ms | 13,134.9 ops/s |

### Resource footprint and shared-host variance

| Measurement | Observation | Scope |
| --- | ---: | --- |
| Stripped static core binary | 12.9 MiB | Built executable only |
| Idle benchmark process + relay RSS | 13.3 MiB | Current Linux RSS after warmup |
| Go heap allocated / runtime reserved | 1.0 MiB / 12.0 MiB | Same process and sample window |
| Idle CPU | 0.124% of one core / 0.062% of host | Ten-second process CPU window |
| Bolt allocated-file growth | 8.0 MiB / 1,000 jobs | Stepwise capacity; not normalized per job |
| Bolt live bucket growth | 1.61 MiB / 1,000 jobs | Branch/leaf bytes occupied by retained cancellation evidence |

Concurrency-1 durable queue throughput across runs 1 through 5 was
218.7/111.4/114.2/153.5/145.1 ops/s. That nearly twofold range on the same
binary is direct evidence that noisy-neighbour contention materially affects
this shared VPS. The fastest result was not selected, and this must not be
read as Linux performance or an SLA.

## Dated Linux/amd64 historical VPS snapshot

This VPS snapshot was measured on 2026-09-20 from source commit
`7018d831798f09719386432dde82db22b1184707`. The binary was built with the
checksum-verified official Go 1.25.14 Linux/amd64 toolchain using `-trimpath
-ldflags "-s -w"` and version label `v0.7.0-dev`. The host was Ubuntu 22.04.5
LTS on a two-vCPU AMD EPYC 9354P allocation with 8.3 GB of memory.

Settings matched the Windows run: 128 measured samples, eight warmups,
concurrency 1/4/16/64, 1,000 cancelled jobs for database growth, and a
ten-second idle window. The smaller shared VPS is intentionally reported
separately instead of blending unlike hosts into one headline number.
It is a shared host: CPU steal, storage contention, scheduling pressure, and
other noisy-neighbour effects were not controlled or measured. They can cause
both lower throughput and higher run-to-run variance, so the gap must not be
read as evidence that Linux itself is slower.

### Durable relay submit/read/cancel

| Clients | p50 | p95 | p99 | Throughput |
| ---: | ---: | ---: | ---: | ---: |
| 1 | 12.165 ms | 25.418 ms | 36.304 ms | 73.8 ops/s |
| 4 | 34.951 ms | 66.869 ms | 84.926 ms | 104.8 ops/s |
| 16 | 115.173 ms | 242.771 ms | 255.881 ms | 120.6 ops/s |
| 64 | 351.352 ms | 423.437 ms | 426.053 ms | 165.1 ops/s |

### Small E2EE job and result

| Clients | p50 | p95 | p99 | Throughput |
| ---: | ---: | ---: | ---: | ---: |
| 1 | 0.217 ms | 0.320 ms | 0.496 ms | 4,260.3 ops/s |
| 4 | 0.253 ms | 0.773 ms | 2.138 ms | 7,563.5 ops/s |
| 16 | 0.337 ms | 8.386 ms | 11.567 ms | 5,433.9 ops/s |
| 64 | 0.278 ms | 9.192 ms | 10.715 ms | 8,053.8 ops/s |

### Verify one 64 KiB artifact

| Clients | p50 | p95 | p99 | Throughput |
| ---: | ---: | ---: | ---: | ---: |
| 1 | 0.141 ms | 0.316 ms | 0.446 ms | 5,846.9 ops/s |
| 4 | 0.239 ms | 1.031 ms | 1.175 ms | 10,402.4 ops/s |
| 16 | 0.885 ms | 3.103 ms | 4.014 ms | 11,314.8 ops/s |
| 64 | 3.059 ms | 8.398 ms | 8.935 ms | 12,022.0 ops/s |

### Resource footprint

| Measurement | Observation | Scope |
| --- | ---: | --- |
| Stripped core binary | 10.3 MiB | Built executable only |
| Idle benchmark process + relay RSS | 12.7 MiB | Current Linux RSS after warmup |
| Go heap allocated / runtime reserved | 0.8 MiB / 8.0 MiB | Same process and sample window |
| Idle CPU | 0.10% of one core | 9.9 ms process CPU time during the 10 s window |
| Fresh Bolt allocation growth | 4.0 MiB / 1,000 jobs | 1,000 small jobs created and cancelled; allocated file growth, not device writes |
| Generic adapter status payload | 245 B / heartbeat; 172.3 KiB/hour | One idle endpoint at 5 s; application JSON only |
| Worker heartbeat payload | 704 B / heartbeat; 495.0 KiB/hour | One GPU and one model at 5 s; WebSocket JSON only |

The same exclusions and heartbeat caveats as the Windows snapshot apply. The
VPS results demonstrate portability and constrained-host behavior; they are
not presented as a comparison of operating systems. Repeated runs on a
dedicated host are required before attributing a difference to ContextBridge,
the operating system, or the hardware rather than shared-host contention.

## Explicit exclusions and unavailable metrics

The benchmark excludes model/provider inference, adapter execution, Internet
and cross-device latency, and transport framing from heartbeat estimates. It
does not invent values for:

- RAM per active inference job, because the run starts no model and process
  memory cannot be attributed honestly to one job;
- operating-system disk-write bytes per job, because Bolt allocation is not
  filesystem journaling, cache behavior, or device write amplification;
- complete wire bytes per job, because prompt/result sizes and transport
  framing are workload- and deployment-dependent.

Publish benchmark results with the complete JSON report, source commit, binary
flags, host, sample settings, warnings, and exclusions. Comparing only a p50
without that context is not a reproducible ContextBridge measurement.
