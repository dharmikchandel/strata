import type { SegmentKind } from './api';

// The order a search rules segments out, cheapest first: by time range (one
// manifest lookup), by bloom filter (an in-memory check), then the reads.
// The strip plays a result in this order once, so the visitor sees each
// filter remove its share before the reads stand up.
export const STAGES: SegmentKind[] = ['time', 'bloom', 'scanned'];

const SETTLE = 220; // ms for one skipped bar to drop into place
const RISE = 500; // ms for one read bar to stand up
const SPREAD = 180; // ms at most between a stage's first and last bar starting
const STEP = 12; // ms between neighbouring bars, when few enough to fit SPREAD
const GAP = 40; // ms between one stage ending and the next starting

export interface Plan {
  // When each segment's bar starts moving, in strip order (oldest first).
  delays: number[];
  // When each stage has finished: its count is shown from then on. A stage
  // with no segments takes no time and ends where the one before it did.
  ends: Record<SegmentKind, number>;
}

// A stage's bars start left to right, STEP apart, squeezed into SPREAD when
// there are many, so 196 bars take no longer than 3.
export function plan(kinds: SegmentKind[]): Plan {
  const count: Record<SegmentKind, number> = { time: 0, bloom: 0, scanned: 0 };
  for (const k of kinds) count[k]++;

  const start = {} as Record<SegmentKind, number>;
  const ends = {} as Record<SegmentKind, number>;
  let t = 0;
  for (const k of STAGES) {
    start[k] = t;
    if (count[k] === 0) {
      ends[k] = t;
      continue;
    }
    const spread = Math.min((count[k] - 1) * STEP, SPREAD);
    ends[k] = t + spread + (k === 'scanned' ? RISE : SETTLE);
    t = ends[k] + GAP;
  }

  const seen: Record<SegmentKind, number> = { time: 0, bloom: 0, scanned: 0 };
  const delays = kinds.map((k) => {
    const i = seen[k]++;
    const spread = Math.min((count[k] - 1) * STEP, SPREAD);
    return Math.round(start[k] + (count[k] > 1 ? (i / (count[k] - 1)) * spread : 0));
  });
  return { delays, ends };
}

// Share of the stored bytes that a search did not download, as text. It is
// rounded down, so it never overstates, and never says 100% when anything
// was read. Null when there is nothing to compare against.
export function avoided(read: number, stored: number): string | null {
  if (stored <= 0 || read > stored) return null;
  const pct = (1 - read / stored) * 100;
  if (read === 0) return '100%';
  if (pct >= 99.9) return 'over 99.9%';
  if (pct >= 99) return `${Math.floor(pct * 10) / 10}%`;
  return `${Math.floor(pct)}%`;
}
