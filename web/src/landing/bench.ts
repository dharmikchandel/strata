// The measured results, read from bench/results at build time, so the page can
// only ever show what the benchmark harness recorded.
import target16 from '../../../bench/results/target-16mib.json';
import noCompaction from '../../../bench/results/no-compaction.json';
import defaults from '../../../bench/results/default-config.json';

export interface QueryResult {
  name: string;
  description: string;
  n: number;
  p50_ms: number;
  p99_ms: number;
  avg_segments_considered: number;
  avg_segments_scanned: number;
  avg_mb_read: number;
}

interface Run {
  environment: { cpu: string; logical_cores: number; memory_mib: number; date: string };
  config: { iterations_per_query: number; iterations_for_full_scan_query: number; deployment_note: string };
  ingest: { lines: number; dataset_bytes: number };
  queries: QueryResult[];
}

export const featured = target16 as unknown as Run; // 73 segments: the same layout as the demo data
export const runs = { noCompaction: noCompaction as unknown as Run, defaults: defaults as unknown as Run };

export const byName = (run: Run, name: string) => run.queries.find((q) => q.name === name);
