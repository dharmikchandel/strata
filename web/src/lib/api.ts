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

export async function search(params: URLSearchParams, signal: AbortSignal): Promise<SearchResult> {
  const res = await fetch('/api/search?' + params.toString(), { signal });
  const body = await res.json();
  if (!res.ok) throw new Error(body.error || 'search failed');
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
