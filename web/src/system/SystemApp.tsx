import { useEffect, useRef, useState } from 'react';
import { fetchOverview, fetchSystem, type Overview, type System, type SystemEvent, type SystemSegment } from '../lib/api';
import { formatBytes, n } from '../lib/format';
import { describe, withLeaving } from '../lib/system';
import { TopBar } from '../components/TopBar';
import { Footer } from '../components/Footer';
import { Bucket } from './Bucket';
import s from './SystemApp.module.css';

const EVERY = 2000; // ms between looks at the server
const KEEP = 200; // events kept on the page
const FADE = 900; // ms a replaced segment stays, fading (Bucket.module.css)

// The machinery at work: the segments in the bucket, sealed and merged as it
// happens, what the server did, and its vital signs. Every number comes from
// the server (/api/system), looked at every 2 s while the tab is visible.
export function SystemApp() {
  const [overview, setOverview] = useState<Overview | null>(null);
  const [sys, setSys] = useState<System | null>(null);
  const [loaded, setLoaded] = useState(false);
  const [events, setEvents] = useState<SystemEvent[]>([]); // newest first
  const [fresh, setFresh] = useState<Set<string>>(new Set());
  const [leaving, setLeaving] = useState<SystemSegment[]>([]);
  const [hot, setHot] = useState<Set<string>>(new Set());
  const last = useRef(0); // the newest event seen
  const prev = useRef<Map<string, SystemSegment> | null>(null);

  useEffect(() => {
    fetchOverview().then(setOverview);
    let stop = false;
    const look = async () => {
      if (document.visibilityState !== 'visible') return;
      const next = await fetchSystem(last.current);
      if (stop) return;
      setLoaded(true);
      setSys(next);
      if (!next) return;
      // What changed since the last look: new segments enter, and the ones a
      // merge replaced stay a moment to fade out. The first look only draws.
      const now = new Map(next.segments.map((g) => [g.id, g]));
      if (prev.current) {
        const before = prev.current;
        setFresh(new Set(next.segments.filter((g) => !before.has(g.id)).map((g) => g.id)));
        const gone = [...before.values()].filter((g) => !now.has(g.id));
        if (gone.length) {
          setLeaving((l) => [...l, ...gone]);
          setTimeout(() => setLeaving((l) => l.filter((g) => !gone.includes(g))), FADE);
        }
      }
      prev.current = now;
      if (next.events.length) {
        last.current = next.events[next.events.length - 1].seq;
        setEvents((old) => [...next.events].reverse().concat(old).slice(0, KEEP));
      }
    };
    look();
    const id = setInterval(look, EVERY);
    return () => {
      stop = true;
      clearInterval(id);
    };
  }, []);

  const segs = sys?.segments ?? [];
  const stored = segs.reduce((a, g) => a + g.size, 0);
  const shown = withLeaving(segs, leaving);
  const leavingIds = new Set(leaving.map((g) => g.id));
  const buf = sys?.buffer;
  const fill = buf && buf.seal_at > 0 ? Math.min(buf.bytes / buf.seal_at, 1) : 0;
  const byGen = [0, 0, 0];
  for (const g of segs) byGen[Math.min(g.generation, 2)]++;

  return (
    <>
      <TopBar page="system" sourceUrl={overview?.about_url} wide />
      <main className={s.main}>
        <section className={s.context} aria-labelledby="system-title">
          <h1 id="system-title" className={s.name}>System</h1>
          {sys && (
            <p className={`${s.facts} mono`}>
              <span><b>{n(sys.segments_total)}</b> segment{sys.segments_total === 1 ? '' : 's'}</span>
              <span><b>{n(Math.round(sys.lines_per_sec))}</b> lines/s arriving</span>
              <span>compaction <b>{sys.compaction ? 'on' : 'off'}</b></span>
              <span>server up since <b>{sys.started}</b> UTC</span>
            </p>
          )}
        </section>

        {loaded && !sys && (
          <p className={s.down} role="alert">
            Can't reach the Strata server, so there is nothing to show. Check that it is running; this page keeps trying every 2 s.
          </p>
        )}

        {sys && (
          <>
            <section className={s.panel} aria-labelledby="bucket-title">
              <div className={s.head}>
                <h2 id="bucket-title" className={s.headline}>
                  {segs.length === 0 ? 'The bucket is empty.' : `${n(sys.segments_total)} segment${sys.segments_total === 1 ? '' : 's'} in the bucket, ${formatBytes(stored)}.`}
                </h2>
                <p className={s.lede}>
                  One block per segment, oldest first, as wide as its file is large. A new block appears when the ingest buffer seals one; a merge replaces
                  its inputs with one block. Searches skip or read these whole.
                  {sys.segments_total > segs.length && ` Only the newest ${n(segs.length)} are drawn.`}
                </p>
              </div>
              {segs.length > 0 && <Bucket segments={shown} fresh={fresh} leaving={leavingIds} hot={hot} />}
              <div className={s.keyRow}>
                <ul className={s.legend}>
                  {['Written by ingest', 'Merged once', 'Merged more than once'].map((label, gen) => (
                    <li key={gen} data-zero={byGen[gen] === 0 || undefined}>
                      <span className={s.swatch} data-gen={gen} aria-hidden="true" />
                      {label} <b className="mono">{n(byGen[gen])}</b>
                    </li>
                  ))}
                </ul>
              </div>
              {buf && buf.seal_at > 0 && (
                <div className={s.buffer}>
                  <p>
                    <b>Ingest buffer.</b> <span className="mono">{formatBytes(buf.bytes)}</span> of lines wait in memory. A segment is sealed at{' '}
                    <span className="mono">{formatBytes(buf.seal_at)}</span> or when its oldest line has waited{' '}
                    <span className="mono">{(buf.seal_after_ms / 1000).toLocaleString('en-US')} s</span>, whichever comes first. Lines are only searchable
                    once sealed, and lost if the process is killed before then.
                  </p>
                  <div
                    className={s.meter}
                    role="meter"
                    aria-label="Ingest buffer, towards the sealing size"
                    aria-valuemin={0}
                    aria-valuemax={buf.seal_at}
                    aria-valuenow={Math.min(buf.bytes, buf.seal_at)}
                  >
                    <i style={{ transform: `scaleX(${fill})` }} />
                  </div>
                  {buf.bytes > buf.seal_at && (
                    <p className={s.warn}>
                      More is waiting than one segment holds, so sealing is failing (is storage down?). New lines are refused at {formatBytes(buf.limit)}.
                    </p>
                  )}
                </div>
              )}
            </section>

            <section className={s.panel} aria-labelledby="log-title">
              <div className={s.head}>
                <h2 id="log-title" className={s.title}>What the server did</h2>
                <p className={s.lede}>
                  Since it started at <span className="mono">{sys.started}</span> UTC, newest first. The server keeps the last 500 events in memory only, so
                  a restart starts this list again.
                </p>
              </div>
              {events.length === 0 ? (
                <p className={s.empty}>
                  Nothing yet: no line has arrived since the server started, so nothing was sealed or merged. With the generator running (<code className="mono">make up</code>), a
                  segment is sealed every few seconds.
                </p>
              ) : (
                <ol className={s.log} onPointerLeave={() => setHot(new Set())}>
                  {events.map((e) => (
                    <li key={e.seq} onPointerEnter={() => setHot(new Set([e.segment ?? '', ...(e.inputs ?? [])]))}>
                      <time className="mono">{e.at.slice(11)}</time>
                      <span className={s.kind} data-kind={e.kind}>
                        {e.kind}
                      </span>
                      <span>{describe(e)}</span>
                    </li>
                  ))}
                </ol>
              )}
            </section>
          </>
        )}
      </main>
      <Footer credit={overview?.credit} aboutUrl={overview?.about_url} wide />
    </>
  );
}
