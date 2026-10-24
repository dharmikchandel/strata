import s from './EmptyStates.module.css';

// Nothing stored yet: how to get lines in, in the commands the README uses.
export function FirstRun() {
  return (
    <section className={s.panel} aria-labelledby="first-run-title">
      <h2 id="first-run-title" className={s.title}>Nothing is stored yet</h2>
      <p className={s.lede}>Strata is running, but no log lines have arrived. Send some, and this page will search them.</p>
      <ol className={s.steps}>
        <li>
          <span>Start the generator, which sends made-up lines shaped like the BGL dataset:</span>
          <code className="mono">make up</code>
        </li>
        <li>
          <span>Or send a file of your own, one line per log entry:</span>
          <code className="mono">go run ./cmd/stratactl ingest -file app.log</code>
        </li>
      </ol>
      <p className={s.note}>Lines become searchable about 5 seconds after they arrive, when their segment is sealed. This page checks every 10 seconds and runs a first search when they appear.</p>
    </section>
  );
}

// The page's server answered, but the Strata server behind it didn't.
export function Unreachable() {
  return (
    <section className={s.panel} aria-labelledby="unreachable-title">
      <h2 id="unreachable-title" className={s.title}>Can't reach the Strata server</h2>
      <p className={s.lede}>
        This page searches through a Strata server, and it didn't answer. Check that it is running (for example with <code className="mono">make up</code>), then reload.
      </p>
      <p className={s.note}>This page tries again every 10 seconds.</p>
    </section>
  );
}
