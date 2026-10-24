import { byName, cap, featured, ms, runs, segs } from '../lib/bench';
import { n } from '../lib/format';
import d from '../components/Doc.module.css';
import s from './Section.module.css';
import m from './Measured.module.css';


// Every figure below comes from bench/results (see bench.ts); nothing is typed in.
export function Measured() {
  const env = featured.environment;
  const rareOff = byName(runs.noCompaction, 'rare word, all history');
  const rareDefault = byName(runs.defaults, 'rare word, all history');
  return (
    <section id="measured" className={s.section} aria-labelledby="measured-title">
      <div className={s.head}>
        <h2 id="measured-title" className={`display ${s.title}`}>
          Measured, not estimated.
        </h2>
        <p className={s.intro}>
          {n(featured.ingest.lines)} lines of real supercomputer logs (BGL, via LogHub) stored as {featured.queries[0]?.avg_segments_considered} segments. Each query ran{' '}
          {featured.config.iterations_per_query} times ({featured.config.iterations_for_full_scan_query} for the full scan), timed at the client.
        </p>
      </div>
      <div className={m.wrap}>
        <table className={m.table}>
          <thead>
            <tr>
              <th scope="col">Search</th>
              <th scope="col" className={m.num}>Segments read</th>
              <th scope="col" className={m.num}>P50</th>
              <th scope="col" className={m.num}>P99</th>
            </tr>
          </thead>
          <tbody>
            {featured.queries.map((q) => {
              const share = q.avg_segments_scanned / q.avg_segments_considered;
              return (
                <tr key={q.name}>
                  <th scope="row">
                    <span className={m.name}>{cap(q.name)}</span>
                    <span className={m.desc}>{q.description}</span>
                  </th>
                  <td className={`${m.num} mono`} data-label="read">
                    <span className={m.bar} aria-hidden="true">
                      <i style={{ width: `${Math.max(share * 100, 1.5)}%` }} />
                    </span>
                    {segs(q.avg_segments_scanned)} of {q.avg_segments_considered}
                  </td>
                  <td className={`${m.num} mono`} data-label="P50">{ms(q.p50_ms)}</td>
                  <td className={`${m.num} mono`} data-label="P99">{ms(q.p99_ms)}</td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
      <p className={m.env}>
        One laptop ({env.cpu}, {env.logical_cores} threads, {Math.round(env.memory_mib / 1024)} GiB), measured {env.date}. Strata and a
        local S3-compatible store ran as containers on the same machine, so there was no network round trip to a cloud bucket.
      </p>
      {rareOff && rareDefault && (
        <div className={m.finding}>
          <h3 className={`display ${m.findingTitle}`}>
            Fewer, larger segments made the rare-word search about {Math.round(rareDefault.p50_ms / rareOff.p50_ms)}× slower.
          </h3>
          <div className={m.findingBody}>
            <p className={`${m.pair} mono`}>
              <span>
                <b>{ms(rareOff.p50_ms)}</b> with {rareOff.avg_segments_considered} small segments, {Math.round(rareOff.avg_mb_read)} MiB read
              </span>
              <span>
                <b>{ms(rareDefault.p50_ms)}</b> with {rareDefault.avg_segments_considered} large segments, {Math.round(rareDefault.avg_mb_read)} MiB read
              </span>
            </p>
            <p className={m.why}>
              Merging segments was meant to speed searches up. It didn't, because a segment a search needs is downloaded whole, so bigger
              segments mean more bytes for the same answer. Reading only the parts of a segment a search needs is the next improvement.
            </p>
          </div>
        </div>
      )}
      <a className={d.more} href="/benchmarks/">
        Every configuration, ingest and the limits of these numbers
      </a>
    </section>
  );
}
