import s from './Section.module.css';
import l from './Limits.module.css';

// From README.md, "Limitations", stated as plainly there.
const LIMITS = [
  ['Acknowledged is not yet durable', 'There is no write-ahead log. Lines are written to storage within about 5 seconds; a process that is killed loses the ones not yet sealed.'],
  ['A resent batch can be stored twice', 'If a connection breaks before a batch is acknowledged, the client resends it.'],
  ['No TLS or authentication', 'Strata binds to loopback by default. Don’t expose it to an untrusted network.'],
  ['One node', 'No clustering or replication. The manifest must be backed up; Strata can detect a lost one but can’t rebuild it.'],
  ['Whole-segment reads', 'A search that needs a segment downloads all of it. That is the main cost in the benchmarks above.'],
  ['Simple search', 'Every word must appear, with optional exact tags and a time range. No phrases, OR, NOT or regular expressions.'],
];

export function Limits() {
  return (
    <section id="limits" className={s.section} aria-labelledby="limits-title">
      <div className={s.head}>
        <h2 id="limits-title" className={`display ${s.title}`}>
          What it doesn’t do yet.
        </h2>
        <p className={s.intro}>Stated plainly, because they matter.</p>
      </div>
      <ul className={l.grid}>
        {LIMITS.map(([title, text]) => (
          <li key={title}>
            <h3 className={l.title}>{title}</h3>
            <p className={l.text}>{text}</p>
          </li>
        ))}
      </ul>
    </section>
  );
}
