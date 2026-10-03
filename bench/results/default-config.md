# Strata benchmark results

Measured on 2026-10-02.

## Environment

- CPU: 13th Gen Intel(R) Core(TM) i5-1345U (12 logical cores)
- Memory: 15107 MiB
- OS: Linux 7.0.0-34-generic
- Go: go1.26.7
- Docker version 29.8.1, build 4a63305
- Deployment: docker compose: strata and rustfs 1.0.0 containers on the same host; server defaults (buffer 4 MiB / 5 s, compaction target 64 MiB, minimum 4 segments per merge)
- Client: strata-bench on the same machine, gRPC over loopback

## Dataset and ingest

- Dataset: BGL (BlueGene/L supercomputer logs, LogHub), 4747963 lines, 709 MiB, covering 2005-06-03T15:42:50Z to 2006-01-04T08:00:05Z
- Settings: 4 parallel gRPC streams, 500 lines per batch; server defaults (buffer 4 MiB / 5 s, compaction on)

| Measure | Value |
|---|---|
| Lines ingested | 4747963 |
| Time to last acknowledgement | 60.5 s |
| **Ingest throughput** | **78420 lines/s** |
| Ingest throughput (dataset bytes) | 11.7 MiB/s |
| Batch acknowledgement latency P50 / P99 / max | 11.5 / 632.1 / 1329.0 ms |
| Pushed back by the server (BUFFER_FULL retries) | 0 |
| Client read+parse ceiling (no sending) | 678410 lines/s |

An acknowledgement means the batch is in the server's memory buffer; it becomes durable when sealed into a segment (within 5 s or 4 MiB). The throughput above is therefore the rate of *accepting* data; the segment settle time below shows when it was all stored and compacted.

## Storage and compaction

| Measure | Value |
|---|---|
| Segments right after ingest | 141 |
| Segments after compaction settled | 17 |
| Time until the segment count stopped changing | 45 s |
| Total stored size | 1008 MiB |
| Stored bytes per dataset byte | 1.42 |

## Correctness check

300 randomly sampled lines were looked up by node name and exact microsecond timestamp: **300 found**.

## Query latency

Each query type ran sequentially after 2 warm-up runs, measured at the client (round trip over loopback, including the server's work). Time windows are centred on randomly sampled real lines. "Skipped" columns are per-query averages of the server's own counters.

| Query | Runs | P50 | P99 | Max | Segments considered | Skipped by time | Skipped by bloom | Scanned | MiB read | Hits |
|---|---|---|---|---|---|---|---|---|---|---|
| rare word, all history | 100 | 481.6 ms | 593.9 ms | 673.3 ms | 17 | 0.0 | 15.0 | 2.0 | 122.1 | 100 |
| rarer word, all history | 100 | 324.2 ms | 454.4 ms | 494.6 ms | 17 | 0.0 | 16.0 | 1.0 | 62.2 | 100 |
| common word, 1-hour window | 100 | 311.0 ms | 527.5 ms | 544.1 ms | 17 | 15.8 | 0.0 | 1.2 | 74.9 | 40 |
| tag level=FATAL, 1-day window | 100 | 381.4 ms | 672.3 ms | 680.3 ms | 17 | 15.5 | 0.0 | 1.5 | 93.7 | 88 |
| alert tag, all history | 100 | 912.7 ms | 1049.6 ms | 1060.2 ms | 17 | 0.0 | 12.0 | 5.0 | 306.6 | 100 |
| word not in the data | 100 | 23.4 ms | 34.4 ms | 37.6 ms | 17 | 0.0 | 17.0 | 0.0 | 0.0 | 0 |
| common word, all history (worst case) | 10 | 2601.6 ms | 2887.8 ms | 2887.8 ms | 17 | 0.0 | 0.0 | 17.0 | 1007.7 | 100 |

- **rare word, all history**: word "hangtest" (about 250 lines in total), no time range
- **rarer word, all history**: word "rmdir", no time range
- **common word, 1-hour window**: word "error" (about 750k lines), 1-hour window around a random real line
- **tag level=FATAL, 1-day window**: tag level=FATAL (about 855k lines), 1-day window around a random real line
- **alert tag, all history**: tag alert=KERNDTLB (about 150k lines, clustered in time), no time range
- **word not in the data**: word that appears nowhere, no time range
- **common word, all history (worst case)**: word "error", no time range: every segment contains it, so every segment is read

With few runs, P99 is simply the slowest or second-slowest run; treat it as such.
