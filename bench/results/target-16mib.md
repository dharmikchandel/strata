# Strata benchmark results

Measured on 2026-10-02.

## Environment

- CPU: 13th Gen Intel(R) Core(TM) i5-1345U (12 logical cores)
- Memory: 15107 MiB
- OS: Linux 7.0.0-34-generic
- Go: go1.26.7
- Docker version 29.8.1, build 4a63305
- Deployment: docker compose: strata and rustfs 1.0.0 containers on the same host; compaction target lowered to 16 MiB with a minimum of 2 segments per merge (STRATA_COMPACT_TARGET_BYTES=16777216, STRATA_COMPACT_MIN_SEGMENTS=2), other settings default
- Client: strata-bench on the same machine, gRPC over loopback

## Dataset and ingest

- Dataset: BGL (BlueGene/L supercomputer logs, LogHub), 4747963 lines, 709 MiB, covering 2005-06-03T15:42:50Z to 2006-01-04T08:00:05Z
- Settings: 4 parallel gRPC streams, 500 lines per batch; server defaults (buffer 4 MiB / 5 s, compaction on)

| Measure | Value |
|---|---|
| Lines ingested | 4747963 |
| Time to last acknowledgement | 66.3 s |
| **Ingest throughput** | **71588 lines/s** |
| Ingest throughput (dataset bytes) | 10.7 MiB/s |
| Batch acknowledgement latency P50 / P99 / max | 12.3 / 673.1 / 1247.2 ms |
| Pushed back by the server (BUFFER_FULL retries) | 0 |
| Client read+parse ceiling (no sending) | 725405 lines/s |

An acknowledgement means the batch is in the server's memory buffer; it becomes durable when sealed into a segment (within 5 s or 4 MiB). The throughput above is therefore the rate of *accepting* data; the segment settle time below shows when it was all stored and compacted.

## Storage and compaction

| Measure | Value |
|---|---|
| Segments right after ingest | 154 |
| Segments after compaction settled | 73 |
| Time until the segment count stopped changing | 50 s |
| Total stored size | 1016 MiB |
| Stored bytes per dataset byte | 1.43 |

## Correctness check

300 randomly sampled lines were looked up by node name and exact microsecond timestamp: **300 found**.

## Query latency

Each query type ran sequentially after 2 warm-up runs, measured at the client (round trip over loopback, including the server's work). Time windows are centred on randomly sampled real lines. "Skipped" columns are per-query averages of the server's own counters.

| Query | Runs | P50 | P99 | Max | Segments considered | Skipped by time | Skipped by bloom | Scanned | MiB read | Hits |
|---|---|---|---|---|---|---|---|---|---|---|
| rare word, all history | 100 | 150.5 ms | 242.3 ms | 253.3 ms | 73 | 0.0 | 71.0 | 2.0 | 26.4 | 100 |
| rarer word, all history | 100 | 164.5 ms | 221.4 ms | 224.0 ms | 73 | 0.0 | 70.0 | 3.0 | 42.1 | 100 |
| common word, 1-hour window | 100 | 102.5 ms | 232.4 ms | 236.9 ms | 73 | 71.1 | 0.2 | 1.7 | 24.3 | 40 |
| tag level=FATAL, 1-day window | 100 | 113.3 ms | 330.2 ms | 334.8 ms | 73 | 69.7 | 0.9 | 2.4 | 34.0 | 88 |
| alert tag, all history | 100 | 475.4 ms | 642.8 ms | 674.9 ms | 73 | 0.0 | 63.0 | 10.0 | 145.7 | 100 |
| word not in the data | 100 | 132.9 ms | 252.9 ms | 257.5 ms | 73 | 0.0 | 72.0 | 1.0 | 15.7 | 0 |
| common word, all history (worst case) | 10 | 2497.8 ms | 2640.8 ms | 2640.8 ms | 73 | 0.0 | 4.0 | 69.0 | 963.7 | 100 |

- **rare word, all history**: word "hangtest" (about 250 lines in total), no time range
- **rarer word, all history**: word "rmdir", no time range
- **common word, 1-hour window**: word "error" (about 750k lines), 1-hour window around a random real line
- **tag level=FATAL, 1-day window**: tag level=FATAL (about 855k lines), 1-day window around a random real line
- **alert tag, all history**: tag alert=KERNDTLB (about 150k lines, clustered in time), no time range
- **word not in the data**: word that appears nowhere, no time range
- **common word, all history (worst case)**: word "error", no time range: every segment contains it, so every segment is read

With few runs, P99 is simply the slowest or second-slowest run; treat it as such.
