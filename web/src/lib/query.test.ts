import { describe, expect, it } from 'vitest';
import type { Corpus } from './api';
import { emptyForm, filterCount, formFromExample, parseTime, toParams, validate } from './query';
import { formatBytes, plural, shortId } from './format';

describe('toParams', () => {
  it('splits tags on spaces and commas, drops empty fields, and sends times as the API expects', () => {
    const p = toParams({ ...emptyForm, text: '  parity error ', tag: 'level=FATAL, component=APP  ', from: '2005-06-20 13:00', limit: '20' });
    expect(p.get('text')).toBe('parity error');
    expect(p.getAll('tag')).toEqual(['level=FATAL', 'component=APP']);
    expect(p.get('from')).toBe('2005-06-20T13:00');
    expect(p.has('to')).toBe(false);
    expect(p.get('limit')).toBe('20');
  });
});

describe('formFromExample', () => {
  it('resolves "last N seconds" against the given clock, in UTC to the minute', () => {
    const now = Date.UTC(2026, 9, 24, 6, 30, 45);
    const f = formFromExample({ title: '', note: '', text: 'error', tags: null, from: 'x', to: 'y', last_seconds: 300, limit: 20 }, now);
    expect(f.from).toBe('2026-10-24 06:25');
    expect(f.to).toBe('');
    expect(f.limit).toBe('20');
  });

  it('sets every field, so nothing leaks from a previous example', () => {
    const f = formFromExample({ title: '', note: '', text: '', tags: { level: 'FATAL' }, from: '2005-06-20T13:00', to: '', limit: 0 });
    expect(f).toEqual({ text: '', tag: 'level=FATAL', from: '2005-06-20 13:00', to: '', limit: '50' });
    expect(filterCount(f)).toBe(2);
  });
});

describe('validate', () => {
  const corpus: Corpus = { segments: 73, entries: 1, stored_bytes: 1, first: '2005-06-03 15:42', last: '2006-01-04 08:00', min_input: '2005-06-03T15:42', max_input: '2006-01-04T08:01' };
  const check = (f: Partial<typeof emptyForm>) => validate({ ...emptyForm, ...f }, corpus);

  it('accepts dates, dates with times, and empty fields', () => {
    expect(check({})).toBeNull();
    expect(check({ from: '2005-06-20', to: '2005-06-20 14:00:30' })).toBeNull();
    expect(parseTime('2005-06-20T13:00')).toBe(Date.UTC(2005, 5, 20, 13, 0));
  });

  it('names the field and the problem, in UTC', () => {
    expect(check({ from: '06/20/2005' })).toMatchObject({ field: 'from', message: expect.stringContaining('YYYY-MM-DD HH:MM') });
    expect(check({ from: '2005-02-30' })?.field).toBe('from');
    expect(check({ from: '2005-07-01 00:00', to: '2005-06-01 00:00' })).toEqual({ field: 'to', message: 'To is not after From. Pick a To later than 2005-07-01 00:00 UTC.' });
    expect(check({ from: '2007-01-01' })?.message).toContain('2005-06-03 15:42 to 2006-01-04 08:00 UTC');
    expect(check({ to: '2001-01-01' })?.field).toBe('to');
    expect(check({ limit: '0' })?.field).toBe('limit');
    expect(check({ limit: '1.5' })?.field).toBe('limit');
  });
});

describe('format', () => {
  it('formats bytes and plurals', () => {
    expect(formatBytes(512)).toBe('512 B');
    expect(formatBytes(5 * 1024 * 1024)).toBe('5.0 MiB');
    expect(plural(1, 'segment')).toBe('1 segment');
    expect(plural(1234, 'segment')).toBe('1,234 segments');
    expect(shortId('0123456789abcdef')).toBe('…89abcdef');
  });
});
