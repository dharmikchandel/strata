// What the design page says, with the test behind each claim. Each test is
// written as a t(name, file) call with string literals; a Go test
// (TestDesignPageNamesRealTests in cmd/stratactl) checks that every one
// exists, so a renamed test can't leave a dead reference here.

export interface TestRef {
  name: string;
  file: string;
}
const t = (name: string, file: string): TestRef => ({ name, file });

export interface Step {
  id: string;
  title: string;
  body: string[];
  tests: TestRef[];
}

export const steps: Step[] = [
  {
    id: 'arrive',
    title: 'A line arrives',
    body: [
      'Clients send batches of lines over a gRPC stream and get a reply for each batch, so after a dropped connection a client knows exactly which batches were taken. A batch is accepted whole or refused whole, with a reason.',
      'Accepted means held in memory. It is not yet durable, and not yet searchable.',
    ],
    tests: [
      t('TestAddBatchIsAllOrNothing', 'internal/ingest/buffer_test.go'),
      t('TestGRPCInvalidBatchIsRejectedAtomically', 'internal/ingest/grpc_test.go'),
      t('TestGRPCManyConcurrentClients', 'internal/ingest/grpc_test.go'),
    ],
  },
  {
    id: 'seal',
    title: 'It is sealed into a segment',
    body: [
      'When about 4 MiB has accumulated, or the oldest line has waited 5 seconds, the buffer seals what it holds into one immutable segment file, uploads it, and only then records it in the manifest. A segment is never edited afterwards.',
      'If the upload or the record fails, the lines go back into the buffer and are tried again; an uploaded file that could not be recorded is deleted. If storage stays down, the buffer fills and clients are told to back off instead of the server running out of memory.',
    ],
    tests: [
      t('TestConcurrentWriters', 'internal/ingest/buffer_test.go'),
      t('TestFailedSealLosesNothing', 'internal/ingest/buffer_test.go'),
      t('TestManifestOutageLosesNothing', 'internal/ingest/buffer_test.go'),
      t('TestGRPCBackpressureAndRecovery', 'internal/ingest/grpc_test.go'),
    ],
  },
  {
    id: 'segment',
    title: 'The segment carries its own index',
    body: [
      'One file: a fixed header (line count, time range, offsets, and two checksums), the lines sorted by time with an offset table, an inverted index from each word to the lines holding it, and a bloom filter over every word in the segment.',
      'Every read is checked: a flipped byte or a cut-short file is reported as damaged, never read as data. The bloom filter can wrongly say "maybe" (tuned to about 1%), never wrongly "no".',
    ],
    tests: [
      t('TestIndexAndBloomMatchContent', 'internal/segment/segment_test.go'),
      t('TestDecodeRejectsCorruption', 'internal/segment/segment_test.go'),
      t('FuzzDecode', 'internal/segment/segment_test.go'),
      t('TestNoFalseNegatives', 'internal/bloom/bloom_test.go'),
      t('TestFalsePositiveRateIsNearTarget', 'internal/bloom/bloom_test.go'),
    ],
  },
  {
    id: 'manifest',
    title: 'The manifest decides what exists',
    body: [
      'A SQLite file with one row per segment: its time range, size, how many merges it has been through, and its bloom filter. A file in the bucket that the manifest doesn\'t list is invisible to searches, which is what makes half-finished uploads harmless.',
      'Changes are transactions, written to disk before they count. The manifest and the bucket share an identity, so a lost manifest stops the server instead of letting cleanup delete every segment as unrecorded.',
    ],
    tests: [
      t('TestCrashMidTransactionLeavesManifestUnchanged', 'internal/manifest/crash_test.go'),
      t('TestReadersNeverSeeAHalfFinishedSwap', 'internal/manifest/manifest_test.go'),
      t('TestLostManifestIsRefusedInsteadOfDestroyingData', 'internal/app/app_test.go'),
    ],
  },
  {
    id: 'search',
    title: 'A search reads only what it must',
    body: [
      'Three filters, cheapest first. The time range, answered by one manifest query. The bloom filters, kept in the manifest, checked in memory. Only the segments that survive both are downloaded and searched with their own index.',
      'Each filter may only be wrong in one direction: it can keep a segment that has no match, costing a read, but never drop one that has a match. Whatever is skipped, the answer is the same as reading everything.',
    ],
    tests: [
      t('TestNarrowTimeRangeSkipsSegments', 'internal/query/query_test.go'),
      t('TestBloomSkipsSegmentsWithoutTheTerm', 'internal/query/query_test.go'),
      t('TestAgreesWithBruteForce', 'internal/query/query_test.go'),
    ],
  },
  {
    id: 'merge',
    title: 'Small segments are merged',
    body: [
      'Many small segments make every search do more work, so a background job merges them: read the inputs, write the merged segment, read it back and check it, then swap it for the inputs in a single manifest transaction, and only then delete the old files.',
      'A crash before the swap leaves the old segments as the data; after it, the merged one. Never a mix, never a line lost or counted twice. Searches running meanwhile restart if a file they planned to read has been replaced.',
    ],
    tests: [
      t('TestCrashBeforeCommitLosesNothing', 'internal/compact/compact_test.go'),
      t('TestCrashAfterCommitDoesNotDoubleCount', 'internal/compact/compact_test.go'),
      t('TestVerificationCatchesSilentCorruption', 'internal/compact/compact_test.go'),
      t('TestQueriesStayCorrectWhileCompacting', 'internal/compact/compact_test.go'),
    ],
  },
];

