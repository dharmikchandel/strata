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

export interface Overview {
  corpus: Corpus | null; // null when nothing is stored yet
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
