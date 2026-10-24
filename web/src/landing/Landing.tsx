import { useEffect, useState } from 'react';
import { fetchOverview, search, type Example, type Overview, type SearchResult } from '../lib/api';
import { formatBytes, n } from '../lib/format';
import { TopBar } from '../components/TopBar';
import { Strip } from '../components/Strip';
import { Footer } from '../components/Footer';
import { ArrowRightIcon, ArrowUpRightIcon } from '../components/icons';
import { Terminal, TERMINAL_LIMIT } from './Terminal';
import { HowItWorks } from './HowItWorks';
import { Measured } from './Measured';
import { Failures } from './Failures';
import { Limits } from './Limits';
import s from './Landing.module.css';

type Proof =
  | { state: 'loading'; example: Example }
  | { state: 'live'; example: Example; result: SearchResult }
  | { state: 'recorded' };

// The landing page proves the engine instead of describing it: on load it runs
// the first example search against the stored logs and shows the real answer,
// as the command-line client prints it and as the strip the app draws.
export function Landing() {
  const [overview, setOverview] = useState<Overview | null>(null);
  const [proof, setProof] = useState<Proof | null>(null);

  useEffect(() => {
    const ctl = new AbortController();
    fetchOverview().then(async (ov) => {
      setOverview(ov);
      const ex = ov?.corpus ? ov.examples?.find((e) => e.text) : undefined;
      if (!ex) {
        setProof({ state: 'recorded' }); // no server, or nothing stored yet
        return;
      }
      setProof({ state: 'loading', example: ex });
      const params = new URLSearchParams({ text: ex.text, limit: String(TERMINAL_LIMIT) });
      for (const [k, v] of Object.entries(ex.tags ?? {})) params.append('tag', `${k}=${v}`);
      if (ex.from && !ex.last_seconds) params.set('from', ex.from);
      if (ex.to && !ex.last_seconds) params.set('to', ex.to);
      try {
        setProof({ state: 'live', example: ex, result: await search(params, ctl.signal) });
      } catch (err) {
        if ((err as Error).name !== 'AbortError') setProof({ state: 'recorded' });
      }
    });
    return () => ctl.abort();
  }, []);

  const corpus = overview?.corpus;
  const live = proof?.state === 'live' ? proof : null;
  const m = live?.result.metrics;

  return (
    <>
      <TopBar page="landing" sourceUrl={overview?.about_url} />
      <main>
        <section className={s.hero} aria-labelledby="hero-title">
          <h1 id="hero-title" className={`display ${s.headline}`}>
            Search logs without reading them all.
          </h1>
          <div className={s.heroText}>
            <p className={s.lede}>
              Strata keeps logs as immutable segments in S3-compatible storage. Before it opens anything, it rules out every segment that
              can't contain your answer, and each search shows exactly what it skipped.
            </p>
            <div className={s.actions}>
              <a className={s.primary} href="/search/">
                Try the live demo <ArrowRightIcon className={s.arrow} />
              </a>
              <a className={s.secondary} href="#how">
                How it works
              </a>
            </div>
            {corpus && (
              <p className={`${s.corpus} mono`}>
                Live on {corpus.name ? `${corpus.name}: ` : ''}
                {n(corpus.entries)} lines in {n(corpus.segments)} segments, {formatBytes(corpus.stored_bytes)}
              </p>
            )}
          </div>
          <div className={s.heroProof}>
            {proof && <Terminal {...proof} />}
          </div>
        </section>

        {live && m && m.segments_considered > 0 && (
          <section className={s.stripBand} aria-label="The same search, drawn as segments">
            <div className={s.stripHead}>
              <p className={s.stripTitle}>
                The same search, one bar per segment.{' '}
                <span className={s.stripRead}>
                  {m.segments_scanned} of {m.segments_considered} read
                </span>
                , {formatBytes(m.bytes_read)}.
              </p>
              <a className={s.stripLink} href="/search/">
                Run your own <ArrowUpRightIcon />
              </a>
            </div>
            <Strip result={live.result} metrics={m} />
          </section>
        )}

        <HowItWorks />
        <Measured />
        <Failures />
        <Limits />

        <section className={s.close} aria-labelledby="close-title">
          <h2 id="close-title" className={`display ${s.closeTitle}`}>
            See what it skips.
          </h2>
          <p className={s.closeText}>Every search in the demo shows the segments it ruled out and the ones it had to read.</p>
          <a className={s.primary} href="/search/">
            Try the live demo <ArrowRightIcon className={s.arrow} />
          </a>
        </section>
      </main>
      <Footer credit={overview?.credit} aboutUrl={overview?.about_url} />
    </>
  );
}
