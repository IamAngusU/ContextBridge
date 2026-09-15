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
