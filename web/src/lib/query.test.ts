import { describe, expect, it } from 'vitest';
import type { Corpus } from './api';
import { SearchError } from './api';
import { explain } from './errors';
import { emptyQuery, fromURL, parse, parseTime, queryFromExample, rangeLabel, toApiParams, toURL, validate, type Query } from './query';
import { formatBytes, plural, shortId } from './format';
import { highlight, lowerLikeGo, terms, tokenize } from './tokenize';

const corpus: Corpus = { segments: 73, entries: 1, stored_bytes: 1, first: '2005-06-03 15:42', last: '2006-01-04 08:00', min_input: '2005-06-03T15:42', max_input: '2006-01-04T08:01' };
const q = (over: Partial<Query>): Query => ({ ...emptyQuery, ...over });

describe('parse', () => {
  it('takes key=value pieces as tags and everything else as words', () => {
    expect(parse('  parity  error level=FATAL component=APP ')).toEqual({ words: 'parity error', tags: [['level', 'FATAL'], ['component', 'APP']] });
    // No key, so it's a word; a value may contain "=".
    expect(parse('=x a=b=c')).toEqual({ words: '=x', tags: [['a', 'b=c']] });
  });
});

describe('toApiParams', () => {
  it('sends words, tags, times and the limit as the API expects', () => {
    const p = toApiParams(q({ q: 'parity error level=FATAL', range: { kind: 'between', from: '2005-06-20 13:00', to: '' }, limit: 20 }), corpus);
    expect(p.get('text')).toBe('parity error');
    expect(p.getAll('tag')).toEqual(['level=FATAL']);
    expect(p.get('from')).toBe('2005-06-20T13:00');
    expect(p.has('to')).toBe(false);
    expect(p.get('limit')).toBe('20');
  });

  it('counts "last" back from the newest stored line, not the clock', () => {
    const p = toApiParams(q({ q: 'error', range: { kind: 'last', seconds: 3600 } }), corpus, Date.UTC(2026, 9, 24));
    expect(p.get('from')).toBe('2006-01-04T07:01:00');
    // With nothing stored there is no newest line, so the clock is used.
    expect(toApiParams(q({ range: { kind: 'last', seconds: 300 } }), null, Date.UTC(2026, 9, 24, 6, 30)).get('from')).toBe('2026-10-24T06:25:00');
  });
});

describe('URL form', () => {
  it('round-trips every kind of range and leaves defaults out', () => {
    for (const query of [
      q({ q: 'parity error level=FATAL' }),
      q({ q: 'error', range: { kind: 'last', seconds: 3600 }, limit: 20 }),
      q({ range: { kind: 'between', from: '2005-06-20 13:00', to: '2005-06-20 14:00' } }),
      q({ range: { kind: 'between', from: '', to: '2005-06-20 14:00' }, limit: 1000 }),
    ]) {
      expect(fromURL(new URLSearchParams(toURL(query).toString()))).toEqual(query);
    }
    expect(toURL(emptyQuery).toString()).toBe('');
    expect(toURL(q({ q: 'error', range: { kind: 'last', seconds: 7 * 86400 } })).toString()).toBe('q=error&last=7d');
  });

  it('falls back to defaults for anything malformed', () => {
    expect(fromURL(new URLSearchParams('last=forever&limit=5000'))).toEqual(emptyQuery);
    expect(fromURL(new URLSearchParams('limit=1.5')).limit).toBe(50);
  });
});

describe('queryFromExample', () => {
  it('puts words and tags in the box and keeps "last" relative', () => {
    expect(queryFromExample({ title: '', note: '', text: '', tags: { level: 'FATAL' }, from: '', to: '', last_seconds: 3600, limit: 20 })).toEqual({ q: 'level=FATAL', range: { kind: 'last', seconds: 3600 }, limit: 20 });
    expect(queryFromExample({ title: '', note: '', text: 'error', tags: null, from: '2005-06-20T13:00', to: '2005-06-20T14:00', limit: 0 })).toEqual({ q: 'error', range: { kind: 'between', from: '2005-06-20 13:00', to: '2005-06-20 14:00' }, limit: 50 });
  });

  it('labels ranges briefly', () => {
    expect(rangeLabel({ kind: 'all' })).toBe('All time');
    expect(rangeLabel({ kind: 'last', seconds: 900 })).toBe('Last 15m');
    expect(rangeLabel({ kind: 'between', from: '2005-06-20 13:00', to: '2005-06-20 14:00' })).toBe('2005-06-20 13:00 – 14:00');
    expect(rangeLabel({ kind: 'between', from: '2005-06-20 13:00', to: '' })).toBe('From 2005-06-20 13:00');
  });
});

