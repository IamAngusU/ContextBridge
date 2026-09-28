# Windows i7-1355U benchmark reports

These reports were captured on 2026-09-27 from source commit
`120387fcaaa63dd597cac498f3c490af186ad60f`. The benchmark executable was
built with Go 1.27.1 for Windows/amd64 using:

```text
go build -trimpath -buildvcs=true -ldflags "-s -w -X main.version=v0.7.0-dev" -o .tmp-metrics/contextbridge-benchmark.exe ./cmd/contextbridge
```

The host was a Samsung 750XGK laptop with an Intel Core i7-1355U (10 physical
cores, 12 logical CPUs), 16 GB of RAM, and Windows 11 Home build 26200. It was
on AC power with a full battery and the active Windows power scheme reported
as `Samsung Mode`.

Three sequential runs used 128 measured samples after eight warmups at
concurrency 1/4/16/64, 1,000 database jobs, and a ten-second idle window. Run
2 is the documented representative run: it has the smallest sum of normalized
absolute deviations from the three-run median across p50, p95, p99, and
throughput for every operation/concurrency row.

| Run | Selection score | Queue throughput at concurrency 1 | Idle RSS |
| ---: | ---: | ---: | ---: |
| [1](run-1.json) | 1.82 | 75.8 ops/s | 15.1 MiB |
| [2](run-2.json) | 1.22 | 76.1 ops/s | 15.3 MiB |
| [3](run-3.json) | 1.53 | 76.8 ops/s | 15.1 MiB |

The selection score is only for choosing a representative run; lower is not a
performance claim. The reports are otherwise the complete command output.
Only `resources.files.binary_path` was normalized from the local absolute path
to `contextbridge-benchmark.exe` before publication to avoid disclosing a
workstation username and checkout path. No measured field was changed.
