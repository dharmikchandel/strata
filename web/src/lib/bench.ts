// The measured results, read from bench/results at build time, so the pages
// can only ever show what the benchmark harness (cmd/strata-bench) recorded.
import target16 from '../../../bench/results/target-16mib.json';
import noCompaction from '../../../bench/results/no-compaction.json';
import defaults from '../../../bench/results/default-config.json';

export interface QueryResult {
  name: string;
  description: string;
  n: number;
  errors: number;
  min_ms: number;
  p50_ms: number;
  p99_ms: number;
  max_ms: number;
  avg_segments_considered: number;
  avg_skipped_by_time: number;
  avg_skipped_by_bloom: number;
  avg_segments_scanned: number;
  avg_mb_read: number;
  avg_hits: number;
}

export interface Run {
  environment: { cpu: string; logical_cores: number; memory_mib: number; kernel: string; go: string; docker: string; date: string };
  config: { ingest_streams: number; batch_lines: number; iterations_per_query: number; iterations_for_full_scan_query: number; verify_sample: number; deployment_note: string };
  ingest: {
    lines: number;
    dataset_bytes: number;
    seconds_to_last_ack: number;
    lines_per_sec: number;
    dataset_mb_per_sec: number;
    read_and_parse_only_lines_per_sec: number;
    batch_ack_p50_ms: number;
    batch_ack_p99_ms: number;
    buffer_full_retries: number;
    first_log_time: string;
    last_log_time: string;
  };
  settle: { segments_right_after_ingest: number; segments_after_compaction_settled: number; seconds_until_settled: number; stored_bytes: number; stored_bytes_per_dataset_byte: number };
  verify: { sampled_lines: number; found_exactly: number };
  queries: QueryResult[];
}

export const featured = target16 as unknown as Run; // 73 segments: the same layout as the demo data
export const runs = { noCompaction: noCompaction as unknown as Run, defaults: defaults as unknown as Run };

// The three configurations, ordered by segment size: large merged segments,
// a smaller merge target, and no merging (the segments as sealed, about 5 MiB).
export interface Config {
  id: string;
  label: string; // what was changed
  run: Run;
}
export const configs: Config[] = [
  { id: 'default', label: 'Default: merge to 64 MiB', run: runs.defaults },
  { id: '16mib', label: 'Merge to 16 MiB', run: featured },
  { id: 'off', label: 'Compaction off', run: runs.noCompaction },
];

export const byName = (run: Run, name: string) => run.queries.find((q) => q.name === name);

// The query that reads every segment, as a worst case; the others are selective.
export const FULL_SCAN = 'common word, all history (worst case)';

export const ms = (v: number) => (v >= 1000 ? `${(v / 1000).toFixed(2)} s` : `${Math.round(v)} ms`);
export const segs = (v: number) => (Number.isInteger(v) ? String(v) : v.toFixed(1));
export const cap = (t: string) => t.charAt(0).toUpperCase() + t.slice(1);