export interface Failure {
  when: string;
  then: string;
  tests: TestRef[];
}

export const failures: Failure[] = [
  {
    when: 'The process is stopped (SIGTERM, docker stop)',
    then: 'Stops taking connections, lets streams in flight finish (up to 10 s), seals everything still in memory, then exits.',
    tests: [t('TestBinaryServesAndStopsCleanlyOnSIGTERM', 'cmd/strata/main_test.go'), t('TestShutdownDrainsThenCutsOffStuckStreams', 'internal/app/app_test.go'), t('TestStack', 'e2e/compose_test.go')],
  },
  {
    when: 'Writing a segment to storage fails',
    then: 'The batch goes back into the buffer and is retried; nothing is lost. If storage stays down, clients are told the buffer is full.',
    tests: [t('TestFailedSealLosesNothing', 'internal/ingest/buffer_test.go'), t('TestGRPCBackpressureAndRecovery', 'internal/ingest/grpc_test.go')],
  },
  {
    when: 'Recording the segment fails after the upload',
    then: 'The uploaded file is deleted and the lines stay in the buffer.',
    tests: [t('TestManifestOutageLosesNothing', 'internal/ingest/buffer_test.go')],
  },
  {
    when: 'The process is killed in the middle of a manifest change',
    then: 'SQLite undoes the unfinished transaction on the next start. Tested by killing a real process with SIGKILL at exact points.',
    tests: [t('TestCrashMidTransactionLeavesManifestUnchanged', 'internal/manifest/crash_test.go'), t('TestCrashAfterCommitIsDurable', 'internal/manifest/crash_test.go')],
  },
  {
    when: 'The process is killed in the middle of a merge',
    then: 'Before the swap the old segments are the data; after it the merged one is. Leftover files are cleaned up later.',
    tests: [t('TestCrashBeforeCommitLosesNothing', 'internal/compact/compact_test.go'), t('TestCrashAfterCommitDoesNotDoubleCount', 'internal/compact/compact_test.go')],
  },
  {
    when: 'A write "succeeds" but the stored bytes are wrong',
    then: 'The merged segment is read back and checked before the swap, so the originals are never replaced by a bad file.',
    tests: [t('TestVerificationCatchesSilentCorruption', 'internal/compact/compact_test.go')],
  },
  {
    when: 'A segment is damaged or missing',
    then: 'A search that needs it fails with an error naming it, rather than an answer that looks complete but isn\'t. Compaction sets it aside and merges the rest.',
    tests: [t('TestCorruptSegmentFailsTheQuery', 'internal/query/query_test.go'), t('TestMissingSegmentEventuallyErrors', 'internal/query/query_test.go'), t('TestUnreadableInputIsSkippedNotFatal', 'internal/compact/compact_test.go')],
  },
  {
    when: 'A merge replaces a segment while a search is reading',
    then: 'The search notices the missing file and starts again from a fresh view of the manifest. Every search sees every line exactly once.',
    tests: [t('TestQueryRestartsWhenSegmentIsReplacedMidQuery', 'internal/query/query_test.go'), t('TestQueriesStayCorrectWhileCompacting', 'internal/compact/compact_test.go')],
  },
  {
    when: 'Cleanup meets a segment still being written',
    then: 'Unrecorded files younger than 10 minutes are never deleted: a segment is uploaded a moment before it is recorded.',
    tests: [t('TestGCOnlyRemovesWhatItShould', 'internal/compact/compact_test.go')],
  },
  {
    when: 'The manifest file is lost but the bucket survives',
    then: 'Without protection, cleanup would treat every real segment as unrecorded and delete it. The server refuses to start instead, and says why.',
    tests: [t('TestGCTrustsItsManifestSoAWrongOneDestroysData', 'internal/compact/compact_test.go'), t('TestLostManifestIsRefusedInsteadOfDestroyingData', 'internal/app/app_test.go')],
  },
  {
    when: 'A client sends a bad or oversized batch',
    then: 'Refused whole, with a status that says why; the stream and other clients carry on.',
    tests: [t('TestGRPCInvalidBatchIsRejectedAtomically', 'internal/ingest/grpc_test.go'), t('TestGRPCOversizedMessageIsRefused', 'internal/ingest/grpc_test.go')],
  },
  {
    when: 'A bug panics inside a request',
    then: 'Caught for that request; it can\'t take the server down.',
    tests: [t('TestRecoverInterceptorTurnsPanicIntoInternalError', 'internal/ingest/grpc_test.go')],
  },
  {
    when: 'Too many searches at once',
    then: 'Refused at once with "busy" rather than queued, since a search can hold whole segments in memory.',
    tests: [t('TestSearchRPCShedsLoadBeyondTheConcurrencyLimit', 'internal/query/grpc_test.go')],
  },
];

