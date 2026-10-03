# Strata

Strata is a log search engine written in Go. It stores logs as **immutable, indexed segments in S3-compatible object storage** and answers full-text and time-range searches by consulting a small SQLite manifest first, so that it **skips the segments that cannot contain the answer instead of reading them**.

Every design decision follows from one idea: *don't read data you don't need.* Each search reports how many segments it skipped and how many it actually read, so the idea can be checked rather than taken on trust.

- [How it works](#how-it-works)
- [Try it](#try-it)
- [Benchmarks](#benchmarks)
- [What happens when things fail](#what-happens-when-things-fail)
- [Design decisions](#design-decisions)
- [Limitations](#limitations)
- [Project layout](#project-layout)
- [Tests](#tests)

## How it works

```
 clients ──gRPC stream──▶ ┌──────────────────────────── strata ────────────────────────────┐
                          │  ingest buffer ──seal──▶ segment file ──▶ object storage (S3)   │
                          │        │                      │                                  │
                          │        └──── record ──────────┴────▶ manifest (SQLite)           │
                          │                                         ▲        ▲               │
 searches ──gRPC────────▶ │  query engine ── time range + bloom ────┘        │               │
                          │        └── fetch only the surviving segments ────┤               │
                          │  compactor ── merge small segments, then swap ───┘               │
                          └──────────────────────────────────────────────────────────────────┘
```

**Segments.** Logs arrive over a gRPC stream and are collected in memory. When about 4 MiB has accumulated, or the oldest line has waited 5 seconds, the buffer is *sealed* into one immutable file: a header (line count, first and last timestamp, checksums), the log lines, an inverted index (word → lines containing it) and a bloom filter over every word in the segment. Segments are never edited. That is what makes the rest simple: no locks on read, nothing to update in place, and a segment can be copied or cached without worrying that it will change.

**The manifest.** A SQLite file records every segment: its object key, time range, size, status and its bloom filter. It is the single source of truth for what data exists. Objects in the bucket that the manifest doesn't list are invisible to searches.

**Searching.** A search passes through three filters, cheapest first, and each may only err in one safe direction (keep a segment that has no match; never drop one that has):

1. **Time range**, answered by one SQLite query. No object-storage reads.
2. **Bloom filter**, also from the manifest. If a search word is *definitely not* in a segment, skip it. No object-storage reads. (A bloom filter can say "maybe" wrongly, costing one wasted read, but never "no" wrongly.)
3. **The segment's own index**, after fetching the few segments that survived.

Results from different segments are merged into timestamp order.

**Compaction.** Many small segments make every search slower, so a background job merges them into larger ones. The merged file is written and read back first, then a *single SQLite transaction* adds it and marks the originals deleted, and only afterwards are the old files removed. A crash at any point leaves either the old state or the new one, never a mix, and never lost or doubled data.

## Try it

You need Docker and Go 1.26 or later.

```sh
docker compose up -d --build        # Strata + a local S3-compatible store (RustFS)

# send some logs: any text file, one line per log line
go run ./cmd/stratactl ingest -file app.log

# search them (lines become searchable within about 5 seconds)
go run ./cmd/stratactl search -text "error timeout" -limit 10

# or use the web page: http://127.0.0.1:7080
go run ./cmd/stratactl ui
```

The page opens with what is stored (lines, segments, time range), a few one-click example searches that each show a different way segments get skipped, and the search form. Every search is then drawn as it happened: one bar per segment, oldest to newest, short if the segment was skipped and tall if it was read, under a sentence such as "Skipped 193 of 196 segments. Read 3 (15.3 MiB)." A list below says which segments were read and how many of the lines shown each one supplied. Those extras are optional and come from flags, so the page carries no knowledge of any particular dataset. For the benchmark dataset:

```sh
go run ./cmd/stratactl ui \
  -corpus-name "BGL supercomputer logs" \
  -examples bench/demo/bgl-examples.json \
  -credit "Data: BGL logs from the BlueGene/L supercomputer at Lawrence Livermore National Laboratory (Oliner and Stearley, DSN 2007), via LogHub." \
  -about-url https://github.com/dharmikchandel/strata#readme
```

`stratactl search` prints the matching lines and then how much work the search did. This is a real search of the benchmark dataset (4.7 million lines in 73 segments); long lines are shortened here:

```
$ stratactl search -text hangtest -limit 3
2005-06-20T13:44:57.111650Z  2005.06.20 R10-M0-N0-I:J18-U11 2005-06-20-13.44.57.111650 ... RAS APP FATAL ciod: Error loading /p/gb2/draeger/benchmark/hangtest_062005/...
2005-06-20T13:44:57.139680Z  2005.06.20 R10-M0-N0-I:J18-U01 2005-06-20-13.44.57.139680 ... RAS APP FATAL ciod: Error loading /p/gb2/draeger/benchmark/hangtest_062005/...
2005-06-20T13:44:57.166392Z  2005.06.20 R10-M1-NC-I:J18-U11 2005-06-20-13.44.57.166392 ... RAS APP FATAL ciod: Error loading /p/gb2/draeger/benchmark/hangtest_062005/...
... more lines match; showing the earliest 3 (raise -limit)

segments  considered 73 | skipped by time 0 | skipped by bloom filter 71 | scanned 2
          [----------------------------------------------------------#]  . time  - bloom  # scanned
read      26.4 MiB from storage | server time 133.45ms | round trip 139.2ms
```

Two of 73 segments were read; the other 71 were ruled out by their bloom filters without being fetched.

`docker compose down` stops everything and keeps the data; add `-v` to delete it. Everything is published on `127.0.0.1` only, because Strata has no TLS or authentication yet (see [Limitations](#limitations)).

Strata itself is one static binary (`go build ./cmd/strata`). Every setting is a flag with an environment variable of the same meaning; `strata -h` lists them.

## Benchmarks

Everything below was measured on a real dataset, on one laptop, and can be reproduced (see the end of this section). Nothing is estimated.

**Dataset.** BGL: logs from the BlueGene/L supercomputer at Lawrence Livermore National Laboratory (via [LogHub](https://github.com/logpai/loghub)). **4,747,963 lines, 709 MiB**, spanning 2005-06-03 to 2006-01-04. Each line was stored with its original text and microsecond timestamp, plus tags for its level (INFO, FATAL, ...), component, and alert category.

**Machine.** 13th Gen Intel(R) Core(TM) i5-1345U, 12 logical cores, 15 GiB RAM, Linux, Docker version 29.8.1, build 4a63305. The Strata server and the S3-compatible store (RustFS 1.0.0) ran as containers on the same machine as the benchmark client, talking over loopback. **That is not real S3**: a network round trip to a cloud bucket would add latency to every segment read, so absolute query times in the cloud would be higher.

### Ingest

4 parallel gRPC streams, 500 lines per batch, server default settings. Three configurations are compared, explained under "What the numbers say".

| | Default (64 MiB target) | Compaction off | 16 MiB target |
|---|---|---|---|
| Ingest throughput | 78,420 lines/s | 81,571 lines/s | 71,588 lines/s |
| Ingest throughput (dataset bytes) | 11.7 MiB/s | 12.2 MiB/s | 10.7 MiB/s |
| Batch acknowledgement P50 / P99 | 11 / 632 ms | 11 / 636 ms | 12 / 673 ms |
| Segments right after ingest | 141 | 196 | 154 |
| Segments after compaction settled | 17 | 196 | 73 |
| Stored size (dataset: 709 MiB) | 1,008 MiB (1.42×) | 1,017 MiB (1.44×) | 1,016 MiB (1.43×) |
| Sampled lines found afterwards | 300 of 300 | 300 of 300 | 300 of 300 |

- Throughput is the rate at which batches were **accepted** (into the server's memory). The data is written to storage within about 5 seconds of that, and the "after settled" rows are when the last segment was sealed and compaction stopped changing anything.
- The client alone could read and parse the file at about 678,410 lines/s, so reading the file was not the limit. I did not determine what was: the server, or the 4 streams each waiting for a reply before sending the next batch.
- After each run, 300 randomly chosen lines were looked up by node name and exact microsecond timestamp. **All 300 were found every time**, so no benchmark number comes from a run that lost data.
- A batch's acknowledgement P99 of about 650 ms is consistent with how sealing works (the writer whose batch fills the buffer does the segment write itself), but I did not isolate that.

### Search

Each query type ran 100 times in a row, after 2 warm-up runs (10 times for the last row, which reads everything). Cells show **P50 / P99 latency**, measured at the client, and **how many segments were actually read out of all that existed**. Time windows are centred on randomly chosen real log lines, so every window contains data.

| Query | Default (64 MiB target): 17 segments | Compaction off: 196 segments | 16 MiB target: 73 segments |
|---|---|---|---|
| rare word, all history | 482 / 594 ms · read 2.0 of 17 | 89 / 138 ms · read 3.0 of 196 | 151 / 242 ms · read 2.0 of 73 |
| rarer word, all history | 324 / 454 ms · read 1.0 of 17 | 102 / 142 ms · read 4.0 of 196 | 165 / 221 ms · read 3.0 of 73 |
| common word, 1-hour window | 311 / 528 ms · read 1.2 of 17 | 55 / 186 ms · read 2.5 of 196 | 102 / 232 ms · read 1.7 of 73 |
| tag level=FATAL, 1-day window | 381 / 672 ms · read 1.5 of 17 | 58 / 251 ms · read 3.3 of 196 | 113 / 330 ms · read 2.4 of 73 |
| alert tag, all history | 913 / 1050 ms · read 5.0 of 17 | 309 / 368 ms · read 20.0 of 196 | 475 / 643 ms · read 10.0 of 73 |
| word not in the data | 23 / 34 ms · read 0.0 of 17 | 27 / 37 ms · read 0.0 of 196 | 133 / 253 ms · read 1.0 of 73 |
| common word, all history (worst case) | 2602 / 2888 ms · read 17.0 of 17 | 2131 / 2219 ms · read 171.0 of 196 | 2498 / 2641 ms · read 69.0 of 73 |

(`P99` over 100 runs is the 99th-fastest run, i.e. nearly the slowest; the last row has only 10 runs. Full results, including MiB read per query, are in [`bench/results/`](bench/results).)

### What the numbers say

- **Skipping works, and it is where the speed comes from.** A search for a common word with a one-hour window read about 1 or 2 segments out of 17, 73, or 196 (skipping 93%, 98% and 99% of them without touching storage). The row at the bottom, where every segment contains the word and so every segment must be read, is what a full scan costs: about 2.1 to 2.6 s in these runs. The same word with a one-hour window took 311 ms (default) and 55 ms (compaction off), **8× and 39× faster**.
- **Rare words and tags skip by bloom filter instead.** Searching a rare word read 1 to 4 segments out of 17 to 196 with no time range at all.
- **Compaction did not make queries faster here. It made them slower.** This surprised me. Compaction cut the number of segments from 196 to 17, but a segment that a search needs is downloaded **whole**, so with 64 MiB segments a search that finds its answer in one segment still reads about 60 MiB. With the original segments (about 5 MiB each) the same search read about 5 MiB and was 3 to 7 times faster for the selective queries. The 16 MiB target landed in between. Compaction also did not change total stored size (about 1.43× the raw data in every case: the inverted index and bloom filters are the overhead), and ingest was 4 to 15% slower with compaction running than with it off, in both of my runs of each configuration (the merging competes with ingest for the same machine).
- **So at this scale compaction mostly costs and does not pay.** Its benefit is fewer segments to track, bloom-check and list, and that matters when there are many thousands of them, which these 196 are not. I did not test that scale. What this data does show is that the default merge target (64 MiB) is too big for whole-segment reads. Reading only the part of a segment that is needed (its header, index and matching lines) would remove the trade-off, and is the obvious next improvement.
- **A word that is not in the data costs either nothing or one wasted read**, depending on whether a bloom filter happens to give a false positive for it (they are tuned to about 1%). In the 16 MiB configuration a false positive made the search read one segment for nothing (133 ms); in the other two it read nothing (23 and 27 ms). That either-or result is how bloom filters behave, not noise to average away.

### Limits of these measurements

- One machine, loopback only, local object store; each configuration was run in full twice. The ordering of results was the same both times, but individual numbers varied: most P50s moved by 5 to 15%, a few by much more (the rare-word query on the default configuration nearly doubled between the two runs, probably because merges group segments differently from run to run, so a different number of segments was read).
- Queries ran one at a time. Latency under concurrent load was not measured.
- Time is measured from the client, so it includes gRPC and loopback networking.
- Only one dataset was used. Its lines have many unique tokens (microsecond timestamps), which makes the index large; other logs would behave differently.

### Reproducing

```sh
# 1. get the dataset (57 MB download, 743 MB unzipped)
curl -L -o BGL.zip https://zenodo.org/records/8196385/files/BGL.zip && unzip BGL.zip

# 2. start a fresh stack on spare ports, so nothing else's data is in the way
export STRATA_PORT=27070 RUSTFS_PORT=29000 RUSTFS_CONSOLE_PORT=29001
docker compose -p strata-bench up -d --build --wait

# 3. run the benchmark (about 12 minutes)
go run ./cmd/strata-bench -addr 127.0.0.1:27070 -dataset BGL.log -out bench/results/mine.md

# the other configurations: add to the strata service environment in a compose
# override file, e.g. STRATA_NO_COMPACT=true, or STRATA_COMPACT_TARGET_BYTES=16777216
# with STRATA_COMPACT_MIN_SEGMENTS=2
docker compose -p strata-bench down -v
```


## What happens when things fail

This is the part of the design that took the most care. For each situation, this is what Strata does and what is tested.

| Situation | What happens |
|---|---|
| The process is stopped (`SIGTERM`, `docker stop`) | Stops accepting new connections, lets in-flight streams finish (up to 10 s), seals everything still in memory, then closes. Tested with the real container: 300 logs that had never been sealed were all readable afterwards. |
| Writing a segment to storage fails | The batch goes back in the buffer and is retried; nothing is lost. If storage stays down the buffer fills up and clients get `BUFFER_FULL` instead of the server running out of memory. |
| The manifest insert fails after the file was written | The file is deleted and the lines stay in the buffer. |
| The process is killed mid-compaction, before or after the manifest swap | Tested at each step: before the swap the old segments remain the data; after it the merged one is. Leftover files are cleaned up later. Queries never see a mix. |
| The write "succeeds" but the stored bytes are wrong | Compaction reads the merged segment back and checks it before committing, so the originals are never replaced by a bad file. |
| A segment is corrupt or missing | Every file has checksums. A search that needs it fails with an error naming the segment, rather than returning an answer that looks complete but isn't. Compaction skips the damaged segment and merges the rest. |
| A segment is replaced by compaction while a search is running | The search notices the missing file and restarts from a fresh view of the manifest. Tested with 4 searchers running continuously through 10 merges: every search saw every line exactly once. |
| Garbage collection and a segment still being written | Unrecorded files younger than 10 minutes are never deleted, because a segment is written a moment before it is recorded. |
| **The manifest file is lost** while the bucket survives | Without protection, garbage collection would treat every real segment as an orphan and delete it. Strata gives the manifest and the bucket a shared random identity and **refuses to start** if they don't match, naming the problem and the way out. Tested with real containers. |
| A client disconnects, or sends a bad or oversized batch | Batches are accepted whole or rejected whole, with a status that says why. A bad batch doesn't affect the stream or other clients. |
| A bug panics inside a request | Caught per request; one bad request can't take the process down. |
| Too many searches at once | Refused immediately with a "busy" status instead of queued, since a search can load whole segments into memory. |

For many of these, the test was validated by temporarily breaking the code in the way it is meant to catch (for example, deleting files before the commit, or closing the buffer before the network server) and checking that the test failed.

## Design decisions

- **Go.** Goroutines and channels fit concurrent ingest and parallel segment fetching; the result is a single static binary.
- **gRPC + Protobuf for ingest and search.** HTTP/2 streams suit continuous log shipping, and the schema makes the log format explicit. Ingest uses a bidirectional stream with a reply per batch, so after a dropped connection a client knows exactly which batches were accepted.
- **SQLite for the manifest**, with write-ahead logging and full fsync on commit. It gives real ACID transactions (the compaction swap is genuinely atomic) without running a separate database, so Strata stays one binary. The trade-off is no built-in replication: the manifest is a single file on a single node.
- **S3-compatible object storage**, developed against a local RustFS server and written to the plain S3 API, so production changes only configuration. The same shared tests run against the real server and an in-memory fake.
- **Bloom filters** let a search skip a segment without opening it. The asymmetry matters: a false positive wastes one read, a false negative would silently lose data, so the filter is only ever allowed to be wrong in the first way. It is tested that it never is the second.
- **No updates or deletes of individual lines.** Segments are immutable; that is a design choice, not a missing feature.

## Limitations

Stated plainly, because they matter:

- **Acknowledged does not mean durable.** The server acknowledges a batch once it is in memory; it is written to storage within about 5 seconds. If the process is killed (not stopped gracefully), the unsealed lines are lost. There is no write-ahead log.
- **A resent batch may be stored twice.** If a connection breaks while a batch is unacknowledged, the client must resend it, which can duplicate lines.
- **No TLS and no authentication.** Strata binds to loopback by default; do not expose it to an untrusted network.
- **Single node.** One server per bucket; there is no clustering, replication or consensus. The manifest must be backed up: Strata can detect a lost manifest but cannot rebuild one.
- **Whole-segment reads.** A search that needs a segment downloads all of it, which is the main cost in the benchmarks above. Reading only the needed parts would reduce it, and would remove the tension between large and small segments.
- **Search is simple.** Words must all appear in a line, in any order, optionally with exact-match tags and a time range. No phrases, OR, NOT, or regular expressions.
- **Newly ingested lines are searchable only after they are sealed**, within about 5 seconds.

## Project layout

```
cmd/strata         the server binary
cmd/stratactl      client: ingest, search, and a minimal web page
cmd/strata-bench   the benchmark harness
internal/segment   the segment file format (header, lines, index, bloom filter)
internal/bloom     bloom filter
internal/storage   storage interface; S3 implementation
internal/manifest  SQLite manifest and its atomic operations
internal/ingest    in-memory buffer, sealing, and the gRPC ingest server
internal/query     query engine and the gRPC search service
internal/compact   compaction and garbage collection
internal/app       configuration, startup, graceful shutdown
internal/bench     dataset parser and statistics for the benchmarks
proto, gen         the gRPC schema and its generated Go code
e2e                end-to-end tests of the Docker setup
```

## Tests

```sh
go test ./...                     # unit and integration tests
docker compose up -d              # the S3 tests need the local object store;
go test -race ./...               #   without it they are skipped, not failed
go test -tags e2e -timeout 15m ./e2e   # builds the image and tests the real containers
```

The tests include crash simulations (a child process killed with `SIGKILL` in the middle of a manifest transaction), concurrency tests under the race detector, and a fuzz test of the segment decoder.

## Dataset credit

The benchmarks use the BGL log dataset: logs from the BlueGene/L supercomputer at Lawrence Livermore National Laboratory, distributed by [LogHub](https://github.com/logpai/loghub). Adam Oliner and Jon Stearley, "What Supercomputers Say: A Study of Five System Logs", DSN 2007. The dataset is not included in this repository.