describe('validate', () => {
  const between = (from: string, to: string) => validate(q({ range: { kind: 'between', from, to } }), corpus);

  it('accepts dates, dates with times, and open ends', () => {
    expect(validate(emptyQuery, corpus)).toBeNull();
    expect(between('2005-06-20', '2005-06-20 14:00:30')).toBeNull();
    expect(parseTime('2005-06-20T13:00')).toBe(Date.UTC(2005, 5, 20, 13, 0));
  });

  it('names the field and the problem, in UTC', () => {
    expect(between('06/20/2005', '')).toMatchObject({ field: 'from', message: expect.stringContaining('YYYY-MM-DD HH:MM') });
    expect(between('2005-02-30', '')?.field).toBe('from');
    expect(between('2005-07-01 00:00', '2005-06-01 00:00')).toEqual({ field: 'to', message: 'To is not after From. Pick a To later than 2005-07-01 00:00 UTC.' });
    expect(between('2007-01-01', '')?.message).toContain('2005-06-03 15:42 to 2006-01-04 08:00 UTC');
    expect(between('', '2001-01-01')?.field).toBe('to');
  });

  it('refuses two values for one tag, which no line can have', () => {
    expect(validate(q({ q: 'level=FATAL error level=INFO' }), corpus)).toMatchObject({ field: 'q', message: expect.stringContaining("can't both be true") });
    expect(validate(q({ q: 'level=FATAL level=FATAL' }), corpus)).toBeNull();
  });
});

describe('tokenize', () => {
  it('splits like the server: lowercase runs of letters and digits', () => {
    // The same case as internal/segment's TestTokenize.
    expect(tokenize('Connection RESET by peer: 10.0.0.1, user=Zoë_42')).toEqual(['connection', 'reset', 'by', 'peer', '10', '0', '0', '1', 'user', 'zoë', '42']);
    expect(terms('LAPACK_81117d lapack')).toEqual(['lapack', '81117d']);
  });

  it('lowercases one code point at a time, as Go does', () => {
    expect(lowerLikeGo('ΟΔΟΣ')).toBe('οδοσ'); // not the context-dependent final "ς"
    expect(lowerLikeGo('İSTANBUL')).toBe('istanbul');
  });

  it('marks whole runs only and keeps the original text', () => {
    const pieces = highlight('ciod: Error loading /p/hangtest_062005, ERRORS', new Set(['error', 'hangtest']));
    expect(pieces.map((p) => p.text).join('')).toBe('ciod: Error loading /p/hangtest_062005, ERRORS');
    expect(pieces.filter((p) => p.hit).map((p) => p.text)).toEqual(['Error', 'hangtest']);
    expect(highlight('x', new Set())).toEqual([{ text: 'x', hit: false }]);
  });
});

describe('explain', () => {
  it('rewrites server errors into plain words, and says whether to retry', () => {
    expect(explain(new SearchError(400, 'time range must have 0 <= from < to')).message).toBe('The time range is empty: its start is not before its end.');
    expect(explain(new SearchError(429, 'the server is busy; try again in a moment'))).toMatchObject({ retry: true });
    expect(explain(new SearchError(502, 'some stored data could not be read'))).toMatchObject({ retry: false, message: expect.stringContaining('incomplete') });
    expect(explain(new SearchError(504, 'the search took too long')).message).toContain('narrow the time range');
    expect(explain(new SearchError(0, 'unreachable'))).toMatchObject({ retry: true });
    expect(explain(new SearchError(400, 'something new')).message).toBe('The server refused this search: something new');
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
