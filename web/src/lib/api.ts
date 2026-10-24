// Shapes of the two JSON endpoints served by `stratactl ui` (cmd/stratactl/ui.go
// and overview.go). Everything in them is untrusted text: render it as text only.

export type SegmentKind = 'time' | 'bloom' | 'scanned';

export interface Hit {
  time: string; // "2006-01-02 15:04:05.000000", UTC
  message: string;
  tags?: Record<string, string>;
  segment_id: string;
}

export interface Metrics {
  segments_considered: number;
  skipped_by_time: number;
  skipped_by_bloom: number;
  segments_scanned: number;
  bytes_read: number;
  retries: number;
  server_micros: number;
}

export interface SegmentOutcome {
  kind: SegmentKind;
  id: string;
  first: string; // "2006-01-02 15:04", UTC
  last: string;
  size: number;
  bytes_read: number;
  hits: number; // lines in this response that came from it
}

export interface SearchResult {
  hits: Hit[];
  truncated: boolean;
  metrics?: Metrics;
  round_trip_ms: number;
  // Every segment, oldest first; empty (with segments_truncated) when there
  // were too many to list.
  segments: SegmentOutcome[];
  segments_truncated: boolean;
  // Every matching line by time, not only the returned ones; null when no
  // segment overlaps the time range.
  histogram: Histogram | null;
}

// Equal buckets from start_ms, each bucket_ms wide. Counted in the segments
// that were read; a skipped segment holds no match, so this is every match.
export interface Histogram {
  start_ms: number;
  bucket_ms: number;
  counts: number[];
}

export interface Corpus {
  name?: string;
  segments: number;
  entries: number;
  stored_bytes: number;
  first: string;
  last: string;
  min_input: string; // bounds for the date inputs, "2005-06-03T15:42"
  max_input: string;
}

export interface Example {
  title: string;
  note: string;
  text: string;
  tags: Record<string, string> | null;
  from: string;
  to: string;
  last_seconds?: number;
  limit: number;
}

// A tag key and the values stored for it (capped: more exist than are listed).
export interface TagValues {
  key: string;
  values: string[];
  capped: boolean;
}

export interface Overview {
  corpus: Corpus | null; // null when nothing is stored yet
  tags?: TagValues[];
  examples: Example[] | null;
  credit?: string;
  about_url?: string;
}

// A search that didn't produce a result. status is the HTTP status, or 0 when
// no answer came back at all; message is the server's (safe) error text.
export class SearchError extends Error {
  constructor(
    readonly status: number,
    message: string,
  ) {
    super(message);
    this.name = 'SearchError';
  }
}

export async function search(params: URLSearchParams, signal: AbortSignal): Promise<SearchResult> {
  let res: Response;
  try {
    res = await fetch('/api/search?' + params.toString(), { signal });
  } catch (err) {
    if ((err as Error).name === 'AbortError') throw err;
    throw new SearchError(0, 'unreachable');
  }
  const body = await res.json().catch(() => ({}));
  if (!res.ok) throw new SearchError(res.status, typeof body.error === 'string' ? body.error : '');
  return body as SearchResult;
}

// Returns null when the overview can't be had: the page works without it.
export async function fetchOverview(): Promise<Overview | null> {
  try {
    const res = await fetch('/api/overview');
    if (!res.ok) return null;
    return (await res.json()) as Overview;
  } catch {
    return null;
  }
}

// /api/system (cmd/stratactl/system.go): the segments as they are now, what
// the server has done since the last call, and its vital signs.
export interface SystemSegment {
  id: string;
  size: number;
  lines: number;
  first: string; // first and last log time, "2006-01-02 15:04" UTC
  last: string;
  generation: number; // 0: written by ingest; n: n merges deep
  created: string; // when it was written, "2006-01-02 15:04:05.000" UTC
}

export interface SystemEvent {
  seq: number;
  at: string; // "2006-01-02 15:04:05.000" UTC
  kind: 'sealed' | 'merged' | 'cleaned';
  segment?: string; // sealed: the new segment; merged: the merged one
  inputs?: string[]; // merged: what it replaced
  lines?: number;
  bytes?: number;
  bytes_in?: number;
  files?: number; // cleaned
}

export interface System {
  segments: SystemSegment[]; // oldest first; at most the newest 2,000
  segments_total: number;
  events: SystemEvent[]; // after the seq asked for, oldest first
  started: string; // when the server (and its event log) started
  lines_per_sec: number;
  buffer: { bytes: number; seal_at: number; seal_after_ms: number; limit: number };
  compaction: boolean;
}

// Null when the system view can't be had (server down): the page says so.
export async function fetchSystem(after: number): Promise<System | null> {
  try {
    const res = await fetch('/api/system?after=' + after);
    if (!res.ok) return null;
    return (await res.json()) as System;
  } catch {
    return null;
  }
}