export const decisions: [string, string][] = [
  ['Go', 'Goroutines fit concurrent ingest and parallel segment fetches, and the result is one static binary.'],
  ['gRPC and Protobuf', 'Streams suit continuous log shipping, and a reply per batch tells a client exactly what was accepted.'],
  ['SQLite for the manifest', 'Real transactions (the merge swap is genuinely atomic) without running a database server. The cost: one file on one machine, no replication.'],
  ['S3-compatible storage', 'Developed against a local RustFS server through the plain S3 API, so a cloud bucket is a configuration change. The same storage tests run against the real server and an in-memory fake.'],
  ['Bloom filters in the manifest', 'Checking one costs no download. If skipping a segment needed a request to storage, nothing would have been saved.'],
  ['No edits or deletes of single lines', 'Segments are immutable. That is a design choice, and it is what makes reads lock-free and crash recovery simple.'],
];

export const limits: [string, string][] = [
  ['Accepted is not durable', 'A batch is acknowledged once it is in memory and stored within about 5 seconds. If the process is killed (not stopped), lines not yet sealed are lost. There is no write-ahead log.'],
  ['A resent batch may be stored twice', 'If a connection breaks while a batch is unacknowledged, the client resends it.'],
  ['No TLS and no authentication', 'Strata listens on loopback by default.'],
  ['Single node', 'One server per bucket; no clustering or replication. The manifest must be backed up: a lost one is detected, not rebuilt.'],
  ['Whole-segment reads', 'A search downloads every segment it needs in full; that is the main cost in the benchmarks.'],
  ['Simple search', 'All words, in any order, plus exact tags and a time range. No phrases, OR, NOT or regular expressions.'],
];
