import s from './Section.module.css';
import f from './Failures.module.css';

// From README.md, "What happens when things fail": each one is tested.
const CASES = [
  {
    when: 'The process is killed in the middle of a compaction',
    then: 'A single SQLite transaction swaps the merged segment in. Before it commits, the old segments are the data; after, the merged one is. Searches never see a mix. Tested by killing a child process at each step.',
  },
  {
    when: 'A segment is replaced while a search is reading',
    then: 'The search notices the missing file and restarts from a fresh view of the manifest. Tested with four searchers running through ten merges: every line seen exactly once.',
  },
  {
    when: 'Writing to storage fails',
    then: 'The batch goes back into the buffer and is retried. If storage stays down, clients get BUFFER_FULL instead of the server running out of memory.',
  },
  {
    when: 'A segment is corrupt or missing',
    then: 'Every file carries checksums. A search that needs it fails and names the segment, rather than returning an answer that looks complete but isn’t.',
  },
  {
    when: 'The manifest is lost but the bucket survives',
    then: 'The manifest and the bucket share a random identity. Strata refuses to start rather than let garbage collection treat every real segment as an orphan.',
  },
  {
    when: 'The server is stopped',
    then: 'It stops accepting connections, lets streams in flight finish, seals everything still in memory, then exits.',
  },
];

export function Failures() {
  return (
    <section id="failures" className={`${s.section} ${f.split}`} aria-labelledby="failures-title">
      <div className={f.aside}>
        <h2 id="failures-title" className={`display ${f.title}`}>
          Built for the bad day.
        </h2>
        <p className={s.intro}>What Strata does when things go wrong, and how each case is tested.</p>
      </div>
      <dl className={f.list}>
        {CASES.map((c) => (
          <div key={c.when} className={f.row}>
            <dt>{c.when}</dt>
            <dd>{c.then}</dd>
          </div>
        ))}
      </dl>
    </section>
  );
}
