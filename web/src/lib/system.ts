import type { SystemEvent, SystemSegment } from './api';
import { formatBytes, n, shortId } from './format';

// Block widths for the bucket view: each block as wide as its file is large,
// on one scale for all of them, so a merged segment is visibly the sum of its
// inputs. The scale lays the blocks out in roughly `rows` rows of `width`;
// a block is never thinner than `min` (it must stay visible and pointable)
// nor wider than a row.
export function blockWidths(sizes: number[], width: number, rows: number, min = 4): number[] {
  const total = sizes.reduce((a, b) => a + b, 0);
  if (total <= 0 || width <= 0) return sizes.map(() => min);
  const perByte = (width * rows) / total;
  return sizes.map((s) => Math.min(width, Math.max(min, Math.round(s * perByte))));
}

// How many rows the bucket view aims for: more segments, more rows, so each
// block keeps some width.
export const rowsFor = (count: number) => Math.min(12, Math.max(1, Math.ceil(count / 30)));

// One event in plain words.
export function describe(e: SystemEvent): string {
  switch (e.kind) {
    case 'sealed':
      return `Sealed segment ${shortId(e.segment ?? '')}: ${n(e.lines ?? 0)} line${e.lines === 1 ? '' : 's'}, ${formatBytes(e.bytes ?? 0)}.`;
    case 'merged':
      return `Merged ${n(e.inputs?.length ?? 0)} segments (${formatBytes(e.bytes_in ?? 0)}) into ${shortId(e.segment ?? '')}: ${n(e.lines ?? 0)} lines, ${formatBytes(e.bytes ?? 0)}.`;
    case 'cleaned':
      return `Cleaned up ${n(e.files ?? 0)} file${e.files === 1 ? '' : 's'} nothing referred to any more.`;
  }
}

// What a generation means, in words.
export function generationText(g: number): string {
  if (g === 0) return 'written by ingest, never merged';
  if (g === 1) return 'merged once';
  return `${n(g)} merges deep`;
}

// The segments to draw: the current ones, plus those that just went away
// (replaced by a merge), which stay a moment in their old place so the eye
// can see what the merge took. Both lists are oldest first by first log time.
export function withLeaving(current: SystemSegment[], leaving: SystemSegment[]): SystemSegment[] {
  if (leaving.length === 0) return current;
  const out = [...current, ...leaving];
  const key = (g: SystemSegment) => g.first + '|' + g.id;
  return out.sort((a, b) => (key(a) < key(b) ? -1 : key(a) > key(b) ? 1 : 0));
}
