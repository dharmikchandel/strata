import { describe, expect, it } from 'vitest';
import type { SegmentKind } from './api';
import { avoided, plan } from './stages';

const kinds = (spec: string): SegmentKind[] => [...spec].map((c) => ({ t: 'time', b: 'bloom', r: 'scanned' })[c] as SegmentKind);

describe('plan', () => {
  it('plays time skips, then bloom skips, then reads, each left to right', () => {
    const p = plan(kinds('rtbtr'));
    // time bars first (positions 1 and 3), then bloom (2), then reads (0 and 4)
    expect(p.delays[1]).toBe(0);
    expect(p.delays[3]).toBeGreaterThan(p.delays[1]);
    expect(p.delays[2]).toBeGreaterThanOrEqual(p.ends.time);
    expect(p.delays[0]).toBeGreaterThanOrEqual(p.ends.bloom);
    expect(p.delays[4]).toBeGreaterThan(p.delays[0]);
    expect(p.ends.time).toBeLessThan(p.ends.bloom);
    expect(p.ends.bloom).toBeLessThan(p.ends.scanned);
  });

  it('gives an empty stage no time', () => {
    const p = plan(kinds('bbr'));
    expect(p.ends.time).toBe(0);
    expect(p.delays[0]).toBe(0);
    // nothing skipped at all: the reads start at once
    expect(plan(kinds('rr')).delays[0]).toBe(0);
  });

  it('takes no longer for many segments than for a few', () => {
    const few = plan(kinds('t'.repeat(20) + 'b'.repeat(20) + 'rr'));
    const many = plan(kinds('t'.repeat(150) + 'b'.repeat(150) + 'r'.repeat(40)));
    expect(many.ends.scanned).toBeLessThanOrEqual(1600);
    expect(Math.max(...many.delays)).toBeLessThan(many.ends.scanned);
    expect(few.ends.scanned).toBeLessThanOrEqual(many.ends.scanned);
  });
});

describe('avoided', () => {
  it('rounds down and never claims everything when something was read', () => {
    expect(avoided(26.4, 1016)).toBe('97%');
    expect(avoided(15.3, 1017)).toBe('98%'); // 98.49…
    expect(avoided(5, 1000)).toBe('99.5%'); // a decimal only where it tells apart

    expect(avoided(1, 1_000_000)).toBe('over 99.9%');
    expect(avoided(0, 1016)).toBe('100%');
    expect(avoided(1016, 1016)).toBe('0%');
    expect(avoided(5, 0)).toBeNull();
  });
});
