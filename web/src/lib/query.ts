import type { Corpus, Example } from './api';

// What the search form holds. Field names match the /api/search parameters.
export interface FormState {
  text: string;
  tag: string; // several key=value pairs, separated by spaces or commas
  from: string; // "YYYY-MM-DD HH:MM", UTC (seconds optional)
  to: string;
  limit: string;
}

export const emptyForm: FormState = { text: '', tag: '', from: '', to: '', limit: '50' };

// Times are shown and typed as "2005-06-20 13:00", the same shape as every
// other time on the page; the API wants "2005-06-20T13:00".
export const displayTime = (s: string) => s.replace('T', ' ');
const apiTime = (s: string) => s.trim().replace(' ', 'T');

export function toParams(f: FormState): URLSearchParams {
  const params = new URLSearchParams();
  const text = f.text.trim();
  if (text) params.set('text', text);
  for (const t of f.tag.split(/[\s,]+/)) if (t) params.append('tag', t);
  if (f.from.trim()) params.set('from', apiTime(f.from));
  if (f.to.trim()) params.set('to', apiTime(f.to));
  if (f.limit.trim()) params.set('limit', f.limit.trim());
  return params;
}

// How many of the optional filters (tags, from, to) are set: shown on the
// folded "Filters" toggle on small screens.
export const filterCount = (f: FormState) => [f.tag, f.from, f.to].filter((v) => v.trim() !== '').length;

// Every field is set, so an example never inherits leftovers from the last one.
// "The last five minutes" is worked out at click time from the visitor's clock,
// in UTC, to the minute.
export function formFromExample(ex: Example, now: number = Date.now()): FormState {
  const tag = Object.entries(ex.tags ?? {})
    .map(([k, v]) => `${k}=${v}`)
    .join(' ');
  const from = ex.last_seconds ? new Date(now - ex.last_seconds * 1000).toISOString().slice(0, 16) : ex.from || '';
  const to = ex.last_seconds ? '' : ex.to || '';
  return { text: ex.text || '', tag, from: displayTime(from), to: displayTime(to), limit: String(ex.limit || 50) };
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

export type Field = 'from' | 'to' | 'limit';
export interface Problem {
  field: Field;
  message: string;
}

// Checks the form before it is sent, so every problem is named in plain words
// next to the form, with times in UTC, instead of in the browser's own bubble.
export function validate(f: FormState, corpus: Corpus | null): Problem | null {
  const from = f.from.trim() ? parseTime(f.from) : undefined;
  const to = f.to.trim() ? parseTime(f.to) : undefined;
  const shape = 'Use YYYY-MM-DD HH:MM in UTC, for example 2005-06-20 13:00.';
  if (from === null) return { field: 'from', message: `“${f.from.trim()}” isn't a date and time. ${shape}` };
  if (to === null) return { field: 'to', message: `“${f.to.trim()}” isn't a date and time. ${shape}` };
  if (from !== undefined && to !== undefined && to <= from) {
    return { field: 'to', message: `To is not after From. Pick a To later than ${displayTime(f.from.trim())} UTC.` };
  }
  if (corpus) {
    const first = parseTime(corpus.min_input);
    const last = parseTime(corpus.max_input);
    const range = `The stored logs run from ${corpus.first} to ${corpus.last} UTC.`;
    if (from !== undefined && last !== null && from > last) return { field: 'from', message: `From is after the newest line. ${range}` };
    if (to !== undefined && first !== null && to < first) return { field: 'to', message: `To is before the oldest line. ${range}` };
  }
  const limit = Number(f.limit);
  if (f.limit.trim() !== '' && (!Number.isInteger(limit) || limit < 1 || limit > 1000)) {
    return { field: 'limit', message: 'Max lines must be a whole number from 1 to 1,000.' };
  }
  return null;
}
