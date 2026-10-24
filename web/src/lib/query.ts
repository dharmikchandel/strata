import type { Corpus, Example } from './api';

// A search as the page holds it: everything typed in the one query box, a
// time range, and how many lines to show. It is a plain object that turns
// into URL parameters and back, so a search can be written to the address bar.
export interface Query {
  q: string; // words and key=value tags, as typed
  range: Range;
  limit: number;
}

// "last" counts back from the newest stored line, not from the visitor's
// clock: with live data that is about now, and with a historical dataset
// (BGL ends in 2006) "the last hour" still means an hour that has data.
export type Range =
  | { kind: 'all' }
  | { kind: 'last'; seconds: number }
  | { kind: 'between'; from: string; to: string }; // "YYYY-MM-DD HH:MM" UTC; either may be empty

export const PRESETS = [
  { label: '15m', seconds: 15 * 60 },
  { label: '1h', seconds: 60 * 60 },
  { label: '24h', seconds: 24 * 60 * 60 },
  { label: '7d', seconds: 7 * 24 * 60 * 60 },
];

export const LIMITS = [20, 50, 100, 500, 1000];
export const MAX_LIMIT = 1000; // the page server's cap (uiMaxLimit in cmd/stratactl/ui.go)

export const emptyQuery: Query = { q: '', range: { kind: 'all' }, limit: 50 };

export interface Parsed {
  words: string; // what goes to the server as text
  tags: [string, string][];
}

// Splits the box into words and tags. A piece shaped key=value (with a
// non-empty key) is a tag; everything else is a word. Words are left as typed:
// the server splits them into terms itself, the same way it indexed the lines.
export function parse(q: string): Parsed {
  const words: string[] = [];
  const tags: [string, string][] = [];
  for (const piece of q.split(/\s+/)) {
    if (!piece) continue;
    const eq = piece.indexOf('=');
    if (eq > 0) tags.push([piece.slice(0, eq), piece.slice(eq + 1)]);
    else words.push(piece);
  }
  return { words: words.join(' '), tags };
}

// Times are shown and typed as "2005-06-20 13:00", the same shape as every
// other time on the page; the API wants "2005-06-20T13:00".
export const displayTime = (s: string) => s.replace('T', ' ');
const apiTime = (s: string) => s.trim().replace(' ', 'T');
export const isoMinute = (ms: number) => new Date(ms).toISOString().slice(0, 16);
const isoSecond = (ms: number) => new Date(ms).toISOString().slice(0, 19);

// The newest stored line, to the minute and rounded up (the overview's
// max_input), which is where "last" ranges count back from.
export function newest(corpus: Corpus | null): number | null {
  return corpus ? parseTime(corpus.max_input) : null;
}

// The parameters for /api/search, with "last" resolved to a start time now.
export function toApiParams(query: Query, corpus: Corpus | null, now: number = Date.now()): URLSearchParams {
  const params = new URLSearchParams();
  const { words, tags } = parse(query.q);
  if (words) params.set('text', words);
  for (const [k, v] of tags) params.append('tag', `${k}=${v}`);
  const r = query.range;
  if (r.kind === 'last') {
    params.set('from', isoSecond((newest(corpus) ?? now) - r.seconds * 1000));
  } else if (r.kind === 'between') {
    if (r.from.trim()) params.set('from', apiTime(r.from));
    if (r.to.trim()) params.set('to', apiTime(r.to));
  }
  params.set('limit', String(query.limit));
  return params;
}

export function durationLabel(seconds: number): string {
  for (const [unit, size] of [['d', 86400], ['h', 3600], ['m', 60]] as const) {
    if (seconds % size === 0) return `${seconds / size}${unit}`;
  }
  return `${seconds}s`;
}

export function parseDuration(s: string): number | null {
  const m = /^(\d{1,6})([smhd])$/.exec(s.trim());
  if (!m || +m[1] === 0) return null;
  return +m[1] * { s: 1, m: 60, h: 3600, d: 86400 }[m[2] as 's' | 'm' | 'h' | 'd'];
}

// The address-bar form: q, then either last=1h or from/to, then limit. Only
// what differs from an empty search is written, so links stay short.
export function toURL(query: Query): URLSearchParams {
  const p = new URLSearchParams();
  if (query.q.trim()) p.set('q', query.q.trim());
  const r = query.range;
  if (r.kind === 'last') p.set('last', durationLabel(r.seconds));
  if (r.kind === 'between') {
    if (r.from.trim()) p.set('from', apiTime(r.from));
    if (r.to.trim()) p.set('to', apiTime(r.to));
  }
  if (query.limit !== emptyQuery.limit) p.set('limit', String(query.limit));
  return p;
}

