import { useEffect, useState } from 'react';
import { fetchOverview, type Overview } from '../lib/api';
import { cap, configs, FULL_SCAN, featured, ms, segs, type QueryResult } from '../lib/bench';
import { formatBytes, n } from '../lib/format';
import { repoBase } from '../lib/repo';
import { TopBar } from '../components/TopBar';
import { Footer } from '../components/Footer';
import { PairedBars, type Group } from './PairedBars';
import d from '../components/Doc.module.css';
import sec from '../landing/Section.module.css';
import s from './BenchApp.module.css';

const mib = (v: number) => (v === 0 ? 'nothing' : `${v >= 100 ? Math.round(v) : v.toFixed(1)} MiB`);

// One row per configuration for a query, as PairedBars wants it.
function rowsFor(name: string) {
  return configs.flatMap(({ label, run }) => {
    const q = run.queries.find((x) => x.name === name);
    if (!q) return [];
    return [
      {
        label,
        sub: `${n(run.settle.segments_after_compaction_settled)} segments`,
        left: q.avg_mb_read,
        right: q.p50_ms,
        detail: `P99 ${ms(q.p99_ms)} · read ${segs(q.avg_segments_scanned)} of ${n(q.avg_segments_considered)} segments · ${n(Math.round(q.avg_hits))} lines returned · ${q.n} runs`,
      },
    ];
  });
}

// Data read per segment read, over every selective search of a configuration:
// how big "one segment" is when a search has to open one.
function perSegment(qs: QueryResult[]): number {
  const sel = qs.filter((q) => q.name !== FULL_SCAN);
  const read = sel.reduce((a, q) => a + q.avg_mb_read, 0);
  const opened = sel.reduce((a, q) => a + q.avg_segments_scanned, 0);
  return opened ? read / opened : 0;
}

