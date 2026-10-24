import { useEffect, useState } from 'react';
import { fetchOverview, type Overview } from '../lib/api';
import { fileUrl, repoBase } from '../lib/repo';
import { TopBar } from '../components/TopBar';
import { Footer } from '../components/Footer';
import { Architecture } from './Architecture';
import { decisions, failures, limits, steps, type TestRef } from './content';
import d from '../components/Doc.module.css';
import sec from '../landing/Section.module.css';
import s from './DesignApp.module.css';

// The tests behind a claim: links into the repository when the page server
// knows where it is (-about-url on GitHub), plain names otherwise.
function Tests({ tests, repo }: { tests: TestRef[]; repo: string | null }) {
  return (
    <p className={s.tests}>
      <span className={s.testsLabel}>Tested by</span>
      {tests.map((tr) =>
        repo ? (
          <a key={tr.name} className={`${s.test} mono`} href={fileUrl(repo, tr.file)} rel="noopener" title={tr.file}>
            {tr.name}
          </a>
        ) : (
          <code key={tr.name} className={`${s.test} mono`} title={tr.file}>
            {tr.name}
          </code>
        ),
      )}
    </p>
  );
}

// How Strata works and what it does when things go wrong, each claim with the
// test that checks it. The words are the README's, laid out to be read.
export function DesignApp() {
  const [overview, setOverview] = useState<Overview | null>(null);
  useEffect(() => {
    fetchOverview().then(setOverview);
  }, []);
  const repo = repoBase(overview?.about_url);

  return (
    <>
      <TopBar page="design" sourceUrl={overview?.about_url} />
      <main className={d.main}>
        <header className={d.top}>
          <h1 className={d.title}>Don't read data you don't need.</h1>
          <p className={d.lede}>
            Strata keeps logs as <b>immutable, self-indexed segments</b> in S3-compatible storage and a small <b>SQLite manifest</b> of them. A search asks the
            manifest first, rules out every segment it can without a download, and reads only what is left. Below is how each part works and what it does when
            something fails, each with the test that checks it.
          </p>
        </header>

        <section className={sec.section} aria-labelledby="parts">
          <div className={sec.head}>
            <h2 id="parts" className={`display ${sec.title}`}>
              The parts.
            </h2>
            <p className={sec.intro}>One server process, one bucket, one SQLite file. No other services.</p>
          </div>
          <Architecture />
        </section>

        <section className={sec.section} aria-labelledby="life">
          <div className={sec.head}>
            <h2 id="life" className={`display ${sec.title}`}>
              The life of a line.
            </h2>
            <p className={sec.intro}>From the moment a client sends it to the moment a merge rewrites it.</p>
          </div>
          <ol className={s.steps}>
            {steps.map((st, i) => (
              <li key={st.id} className={s.step}>
                <span className={`${s.num} mono`}>{i + 1}</span>
                <div>
                  <h3 className={s.stepTitle}>{st.title}</h3>
                  {st.id === 'segment' && <SegmentLayout />}
                  <div className={d.prose}>
                    {st.body.map((p) => (
                      <p key={p.slice(0, 24)}>{p}</p>
                    ))}
                  </div>
                  <Tests tests={st.tests} repo={repo} />
                </div>
              </li>
            ))}
          </ol>
        </section>

        <section className={sec.section} aria-labelledby="failures">
          <div className={sec.head}>
            <h2 id="failures" className={`display ${sec.title}`}>
              When things fail.
            </h2>
            <p className={sec.intro}>
              The part of the design that took the most care. Many of these tests were checked by breaking the code on purpose and watching the test fail.
            </p>
          </div>
          <dl className={s.failures}>
            {failures.map((f) => (
              <div key={f.when} className={s.failure}>
                <dt>{f.when}</dt>
                <dd>
                  <p>{f.then}</p>
                  <Tests tests={f.tests} repo={repo} />
                </dd>
              </div>
            ))}
          </dl>
        </section>

        <section className={sec.section} aria-labelledby="decisions">
          <div className={sec.head}>
            <h2 id="decisions" className={`display ${sec.title}`}>
              Why it is built this way.
            </h2>
          </div>
          <ul className={d.list}>
            {decisions.map(([what, why]) => (
              <li key={what}>
                <b>{what}.</b> {why}
              </li>
            ))}
          </ul>
        </section>

        <section className={sec.section} aria-labelledby="limits">
          <div className={sec.head}>
            <h2 id="limits" className={`display ${sec.title}`}>
              What it doesn't do yet.
            </h2>
          </div>
          <ul className={d.list}>
            {limits.map(([what, why]) => (
              <li key={what}>
                <b>{what}.</b> {why}
              </li>
            ))}
          </ul>
          <a className={d.more} href="/benchmarks/">
            What the design costs and saves, measured
          </a>
        </section>
      </main>
      <Footer credit={overview?.credit} aboutUrl={overview?.about_url} />
    </>
  );
}

// A segment file, to scale in spirit: a small fixed header, the lines (most
// of the file), the index, and the bloom filter at the end.
function SegmentLayout() {
  const parts: [string, string, string][] = [
    ['header', 'Header', '84 bytes: counts, time range, offsets, two checksums'],
    ['lines', 'Lines', 'sorted by time, with an offset table'],
    ['index', 'Index', 'each word → the lines holding it'],
    ['bloom', 'Bloom filter', 'every word, about 1% false "maybe"'],
  ];
  return (
    <div className={s.layout} role="img" aria-label="A segment file: header, lines, index, bloom filter, in that order">
      {parts.map(([k, name, what]) => (
        <span key={k} className={s.part} data-part={k}>
          <b>{name}</b>
          <span>{what}</span>
        </span>
      ))}
    </div>
  );
}
