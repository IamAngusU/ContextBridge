# Windows i9-12900K benchmark reports

These reports were captured on 2026-09-28 from source commit
`0c5812f25b872b14dd58e3e0d3e4ac4847340754`. The benchmark executable was
built with Go 1.27.1 for Windows/amd64 using:

```text
go build -trimpath -buildvcs=true -ldflags "-s -w -X main.version=v0.7.0-dev" -o .tmp-metrics/contextbridge-benchmark.exe ./cmd/contextbridge
```

The directory uses the Europe/Berlin calendar date; `generated_at` inside each
machine-readable report is UTC.

The host was an Acer Predator PO7-640 desktop with an Intel Core i9-12900K
(16 physical cores, 24 logical CPUs), 32 GiB of DDR5 memory configured at
4000 MT/s, an NVIDIA GeForce RTX 3080 with 10 GiB of VRAM, and Windows 11 Home
build 26200. The active Windows power scheme was `High performance`; the
desktop had no battery.

Normal workstation background services remained active and were not stopped
for the measurement. The benchmark itself performs no model inference and
does not use the GPU. These are therefore real workstation observations, not
isolated lab measurements or an SLA.

The first three sequential runs used 128 measured samples after eight warmups
at concurrency 1/4/16/64, 1,000 database jobs, and a ten-second idle window.
Run 2 was a visible durable-queue outlier, so two diagnostic repeats were
captured instead of discarding or hiding it. All five reports are published.

Run 3 is the documented representative run. It has the smallest sum of
normalized absolute deviations from the five-run median across p50, p95, p99,
and throughput for every operation/concurrency row.

| Run | Selection score | Queue throughput at concurrency 1 | Idle RSS |
| ---: | ---: | ---: | ---: |
| [1](run-1.json) | 6.16 | 297.9 ops/s | 15.4 MiB |
| [2](run-2.json) | 14.44 | 173.3 ops/s | 15.3 MiB |
| [3](run-3.json) | 2.70 | 315.3 ops/s | 15.3 MiB |
| [4](run-4.json) | 5.53 | 318.2 ops/s | 15.3 MiB |
| [5](run-5.json) | 3.77 | 302.4 ops/s | 15.3 MiB |

The selection score chooses a representative observation; lower is not a
performance claim. The reports otherwise preserve the complete command
output. Only `resources.files.binary_path` was normalized from the local path
to `contextbridge-benchmark.exe`. No measured field was changed.