// Every figure on this page comes from bench/results, read when the page is
// built (lib/bench.ts). Nothing is typed in.
export function BenchApp() {
  const [overview, setOverview] = useState<Overview | null>(null);
  useEffect(() => {
    fetchOverview().then(setOverview);
  }, []);
  const repo = repoBase(overview?.about_url);
  const env = featured.environment;
  const ing = featured.ingest;
  const selective = featured.queries.filter((q) => q.name !== FULL_SCAN);
  const groups: Group[] = selective.map((q) => ({ title: cap(q.name), note: q.description, rows: rowsFor(q.name) }));
  const fullScan: Group[] = [{ title: cap(FULL_SCAN), note: featured.queries.find((q) => q.name === FULL_SCAN)?.description, rows: rowsFor(FULL_SCAN) }];

  return (
    <>
      <TopBar page="benchmarks" sourceUrl={overview?.about_url} />
      <main className={d.main}>
        <header className={d.top}>
          <h1 className={d.title}>Measured, not estimated.</h1>
          <p className={d.lede}>
            <b>{n(ing.lines)} lines</b> ({formatBytes(ing.dataset_bytes)}) of real logs from the BlueGene/L supercomputer, {ing.first_log_time.slice(0, 10)} to{' '}
            {ing.last_log_time.slice(0, 10)}, stored three ways and searched. Each configuration was loaded from scratch, checked ({n(featured.verify.found_exactly)} of{' '}
            {n(featured.verify.sampled_lines)} sampled lines found every time), then each search ran {featured.config.iterations_per_query} times (
            {featured.config.iterations_for_full_scan_query} for the full scan), timed at the client.
          </p>
          <p className={`${d.facts} mono`}>
            <span>
              <b>{env.cpu}</b>, {env.logical_cores} threads, {Math.round(env.memory_mib / 1024)} GiB
            </span>
            <span>{env.kernel}</span>
            <span>{env.go}</span>
            <span>measured {env.date}</span>
          </p>
          <p className={s.caveat}>
            One laptop. Strata and a local S3-compatible store (RustFS) ran as containers on the same machine, so there was no network round trip to a cloud bucket:
            in the cloud every segment read costs more. Searches ran one at a time.
          </p>
        </header>

        <section className={sec.section} aria-labelledby="finding">
          <div className={sec.head}>
            <h2 id="finding" className={`display ${sec.title}`}>
              Merging segments made selective searches slower.
            </h2>
            <p className={sec.intro}>
              A segment a search needs is downloaded whole. Merged into big segments, a search that finds its answer in one segment still downloads all of it. The
              orange bars are the data read; the time follows them.
            </p>
          </div>
          <PairedBars
            label="Data read and median time per search, for each configuration"
            groups={groups}
            left={{ title: 'Data read per search', format: mib, kind: 'read' }}
            right={{ title: 'Median time (P50)', format: ms, kind: 'neutral' }}
          />
          <div className={`${d.prose} ${s.after}`}>
            <p>
              One segment opened cost about{' '}
              {configs.map((c, i) => (
                <span key={c.id}>
                  {i > 0 && (i === configs.length - 1 ? ' and ' : ', ')}
                  <b className="mono">{mib(perSegment(c.run.queries))}</b> {c.id === 'off' ? 'uncompacted' : `at the ${c.id === 'default' ? '64' : '16'} MiB target`}
                </span>
              ))}
              , averaged over these searches. Compaction cut the segment count from {n(configs[2].run.settle.segments_after_compaction_settled)} to{' '}
              {n(configs[0].run.settle.segments_after_compaction_settled)}, but stored size didn't change (about{' '}
              {featured.settle.stored_bytes_per_dataset_byte.toFixed(2)}× the raw data each time) and ingest was slower with it on.
            </p>
            <p>
              A word that isn't in the data reads nothing, or one segment when a bloom filter wrongly says "maybe" (they are tuned to about 1%). That happened in the
              16 MiB run, which is why its bar isn't zero. Reading only the parts of a segment a search needs (header, index, matching lines) would remove the trade-off
              between few large segments and many small ones.
            </p>
          </div>
        </section>

        <section className={sec.section} aria-labelledby="skipping">
          <div className={sec.head}>
            <h2 id="skipping" className={`display ${sec.title}`}>
              Most segments are never opened.
            </h2>
            <p className={sec.intro}>
              Segments read out of all that existed, averaged over the runs, with how many the time range and the bloom filters ruled out without a download.
            </p>
          </div>
          <div className={d.scroll}>
            <table className={`${d.table} ${d.stack}`}>
              <thead>
                <tr>
                  <th scope="col">Search</th>
                  {configs.map((c) => (
                    <th key={c.id} scope="col" className={d.num}>
                      {c.label}
                    </th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {featured.queries.map((q) => (
                  <tr key={q.name}>
                    <th scope="row">
                      {cap(q.name)}
                      <span className={d.sub}>{q.description}</span>
                    </th>
                    {configs.map((c) => {
                      const r = c.run.queries.find((x) => x.name === q.name);
                      return (
                        <td key={c.id} className={`${d.num} mono`} data-label={c.label}>
                          {r ? (
                            <>
                              read {segs(r.avg_segments_scanned)} of {n(r.avg_segments_considered)}
                              <span className={d.sub}>
                                time {segs(r.avg_skipped_by_time)} · bloom {segs(r.avg_skipped_by_bloom)}
                              </span>
                            </>
                          ) : (
                            '–'
                          )}
                        </td>
                      );
                    })}
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </section>

        <section className={sec.section} aria-labelledby="worst">
          <div className={sec.head}>
            <h2 id="worst" className={`display ${sec.title}`}>
              The worst case: every segment holds the word.
            </h2>
            <p className={sec.intro}>
              A common word over all history can't be ruled out anywhere, so every segment is read. This is what a search costs without skipping, and it is about the
              same whichever way the data is cut.
            </p>
          </div>
          <PairedBars
            label="Data read and median time for a full scan, for each configuration"
            groups={fullScan}
            left={{ title: 'Data read per search', format: mib, kind: 'read' }}
            right={{ title: 'Median time (P50)', format: ms, kind: 'neutral' }}
          />
        </section>

        <section className={sec.section} aria-labelledby="ingest">
          <div className={sec.head}>
            <h2 id="ingest" className={`display ${sec.title}`}>
              Ingest.
            </h2>
            <p className={sec.intro}>
              {featured.config.ingest_streams} parallel gRPC streams, {n(featured.config.batch_lines)} lines per batch. Throughput counts batches accepted into the
              server's memory; they are in storage within about 5 seconds. The client alone could read and parse the file at about{' '}
              {n(Math.round(ing.read_and_parse_only_lines_per_sec / 1000) * 1000)} lines/s, so reading it was not the limit.
            </p>
          </div>
          <div className={d.scroll}>
            <table className={`${d.table} ${d.stack}`}>
              <thead>
                <tr>
                  <th scope="col">Configuration</th>
                  <th scope="col" className={d.num}>Lines per second</th>
                  <th scope="col" className={d.num}>Batch reply P50 / P99</th>
                  <th scope="col" className={d.num}>Segments: after ingest → settled</th>
                  <th scope="col" className={d.num}>Stored</th>
                  <th scope="col" className={d.num}>Lines checked</th>
                </tr>
              </thead>
              <tbody>
                {configs.map(({ id, label, run }) => (
                  <tr key={id}>
                    <th scope="row">{label}</th>
                    <td className={`${d.num} mono`} data-label="Lines per second">
                      {n(Math.round(run.ingest.lines_per_sec))}
                      <span className={d.sub}>{run.ingest.dataset_mb_per_sec.toFixed(1)} MiB/s</span>
                    </td>
                    <td className={`${d.num} mono`} data-label="Batch reply P50 / P99">
                      {ms(run.ingest.batch_ack_p50_ms)} / {ms(run.ingest.batch_ack_p99_ms)}
                    </td>
                    <td className={`${d.num} mono`} data-label="Segments">
                      {n(run.settle.segments_right_after_ingest)} → {n(run.settle.segments_after_compaction_settled)}
                      {run.settle.seconds_until_settled > 0 && <span className={d.sub}>settled after {Math.round(run.settle.seconds_until_settled)} s</span>}
                    </td>
                    <td className={`${d.num} mono`} data-label="Stored">
                      {formatBytes(run.settle.stored_bytes)}
                      <span className={d.sub}>{run.settle.stored_bytes_per_dataset_byte.toFixed(2)}× the raw data</span>
                    </td>
                    <td className={`${d.num} mono`} data-label="Lines checked">
                      {n(run.verify.found_exactly)} of {n(run.verify.sampled_lines)} found
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </section>

        <section className={sec.section} aria-labelledby="limits">
          <div className={sec.head}>
            <h2 id="limits" className={`display ${sec.title}`}>
              What these numbers don't show.
            </h2>
          </div>
          <ul className={d.list}>
            <li>
              <b>One machine, a local store.</b> Every segment read went over loopback to a container. A cloud bucket adds a network round trip to each one, so
              absolute times would be higher, and whole-segment reads would cost more.
            </li>
            <li>
              <b>Two full runs per configuration.</b> The order of the results was the same both times; most medians moved 5 to 15%, one nearly doubled (merges group
              segments differently from run to run). This page shows the second run.
            </li>
            <li>
              <b>One search at a time.</b> Latency under concurrent searches, or while ingest is running, was not measured.
            </li>
            <li>
              <b>One dataset.</b> BGL lines carry many unique tokens (microsecond timestamps), which makes the index large. Other logs would behave differently.
            </li>
          </ul>
        </section>

        <section className={sec.section} aria-labelledby="reproduce">
          <div className={sec.head}>
            <h2 id="reproduce" className={`display ${sec.title}`}>
              Run it yourself.
            </h2>
            <p className={sec.intro}>About 12 minutes per configuration. The harness writes the same files this page is built from.</p>
          </div>
          <pre className={d.code}>{`curl -L -o BGL.zip https://zenodo.org/records/8196385/files/BGL.zip && unzip BGL.zip
export STRATA_PORT=27070 RUSTFS_PORT=29000 RUSTFS_CONSOLE_PORT=29001
docker compose -p strata-bench up -d --build --wait
go run ./cmd/strata-bench -addr 127.0.0.1:27070 -dataset BGL.log -out bench/results/mine.md
docker compose -p strata-bench down -v`}</pre>
          {repo && (
            <a className={d.more} href={`${repo}/tree/main/bench/results`} rel="noopener">
              The raw results, with every figure the harness recorded
            </a>
          )}
        </section>
      </main>
      <Footer credit="Data: BGL logs from the BlueGene/L supercomputer at Lawrence Livermore National Laboratory (Oliner and Stearley, DSN 2007), via LogHub." aboutUrl={overview?.about_url} />
    </>
  );
}