// Reads what toURL wrote. Anything malformed falls back to the default for
// that part, so a hand-edited link still opens a working page.
export function fromURL(p: URLSearchParams): Query {
  const last = parseDuration(p.get('last') ?? '');
  const from = displayTime(p.get('from') ?? '');
  const to = displayTime(p.get('to') ?? '');
  const range: Range = last ? { kind: 'last', seconds: last } : from || to ? { kind: 'between', from, to } : { kind: 'all' };
  const limit = Number(p.get('limit'));
  return {
    q: p.get('q') ?? '',
    range,
    limit: Number.isInteger(limit) && limit >= 1 && limit <= MAX_LIMIT ? limit : emptyQuery.limit,
  };
}

// An example search from the overview, as a query. Every part is set, so an
// example never inherits leftovers from the previous search.
export function queryFromExample(ex: Example): Query {
  const tags = Object.entries(ex.tags ?? {}).map(([k, v]) => `${k}=${v}`);
  const q = [ex.text || '', ...tags].filter(Boolean).join(' ');
  const range: Range = ex.last_seconds
    ? { kind: 'last', seconds: ex.last_seconds }
    : ex.from || ex.to
      ? { kind: 'between', from: displayTime(ex.from || ''), to: displayTime(ex.to || '') }
      : { kind: 'all' };
  const limit = ex.limit >= 1 && ex.limit <= MAX_LIMIT ? ex.limit : emptyQuery.limit;
  return { q, range, limit };
}

// What the time control says: "All time", "Last 1h", or the custom range.
export function rangeLabel(r: Range): string {
  if (r.kind === 'all') return 'All time';
  if (r.kind === 'last') return `Last ${durationLabel(r.seconds)}`;
  const from = r.from.trim();
  const to = r.to.trim();
  if (from && to) {
    // Same day: show the date once, "2005-06-20 13:00 – 14:00".
    return from.slice(0, 10) === to.slice(0, 10) && to.length > 10 ? `${from} – ${to.slice(11)}` : `${from} – ${to}`;
  }
  return from ? `From ${from}` : `Until ${to}`;
}

// The window a range covers, in ms since the epoch: null at an open end.
export function rangeBounds(r: Range, corpus: Corpus | null, now: number = Date.now()): [number | null, number | null] {
  if (r.kind === 'last') return [(newest(corpus) ?? now) - r.seconds * 1000, null];
  if (r.kind === 'between') return [r.from.trim() ? parseTime(r.from) : null, r.to.trim() ? parseTime(r.to) : null];
  return [null, null];
}

const TIME = /^(\d{4})-(\d{2})-(\d{2})(?:[ T](\d{2}):(\d{2})(?::(\d{2}))?)?$/;

// Milliseconds since the epoch for a UTC "YYYY-MM-DD[ HH:MM[:SS]]", or null
// when it isn't one (including impossible dates such as 2005-02-30).
export function parseTime(s: string): number | null {
  const m = TIME.exec(s.trim());
  if (!m) return null;
  const [y, mo, d, h = '0', mi = '0', se = '0'] = m.slice(1).map((v) => v ?? undefined) as string[];
  const ms = Date.UTC(+y, +mo - 1, +d, +h, +mi, +se);
  const back = new Date(ms);
  if (back.getUTCMonth() !== +mo - 1 || back.getUTCDate() !== +d || +h > 23 || +mi > 59 || +se > 59) return null;
  return ms;
}

export type Field = 'q' | 'from' | 'to';
export interface Problem {
  field: Field;
  message: string;
}

// Checks a custom range before it is used, so every problem is named in plain
// words next to the field, with times in UTC.
export function validateRange(from: string, to: string, corpus: Corpus | null): Problem | null {
  const f = from.trim() ? parseTime(from) : undefined;
  const t = to.trim() ? parseTime(to) : undefined;
  const shape = 'Use YYYY-MM-DD HH:MM in UTC, for example 2005-06-20 13:00.';
  if (f === null) return { field: 'from', message: `“${from.trim()}” isn't a date and time. ${shape}` };
  if (t === null) return { field: 'to', message: `“${to.trim()}” isn't a date and time. ${shape}` };
  if (f !== undefined && t !== undefined && t <= f) {
    return { field: 'to', message: `To is not after From. Pick a To later than ${displayTime(from.trim())} UTC.` };
  }
  if (corpus) {
    const first = parseTime(corpus.min_input);
    const last = parseTime(corpus.max_input);
    const range = `The stored logs run from ${corpus.first} to ${corpus.last} UTC.`;
    if (f !== undefined && last !== null && f > last) return { field: 'from', message: `From is after the newest line. ${range}` };
    if (t !== undefined && first !== null && t < first) return { field: 'to', message: `To is before the oldest line. ${range}` };
  }
  return null;
}

// Checks the whole query before it is sent.
export function validate(query: Query, corpus: Corpus | null): Problem | null {
  const { tags } = parse(query.q);
  const seen = new Map<string, string>();
  for (const [k, v] of tags) {
    const before = seen.get(k);
    if (before !== undefined && before !== v) {
      return {
        field: 'q',
        message: `${k}=${before} and ${k}=${v} can't both be true of one line, so nothing would match. Keep one.`,
      };
    }
    seen.set(k, v);
  }
  if (query.range.kind === 'between') return validateRange(query.range.from, query.range.to, corpus);
  return null;
}
