# Limits and measured ContextBridge overhead

ContextBridge separates protocol limits from engine and provider limits. A
model may refuse a request, return fewer artifacts, or impose a smaller quota.
The boundaries below describe what the public core accepts and verifies.

## Payload and result boundaries

| Boundary | Core behavior |
| --- | --- |
| Input image | One image per job, up to exactly 8 MiB after base64 decoding. |
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
the benchmark process, fresh-database allocation growth, and fixed typed
heartbeat payloads. It creates temporary databases and loopback listeners,
removes them afterward, sends no model request, and contacts no Internet host.

## Dated Windows/amd64 development snapshot

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

## Dated Linux/amd64 VPS snapshot

This second snapshot was measured on 2026-09-20 from source commit
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
