# Windows i7-1355U current benchmark reports

These reports were captured on 2026-09-28 from exact clean source commit
`3856f26f5366b1a93ee0ce25a81de640c8101a88`. The benchmark executable was
built with Go 1.27.1 for Windows/amd64 using:

```text
go build -trimpath -buildvcs=true -ldflags "-s -w -X main.version=v0.8.0-dev" -o .tmp-metrics/contextbridge-benchmark.exe ./cmd/contextbridge
```

Embedded build provenance reported the same revision and
`vcs.modified=false`. The host was a Samsung 750XGK laptop with an Intel Core
i7-1355U (10 physical cores, 12 logical CPUs), 16 GB of RAM, and Windows 11
Home build 26200. It was on AC power at 98% battery with the active Windows
power scheme reported as `Samsung Mode`.

Three sequential runs used 128 measured samples after eight warmups at
concurrency 1/4/16/64, 1,000 database jobs, and a ten-second idle window. Run
1 is the documented representative timing run: it has the smallest sum of
normalized absolute deviations from the three-run median across p50, p95,
p99, and throughput for every operation/concurrency row.

| Run | Selection score | Queue throughput at concurrency 1 | Idle RSS |
| ---: | ---: | ---: | ---: |
| [1](run-1.json) | 1.05 | 94.9 ops/s | 55.8 MiB |
| [2](run-2.json) | 1.18 | 94.6 ops/s | 15.1 MiB |
| [3](run-3.json) | 1.38 | 94.7 ops/s | 15.0 MiB |

The selection score is only for choosing representative operation timings;
lower is not a performance claim and resource metrics do not participate in
the score. Run 1's Windows working set was a visible outlier while Go heap
allocation remained 1.2 MiB in every run. Aggregate documentation therefore
reports the three-run median idle RSS of 15.1 MiB and keeps all raw values
available here.

The concurrency-1 durable-queue median increased from 76.09 ops/s in the
[2026-09-27 snapshot](../2026-09-27-windows-i7-1355u/) to 94.72 ops/s here, an
observed 24.5% increase. This is a cross-commit development comparison under
uncontrolled background load, not a controlled attribution or SLA.

The reports are complete command output. `binary_path` was emitted as the
executable base name by the benchmark itself; no measured field was changed
after capture.
