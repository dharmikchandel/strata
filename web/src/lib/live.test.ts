import { describe, expect, it } from 'vitest';
import { lineKeys } from '../components/LogViewer';
import { afterNanos, ratePerSecond } from './live';

describe('afterNanos', () => {
  it('is the last nanosecond of the line\'s microsecond, exactly', () => {
    // 2006-01-04 08:00:05.123456 UTC = 1136361605 s
    expect(afterNanos('2006-01-04 08:00:05.123456')).toBe('1136361605123456999');
    expect(afterNanos('2006-01-04 08:00')).toBeNull();
  });
});

describe('ratePerSecond', () => {
  it('needs 10 s of samples, then gives lines per second', () => {
    expect(ratePerSecond([[0, 0], [4000, 400]])).toBeNull();
    expect(ratePerSecond([[0, 1000], [5000, 1000], [20_000, 21_000]])).toBe(1000);
    // Fewer lines than before (compaction never removes lines, but a wiped store does): no rate.
    expect(ratePerSecond([[0, 500], [20_000, 100]])).toBeNull();
  });
});

describe('lineKeys', () => {
  it('names lines by content, telling identical ones apart', () => {
    const a = { time: '2026-10-25 10:00:00.000001', message: 'x', segment_id: 's1' };
    const b = { time: '2026-10-25 10:00:00.000002', message: 'x', segment_id: 's1' };
    const keys = lineKeys([a, b, { ...a, segment_id: 's2' }]);
    expect(new Set(keys).size).toBe(3);
    // The same line keeps its key when others arrive before it.
    expect(lineKeys([b, a])[1]).toBe(keys[0]);
  });
});
