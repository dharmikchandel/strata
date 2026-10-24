import type { Example, SearchResult } from './api';
import { formatBytes } from './format';

// The landing page shows a search the way `stratactl search` prints it
// (cmd/stratactl/search.go), filled with a real response from /api/search.
// Keep this in step with that file's output format.

const rfc3339 = (s: string) => (s.length === 16 ? s + ':00' : s) + 'Z';

export function cliCommand(ex: Example, limit: number): string {
  const parts = ['stratactl search'];
  if (ex.text) parts.push(`-text ${/\s/.test(ex.text) ? JSON.stringify(ex.text) : ex.text}`);
  for (const [k, v] of Object.entries(ex.tags ?? {})) parts.push(`-tag ${k}=${v}`);
  if (ex.from && !ex.last_seconds) parts.push(`-from ${rfc3339(ex.from)}`);
  if (ex.to && !ex.last_seconds) parts.push(`-to ${rfc3339(ex.to)}`);
  parts.push(`-limit ${limit}`);
  return parts.join(' ');
}

// Go prints a time.Duration rounded to 10µs as e.g. "113.45ms" or "2.07508s".
function goDuration(micros: number, roundTo: number): string {
  const us = Math.round(micros / roundTo) * roundTo;
  if (us >= 1_000_000) return `${+(us / 1_000_000).toFixed(6)}s`;
  if (us >= 1000) return `${+(us / 1000).toFixed(3)}ms`;
  return `${us}µs`;
}

// The 60-column bar: '.' skipped by time, '-' skipped by bloom, '#' scanned.
function bar(considered: number, time: number, bloom: number, scanned: number): string {
  if (considered === 0) return '(no segments yet)';
  const scale = (v: number) => Math.floor((v / considered) * 60);
  let sc = scale(scanned);
  if (scanned > 0 && sc === 0) sc = 1;
  return '[' + '.'.repeat(scale(time)) + '-'.repeat(scale(bloom)) + '#'.repeat(sc) + ']  . time  - bloom  # scanned';
}

// Go's %v of a map[string]string: keys sorted, "map[k:v k:v]".
const goMap = (m: Record<string, string>) =>
  'map[' + Object.keys(m).sort().map((k) => `${k}:${m[k]}`).join(' ') + ']';

export function cliOutput(r: SearchResult): string[] {
  const lines: string[] = [];
  for (const h of r.hits) {
    const t = h.time.replace(' ', 'T') + 'Z';
    lines.push(`${t}  ${h.message}` + (h.tags && Object.keys(h.tags).length ? `  ${goMap(h.tags)}` : ''));
  }
  if (r.truncated) lines.push(`... more lines match; showing the earliest ${r.hits.length} (raise -limit)`);
  if (r.hits.length === 0) lines.push('(no matches)');
  lines.push('');
  const m = r.metrics;
  if (!m) return lines;
  lines.push(
    `segments  considered ${m.segments_considered} | skipped by time ${m.skipped_by_time} | skipped by bloom filter ${m.skipped_by_bloom} | scanned ${m.segments_scanned}`,
  );
  lines.push('          ' + bar(m.segments_considered, m.skipped_by_time, m.skipped_by_bloom, m.segments_scanned));
  lines.push(
    `read      ${formatBytes(m.bytes_read)} from storage | server time ${goDuration(m.server_micros, 10)} | round trip ${goDuration(Math.round(r.round_trip_ms * 1000), 100)}`,
  );
  if (m.retries > 0) lines.push(`          (restarted ${m.retries} time(s): a segment was replaced during the search)`);
  return lines;
}
