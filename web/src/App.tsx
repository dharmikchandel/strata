import { useCallback, useEffect, useRef, useState } from 'react';
import { fetchOverview, search, type Overview, type SearchResult } from './lib/api';
import { emptyForm, formFromExample, toParams, filterCount, validate, type Field, type FormState } from './lib/query';
import { TopBar } from './components/TopBar';
import { SearchForm } from './components/SearchForm';
import { Examples } from './components/Examples';
import { Readout } from './components/Readout';
import { Results } from './components/Results';
import { Footer } from './components/Footer';
import { AlertIcon } from './components/icons';
import s from './App.module.css';

export function App() {
  const [overview, setOverview] = useState<Overview | null>(null);
  const [loaded, setLoaded] = useState(false);
  const [form, setForm] = useState<FormState>(emptyForm);
  const [filtersOpen, setFiltersOpen] = useState(false);
  const [active, setActive] = useState<number | null>(null); // example whose search is shown
  const [result, setResult] = useState<SearchResult | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [invalid, setInvalid] = useState<Field | null>(null);
  const [busy, setBusy] = useState(false);

  const inFlight = useRef<AbortController | null>(null);
  const wordsRef = useRef<HTMLInputElement>(null);
  const readoutRef = useRef<HTMLDivElement>(null);
  const revealNext = useRef(false);

  // A newer search replaces an older one still in flight, so a slow answer can
  // never overwrite a fresh one.
  const runSearch = useCallback(async (f: FormState, reveal: boolean) => {
    inFlight.current?.abort();
    const mine = new AbortController();
    inFlight.current = mine;
    setError(null);
    setInvalid(null);
    setBusy(true);
    try {
      const r = await search(toParams(f), mine.signal);
      revealNext.current = reveal;
      setResult(r);
    } catch (err) {
      if ((err as Error).name === 'AbortError') return;
      setResult(null);
      setError((err as Error).message);
    } finally {
      if (inFlight.current === mine) {
        inFlight.current = null;
        setBusy(false);
      }
    }
  }, []);

  const runExample = useCallback(
    (i: number, ov: Overview | null, reveal: boolean) => {
      const ex = ov?.examples?.[i];
      if (!ex) return;
      const f = formFromExample(ex);
      setForm(f);
      setActive(i);
      // Show the filters an example uses, so what ran is visible; fold them otherwise.
      setFiltersOpen(filterCount(f) > 0);
      runSearch(f, reveal);
    },
    [runSearch],
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

  // While data keeps arriving the counts at the top keep up.
  useEffect(() => {
    const id = setInterval(async () => {
      if (document.visibilityState !== 'visible') return;
      const ov = await fetchOverview();
      if (ov) setOverview((prev) => (prev ? { ...prev, corpus: ov.corpus } : ov));
    }, 10_000);
    return () => clearInterval(id);
  }, []);

  // A cursor in the search box is a convenience with a keyboard and a hazard on
  // a phone, where focusing it opens the on-screen keyboard over the page.
  useEffect(() => {
    if (window.matchMedia('(pointer: fine)').matches) wordsRef.current?.focus({ preventScroll: true });
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

  const corpus = overview?.corpus ?? null;
  const examples = overview?.examples ?? [];
  const hits = result?.hits ?? [];
  const m = result?.metrics;

  return (
    <>
      <TopBar corpus={corpus} loaded={loaded} />
      <main className={s.main}>
        {loaded && !corpus && (
          <p className={s.empty}>
            Nothing is stored yet. Send some logs with <code className="mono">stratactl ingest</code>, then search them here.
          </p>
        )}

        <SearchForm
          ref={wordsRef}
          form={form}
          corpus={corpus}
          busy={busy}
          invalid={invalid}
          filtersOpen={filtersOpen}
          onFiltersOpen={setFiltersOpen}
          onChange={setForm}
          onSubmit={() => {
            const problem = validate(form, corpus);
            if (problem) {
              setError(problem.message);
              setInvalid(problem.field);
              setFiltersOpen(true); // the field at fault must not stay folded away
              return;
            }
            setActive(null); // a hand-made search is no longer "the example"
            runSearch(form, true);
          }}
        />

        {error && (
          <p id="search-error" className={s.error} role="alert">
            <AlertIcon />
            {error}
          </p>
        )}

        {corpus && <Examples examples={examples} active={active} onRun={(i) => runExample(i, overview, true)} />}

        <div ref={readoutRef} className={s.anchor}>
          {result && <Readout result={result} stale={busy} />}
        </div>

        <p className={s.status} aria-live="polite" data-help={(result && hits.length === 0) || undefined}>
          {busy && !result
            ? 'Searching…'
            : result && hits.length === 0
              ? m && m.segments_considered > 0
                ? `No line matched. The search still ran: ${m.segments_scanned} of ${m.segments_considered} segments were read. A line must contain every word, so try fewer words or a wider time range.`
                : 'No line matched.'
              : result
                ? `${hits.length} line${hits.length === 1 ? '' : 's'}${result.truncated ? ' (more match; showing the earliest)' : ''}`
                : ''}
        </p>

        {result && <Results hits={hits} stale={busy} />}
      </main>
      <Footer credit={overview?.credit} aboutUrl={overview?.about_url} />
    </>
  );
}
