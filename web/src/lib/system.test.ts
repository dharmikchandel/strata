import { describe as group, expect, it } from 'vitest';
import type { SystemSegment } from './api';
import { blockWidths, describe, generationText, rowsFor, withLeaving } from './system';

const seg = (id: string, first: string): SystemSegment => ({ id, first, last: first, size: 1, lines: 1, generation: 0, created: '' });

group('blockWidths', () => {
  it('puts every block on one scale, with a floor and a ceiling', () => {
    // 4 bytes over 2 rows of 100 px: 50 px a byte; the 150 px block is held to one row.
    expect(blockWidths([3, 1], 100, 2)).toEqual([100, 50]);
    expect(blockWidths([1000, 1], 100, 1)).toEqual([100, 4]); // never thinner than 4px
    expect(blockWidths([5], 0, 1)).toEqual([4]);
  });
  it('aims for more rows as segments add up', () => {
    expect([rowsFor(1), rowsFor(31), rowsFor(5000)]).toEqual([1, 2, 12]);
  });
});

group('describe', () => {
  it('says what happened in plain words', () => {
    expect(describe({ seq: 1, at: '', kind: 'sealed', segment: '18e1-0123456789', lines: 1000, bytes: 2048 })).toBe('Sealed segment …23456789: 1,000 lines, 2.0 KiB.');
    expect(describe({ seq: 2, at: '', kind: 'merged', segment: 'abcdef0123', inputs: ['a', 'b', 'c'], lines: 3000, bytes: 3072, bytes_in: 4096 })).toBe(
      'Merged 3 segments (4.0 KiB) into …cdef0123: 3,000 lines, 3.0 KiB.',
    );
    expect(describe({ seq: 3, at: '', kind: 'cleaned', files: 1 })).toBe('Cleaned up 1 file nothing referred to any more.');
    expect(generationText(0)).toContain('never merged');
    expect(generationText(3)).toBe('3 merges deep');
  });
});

group('withLeaving', () => {
  it('keeps a replaced segment in its old place for a moment', () => {
    const now = [seg('m', '2026-10-25 09:00'), seg('z', '2026-10-25 09:05')];
    const gone = [seg('a', '2026-10-25 09:01')];
    expect(withLeaving(now, gone).map((g) => g.id)).toEqual(['m', 'a', 'z']);
    expect(withLeaving(now, [])).toBe(now);
  });
});
