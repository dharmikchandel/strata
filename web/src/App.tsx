import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { fetchOverview, search, SearchError, type Overview, type SearchResult } from './lib/api';
import { explain, type Explained } from './lib/errors';
import { afterNanos, liveOn, openLive, ratePerSecond, type Live } from './lib/live';
import { lineKeys } from './components/LogViewer';
import { emptyQuery, parse, parseTime, queryFromExample, rangeBounds, toApiParams, validate, type Query, type Range } from './lib/query';
import { terms as termsOf } from './lib/tokenize';
import { TopBar } from './components/TopBar';
import { ContextRow } from './components/ContextRow';
import { QueryBar } from './components/QueryBar';
import { Examples } from './components/Examples';
import { Readout } from './components/Readout';
import { Results } from './components/Results';
import { FirstRun, Unreachable } from './components/EmptyStates';
import { Footer } from './components/Footer';
import { AlertIcon } from './components/icons';
import s from './App.module.css';

export function App() {
  const [overview, setOverview] = useState<Overview | null>(null);
  const [loaded, setLoaded] = useState(false);
  const [query, setQuery] = useState<Query>(emptyQuery); // what the controls show
  const [shown, setShown] = useState<Query | null>(null); // what the result on screen searched for
  const [active, setActive] = useState<number | null>(null); // example whose search is shown
  const [result, setResult] = useState<SearchResult | null>(null);
  const [seq, setSeq] = useState(0); // counts results, so each new one starts with its rows closed
  const [segment, setSegment] = useState<string | null>(null); // a read segment whose lines alone are shown
  const [error, setError] = useState<Explained | null>(null);
  const [invalid, setInvalid] = useState(false);
  const [busy, setBusy] = useState(false);
  const [live, setLive] = useState<Live | null>(null);
  const [rate, setRate] = useState<number | null>(null); // lines stored per second, while live

  const inFlight = useRef<AbortController | null>(null);
  const inputRef = useRef<HTMLInputElement>(null);
  const readoutRef = useRef<HTMLDivElement>(null);
  const revealNext = useRef(false);
  const lastRun = useRef<Query | null>(null);
  const overviewRef = useRef<Overview | null>(null);
  overviewRef.current = overview;
  const queryRef = useRef(query);
  queryRef.current = query;
  const closeLive = useRef<(() => void) | null>(null);
  const liveRef = useRef(false);
  liveRef.current = liveOn(live);
  const samples = useRef<[number, number][]>([]); // (time ms, lines stored), the last 30 s

  // A newer search replaces an older one still in flight, so a slow answer can
  // never overwrite a fresh one.
  const runSearch = useCallback(async (q: Query, reveal: boolean): Promise<SearchResult | null> => {
    inFlight.current?.abort();
    const mine = new AbortController();
    inFlight.current = mine;
    lastRun.current = q;
    setError(null);
    setInvalid(false);
    setBusy(true);
    try {
      const r = await search(toApiParams(q, overviewRef.current?.corpus ?? null), mine.signal);
      revealNext.current = reveal;
      setResult(r);
      setSegment(null);
      setShown(q);
      setSeq((n) => n + 1);
      return r;
    } catch (err) {
      if ((err as Error).name === 'AbortError') return null;
      // The error sits by the query box; the last good result stays, dimmed,
      // so an error never leaves a hole where the page was.
      setError(explain(err instanceof SearchError ? err : new SearchError(0, '')));
      return null;
    } finally {
      if (inFlight.current === mine) {
        inFlight.current = null;
        setBusy(false);
      }
    }
  }, []);

  const stopLive = useCallback(() => {
    closeLive.current?.();
    closeLive.current = null;
    samples.current = [];
    setRate(null);
    setLive(null);
  }, []);

  // Live: first an ordinary search, newest first over all time, so the page
  // shows the latest lines at once; then a stream (lib/live.ts) that starts
  // right after the newest of them and puts each new line on top. The readout
  // follows the stream's own searches, so the strip grows as segments seal.
  const startLive = useCallback(
    async (base: Query) => {
      closeLive.current?.();
      closeLive.current = null;
      const q: Query = { ...base, range: { kind: 'all' }, order: 'newest' };
      setQuery(q);
      setActive(null);
      const problem = validate(q, overviewRef.current?.corpus ?? null);
      if (problem) {
        setError({ message: problem.message, retry: false });
        setInvalid(problem.field === 'q');
        setLive(null);
        return;
      }
      const r = await runSearch(q, false);
      if (!r) {
        setLive(null);
        return;
      }
      const params = new URLSearchParams();
      const { words, tags } = parse(q.q);
      if (words) params.set('text', words);
      for (const [k, v] of tags) params.append('tag', `${k}=${v}`);
      const after = r.hits[0] ? afterNanos(r.hits[0].time) : null;
      if (after) params.set('after', after);
      params.set('limit', String(q.limit)); // no event needs to carry more lines than are shown
      samples.current = [];
      setRate(null);
      setLive({ since: new Date().toISOString().slice(11, 19), lines: r.hits, fresh: new Set(), added: 0, gap: false, state: 'connecting', note: null });
      closeLive.current = openLive(params, {
        onTick: (tick) => {
          setResult(tick.result);
          if (tick.corpus) {
            const corpus = tick.corpus;
            setOverview((prev) => (prev ? { ...prev, corpus } : prev));
            const now = Date.now();
            samples.current = [...samples.current.filter(([at]) => now - at <= 30_000), [now, corpus.entries]];
            setRate(ratePerSecond(samples.current));
          }
          setLive((prev) => {
            if (!prev) return prev;
            const lines = [...tick.lines, ...prev.lines].slice(0, queryRef.current.limit);
            const fresh = new Set(lineKeys(lines).slice(0, tick.lines.length));
            return { ...prev, lines, fresh, added: prev.added + tick.lines.length, gap: prev.gap || tick.gap, note: null };
          });
        },
        onState: (state) => setLive((prev) => prev && { ...prev, state }),
        onProblem: (message, retry) =>
          setLive((prev) => prev && { ...prev, state: retry ? prev.state : 'failed', note: retry ? `${message}; trying again.` : message }),
        onEnd: (after) =>
          setLive((prev) => prev && { ...prev, state: 'ended', note: `Live stopped after ${Math.round(after / 60)} minutes, so an open tab doesn't search forever. Turn it on again to carry on.` }),
      });
    },
    [runSearch],
  );
  useEffect(() => () => closeLive.current?.(), []);

  // Checks, then runs. Used by every control, so each one validates the same
  // way. While live, a new search over all time restarts the live view with
  // it; any other range ends the live view.
  const submit = useCallback(
    (q: Query, reveal = true) => {
      if (liveRef.current && q.range.kind === 'all') {
        startLive(q);
        return;
      }
      stopLive();
      setQuery(q);
      const problem = validate(q, overviewRef.current?.corpus ?? null);
      if (problem) {
        setError({ message: problem.message, retry: false });
        setInvalid(problem.field === 'q');
        return;
      }
      setActive(null); // a hand-made search is no longer "the example"
      runSearch(q, reveal);
    },
    [runSearch, startLive, stopLive],
  );

  const runExample = useCallback(
    (i: number, ov: Overview | null, reveal: boolean) => {
      const ex = ov?.examples?.[i];
      if (!ex) return;
      stopLive();
      const q = queryFromExample(ex);
      setQuery(q);
      setActive(i);
      runSearch(q, reveal);
    },
    [runSearch, stopLive],
  );

  // First view: what is stored, what to try, and a real search already run,
  // so a visitor never lands on an empty page. Never moves the page on load.
  useEffect(() => {
    fetchOverview().then((ov) => {
      setOverview(ov);
      setLoaded(true);
      if (ov?.corpus && ov.examples?.length) runExample(0, ov, false);
    });
  }, [runExample]);

  // While data keeps arriving the counts at the top keep up. When the first
  // lines arrive on an empty store, the page runs its first search by itself.
  useEffect(() => {
    const id = setInterval(async () => {
      if (document.visibilityState !== 'visible') return;
      const ov = await fetchOverview();
      if (!ov) return; // a missed refresh keeps what is on screen
      const wasEmpty = !overviewRef.current?.corpus;
      setOverview((prev) => (prev ? { ...prev, corpus: ov.corpus, tags: ov.tags } : ov));
      if (wasEmpty && ov.corpus && !lastRun.current) runExample(0, ov, false);
    }, 10_000);
    return () => clearInterval(id);
  }, [runExample]);

  // A cursor in the search box is a convenience with a keyboard and a hazard on
  // a phone, where focusing it opens the on-screen keyboard over the page.
  useEffect(() => {
    if (window.matchMedia('(pointer: fine)').matches) inputRef.current?.focus({ preventScroll: true });
  }, []);

  // "/" jumps to the search box from anywhere, unless the visitor is typing.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== '/' || e.ctrlKey || e.metaKey || e.altKey) return;
      const t = e.target as HTMLElement;
      if (t.closest('input, textarea, select, [contenteditable="true"]')) return;
      e.preventDefault();
      inputRef.current?.focus();
      inputRef.current?.select();
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, []);

  // After a search the visitor asked for, bring the result into view if it is
  // cut off by the bottom of the window (common on a phone).
  useEffect(() => {
    if (!revealNext.current) return;
    revealNext.current = false;
    const el = readoutRef.current;
    if (!el || el.getBoundingClientRect().bottom <= window.innerHeight) return;
    const calm = window.matchMedia('(prefers-reduced-motion: reduce)').matches;
    el.scrollIntoView({ behavior: calm ? 'auto' : 'smooth', block: 'start' });
  }, [result]);

  // A tag clicked in a line goes into the box, or comes out if it was there,
  // and the search runs again.
  const onTag = useCallback(
    (k: string, v: string) => {
      const q = queryRef.current;
      const term = `${k}=${v}`;
      const pieces = q.q.split(/\s+/).filter(Boolean);
      const next = pieces.includes(term) ? pieces.filter((p) => p !== term) : [...pieces, term];
      submit({ ...q, q: next.join(' ') }, false);
    },
    [submit],
  );

  // What the result on screen searched for: marked in its lines and tags.
  const marks = useMemo(() => {
    const p = parse(shown?.q ?? '');
    return { terms: new Set(termsOf(p.words)), tags: new Set(p.tags.map(([k, v]) => `${k}=${v}`)) };
  }, [shown]);

  const corpus = overview?.corpus ?? null;
  // Where the result's matches can be: its range, cut to the stored data.
  const extent = useMemo((): [number | null, number | null] => {
    const [from, to] = shown ? rangeBounds(shown.range, corpus) : [null, null];
    const first = corpus ? parseTime(corpus.min_input) : null;
    const last = corpus ? parseTime(corpus.max_input) : null;
    const clip = (a: number | null, b: number | null, pick: (x: number, y: number) => number) => (a === null ? b : b === null ? a : pick(a, b));
    return [clip(from, first, Math.max), clip(to, last, Math.min)];
  }, [shown, corpus]);
  const examples = overview?.examples ?? [];
  const firstSearch = busy && !result;

  return (
    <>
      <TopBar page="search" sourceUrl={overview?.about_url} wide />
      <main className={s.main}>
        <ContextRow
          name={corpus?.name || 'Stored logs'}
          corpus={corpus}
          live={liveOn(live)}
          rate={rate}
          onLive={() => {
            if (!liveOn(live)) return startLive(query);
            // Off: back to an ordinary search for the same words.
            stopLive();
            liveRef.current = false;
            submit(query, false);
          }}
        />

        <QueryBar
          ref={inputRef}
          query={query}
          corpus={corpus}
          tags={overview?.tags ?? []}
          busy={busy}
          invalid={invalid}
          onText={(q) => {
            setQuery((prev) => ({ ...prev, q }));
            if (invalid) setInvalid(false);
          }}
          onRange={(range: Range) => submit({ ...query, range })}
          onSubmit={() => submit(query)}
        />

        {error && (
          <div id="search-error" className={s.error} role="alert">
            <AlertIcon />
            <p>{error.message}</p>
            {error.retry && lastRun.current && (
              <button type="button" className={s.retry} onClick={() => lastRun.current && runSearch(lastRun.current, false)}>
                Try again
              </button>
            )}
          </div>
        )}

        {corpus && <Examples examples={examples} active={active} onRun={(i) => runExample(i, overview, true)} />}

        {loaded && !overview && <Unreachable />}
        {loaded && overview && !corpus && <FirstRun />}

        <div ref={readoutRef} className={s.anchor}>
          {result && (
            <Readout
              result={result}
              stale={busy}
              live={!!live}
              stored={corpus?.stored_bytes ?? null}
              picked={segment}
              onPick={live ? undefined : setSegment}
            />
          )}
        </div>

        {(result || firstSearch) && (
          <Results
            key={seq}
            result={result}
            stale={busy && !!result}
            limit={query.limit}
            order={shown?.order ?? query.order}
            terms={marks.terms}
            tags={marks.tags}
            extent={extent}
            segment={segment}
            onSegment={setSegment}
            onLimit={(limit) => submit({ ...query, limit }, false)}
            onOrder={(order) => submit({ ...query, order }, false)}
            live={live}
            onTag={onTag}
          />
        )}
      </main>
      <Footer credit={overview?.credit} aboutUrl={overview?.about_url} wide />
    </>
  );
}
