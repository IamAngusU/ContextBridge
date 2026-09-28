# Windows i9-12900K durable-queue regression fix

This directory preserves the five complete reports and the controlled A/B
evidence used to evaluate commit
`2b32824c3ba570e756bcd0504edeb8a9e0472a33` on 2026-09-28.

The measured host was an Acer Predator PO7-640 with an Intel Core i9-12900K
(16 physical cores, 24 logical CPUs), 32 GiB RAM, Windows/amd64, and the
Windows High performance power scheme. Both binaries were built with Go
1.27.0, `-trimpath -buildvcs=true`, and stripped linker flags. Normal
workstation background activity remained enabled. No model or provider
inference ran.

## Five-run current snapshot

Each `run-N.json` used 128 measured samples after eight warmups at concurrency
1/4/16/64, 1,000 fresh create/cancel database jobs, and a ten-second idle
window. Before looking at the results, the representative-run rule remained:
select the run with the smallest summed normalized deviation from the five-run
median over every operation/concurrency p50, p95, p99, and throughput value.
That selects run 1; its score was 3.5183 versus 3.6884 for the next closest run.

| Clients | p50 | p95 | p99 | Throughput |
| ---: | ---: | ---: | ---: | ---: |
| 1 | 2.547 ms | 5.071 ms | 10.177 ms | 389.6 ops/s |
| 4 | 8.767 ms | 10.661 ms | 11.616 ms | 464.1 ops/s |
| 16 | 38.956 ms | 44.761 ms | 47.643 ms | 407.4 ops/s |
| 64 | 144.467 ms | 147.382 ms | 149.466 ms | 435.5 ops/s |

Concurrency-1 throughput across the five runs was
389.6/379.5/399.4/360.1/395.9 ops/s. The representative idle working set was
15.4 MiB and the stripped binary was 13.3 MiB.

Schema-2 database evidence for run 1:

- allocated Bolt file: 32 KiB to 8 MiB (stepwise capacity, not a per-job rate);
- live bucket growth: 1,681,381 bytes for 1,000 retained cancellation
  tombstones and their authoritative evidence (1,681.381 bytes/job);
- transaction page allocation: 105,508,864 bytes across 25,759 page
  allocations. This is internal Bolt churn, not claimed OS/device writes.

A separate 5,000-job diagnostic produced 8,409,077 live bucket bytes, or
1,681.8154 bytes/job, while allocated file size jumped to 32 MiB. That stable
live slope and discontinuous file allocation are why schema 2 no longer
normalizes file growth per job.

## Alternating same-toolchain A/B

The `ab-run-N-*.json` files used this fixed order to reduce drift:
prior/fixed/fixed/prior/prior/fixed. Every run used 192 samples, eight warmups,
1,000 database jobs, and a 500 ms idle window. The prior binary is exact commit
`3fe46fd6e24ccdefbe814e7ab05b8f37286acd99`; the fixed binary is
`2b32824c3ba570e756bcd0504edeb8a9e0472a33`. Both were compiled on the same
host with Go 1.27.0 and the same build flags.

| Metric | Prior median | Fixed median | Change |
| --- | ---: | ---: | ---: |
| 1-client throughput | 318.7 ops/s | 388.9 ops/s | +22.0% |
| 64-client throughput | 372.5 ops/s | 413.2 ops/s | +10.9% |
| 1-client p50 | 3.072 ms | 2.576 ms | -16.2% |
| 64-client p50 | 169.134 ms | 149.631 ms | -11.5% |

The removed work was an empty durable recovery-probe transaction on queued-job
cancellation. A queued job has no assigned node and cannot own such a probe.
The remaining difference from the 2026-09-20 historical snapshot represents
real additional work introduced since then (including authoritative events,
producer isolation, and bounded queue indexes), plus ordinary host/toolchain
variation. No durability, ownership, fencing, or event evidence was removed.

These observations are development snapshots, not an SLA.

The reports are complete benchmark output except that `binary_path` was
normalized to the executable base name before publication. This removes the
workstation username and temporary checkout path without changing a measured
field.
