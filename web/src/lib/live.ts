import type { Corpus, Hit, SearchResult } from './api';

// One event of a live view (/api/live, cmd/stratactl/live.go): the lines that
// became searchable since the last event, newest first, the search that found
// them (for the readout), and what is stored now.
export interface LiveTick {
  lines: Hit[];
  gap: boolean; // more new lines arrived than one event carries
  result: SearchResult;
  corpus: Corpus | null;
}

export type LiveState = 'connecting' | 'open' | 'reconnecting' | 'failed';

// A live view as the page holds it: the lines on screen (newest first) and
// how the stream is doing.
export interface Live {
  since: string; // when it started, "HH:MM:SS" UTC
  lines: Hit[];
  fresh: Set<string>; // keys of the lines from the latest event
  added: number; // new lines since it started
  gap: boolean; // some new lines were passed over
  state: LiveState | 'ended';
  note: string | null; // a passing problem, or why it stopped
}
export const liveOn = (l: Live | null) => !!l && l.state !== 'failed' && l.state !== 'ended';

export interface LiveHandlers {
  onTick: (t: LiveTick) => void;
  onState: (s: LiveState) => void;
  onProblem: (message: string, retry: boolean) => void;
  onEnd: (afterSeconds: number) => void;
}

// Opens a live view and returns the function that closes it. EventSource
// reconnects by itself after a dropped connection, sending the last event id,
// so the server carries on from the newest line already shown.
export function openLive(params: URLSearchParams, h: LiveHandlers): () => void {
  const es = new EventSource('/api/live?' + params.toString());
  const data = (e: Event) => JSON.parse((e as MessageEvent).data);
  h.onState('connecting');
  es.addEventListener('open', () => h.onState('open'));
  es.addEventListener('tick', (e) => h.onTick(data(e) as LiveTick));
  es.addEventListener('problem', (e) => {
    const p = data(e) as { error: string; retry: boolean };
    if (!p.retry) es.close();
    h.onProblem(p.error, p.retry);
  });
  es.addEventListener('end', (e) => {
    es.close();
    h.onEnd((data(e) as { after_seconds: number }).after_seconds);
  });
  // A refused connection (too many viewers, server down) closes for good; a
  // dropped one is retried by the browser.
  es.onerror = () => h.onState(es.readyState === EventSource.CLOSED ? 'failed' : 'reconnecting');
  return () => es.close();
}

// The stream's starting point after a line shown on the page: the last
// nanosecond of that line's microsecond (the page has times to the
// microsecond), as a decimal string, since nanoseconds since 1970 are beyond
// the integers a JavaScript number holds exactly.
export function afterNanos(time: string): string | null {
  const m = /^(\d{4})-(\d{2})-(\d{2}) (\d{2}):(\d{2}):(\d{2})\.(\d{6})$/.exec(time);
  if (!m) return null;
  const sec = Date.UTC(+m[1], +m[2] - 1, +m[3], +m[4], +m[5], +m[6]) / 1000;
  return (BigInt(sec) * 1_000_000_000n + BigInt(m[7]) * 1000n + 999n).toString();
}

// Lines per second stored, from (time ms, stored lines) samples. Lines are
// counted when their segment is sealed, which happens in steps every few
// seconds, so the rate is only given once the samples span 10 s or more.
export function ratePerSecond(samples: [number, number][]): number | null {
  if (samples.length < 2) return null;
  const [t0, e0] = samples[0];
  const [t1, e1] = samples[samples.length - 1];
  if (t1 - t0 < 10_000 || e1 < e0) return null;
  return ((e1 - e0) * 1000) / (t1 - t0);
}
