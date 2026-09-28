# Linux two-vCPU VPS snapshot

This directory preserves five complete reports measured on 2026-09-28 from
exact clean source commit
`1300b4b5c649ab3b6cc40626deedbf38ad5f5907`.

The statically linked Linux/amd64 binary was cross-built with Go 1.27.0 using
`CGO_ENABLED=0`, `-trimpath -buildvcs=true`, and stripped linker flags.
Embedded Go build provenance confirmed `vcs.modified=false` and the exact
commit above before the temporary binary was removed from the VPS.

The host was Ubuntu 22.04.5 LTS, Linux 5.15.0-185-generic, with two virtual AMD
EPYC 9354P CPUs and 7.75 GiB RAM. It is shared infrastructure: CPU steal,
storage contention, scheduling pressure, and other noisy-neighbour effects
were not controlled or separately observable.

Each report used 128 measured samples after eight warmups at concurrency
1/4/16/64, 1,000 fresh create/cancel database jobs, and a ten-second idle
window. Before looking at the results, the representative-run rule remained:
select the run with the smallest summed normalized deviation from the five-run
median over every operation/concurrency p50, p95, p99, and throughput value.
That selects run 3.

## Representative run 3

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

### Resource footprint

| Measurement | Observation | Scope |
| --- | ---: | --- |
| Stripped static core binary | 12.9 MiB | Built executable only |
| Idle benchmark process + relay RSS | 13.3 MiB | Current Linux RSS after warmup |
| Go heap allocated / runtime reserved | 1.0 MiB / 12.0 MiB | Same process and sample window |
| Idle CPU | 0.124% of one core / 0.062% of host capacity | Ten-second process CPU window |
| Bolt allocated-file growth | 8.0 MiB / 1,000 jobs | Stepwise capacity; not normalized per job |
| Bolt live bucket growth | 1.61 MiB / 1,000 jobs | Branch/leaf bytes occupied by retained cancellation evidence |
| Generic adapter status payload | 245 B / heartbeat; 172.3 KiB/hour | One idle endpoint at 5 s; application JSON only |
| Worker heartbeat payload | 728 B / heartbeat; 511.9 KiB/hour | One GPU and one model at 5 s; WebSocket JSON only |

## Variance is part of the result

Concurrency-1 durable queue throughput across the five sequential runs was
218.7/111.4/114.2/153.5/145.1 ops/s. The fastest run was not selected, and no
run was deleted. This nearly twofold range on the same binary is direct
evidence that shared-host contention materially affects the snapshot; it must
not be interpreted as Linux performance or an SLA.

The old 2026-09-20 snapshot remains published as historical evidence, but its
original raw JSON was no longer present on the VPS and is not reconstructed.
These five files are the first complete retained raw VPS set.

