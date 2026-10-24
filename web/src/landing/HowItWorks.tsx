import s from './Section.module.css';
import h from './HowItWorks.module.css';

type Kind = 'open' | 'time' | 'bloom' | 'read';

// Twenty segments, each stage ruling more of them out. The same pattern at
// every step, so the eye follows what each filter removed. An illustration,
// not a measurement.
const TIME_OUT = new Set([0, 1, 2, 3, 4, 5, 14, 15, 16, 17, 18, 19]);
const READ = new Set([9]);
const at = (stage: 1 | 2 | 3) =>
  Array.from({ length: 20 }, (_, i): Kind => {
    if (TIME_OUT.has(i)) return 'time';
    if (stage === 1) return 'open';
    if (!READ.has(i)) return 'bloom';
    return stage === 2 ? 'open' : 'read';
  });

const STEPS = [
  {
    title: 'Time range',
    cost: 'One SQLite query. Nothing read from storage.',
    text: 'The manifest records every segment’s first and last timestamp. Segments entirely outside the range you asked for are ruled out first, because it costs almost nothing.',
    bars: at(1),
  },
  {
    title: 'Bloom filter',
    cost: 'Also from the manifest. Still nothing read.',
    text: 'Each segment carries a bloom filter over every word in it. If a word you searched for is definitely absent, the segment is skipped. It can wrongly say “maybe”, costing one wasted read, but never wrongly says “no”.',
    bars: at(2),
  },
  {
    title: 'Read what survives',
    cost: 'The only step that touches storage.',
    text: 'The few segments left are fetched in parallel and searched with their own inverted index. Matches from different segments are merged into timestamp order.',
    bars: at(3),
  },
];

export function HowItWorks() {
  return (
    <section id="how" className={s.section} aria-labelledby="how-title">
      <div className={s.head}>
        <h2 id="how-title" className={`display ${s.title}`}>
          Three filters, cheapest first.
        </h2>
        <p className={s.intro}>
          Logs are sealed into immutable segments, each with its own index and bloom filter. A small SQLite manifest knows what every
          segment holds, so most of the work happens before any data is fetched.
        </p>
      </div>
      <ol className={h.steps}>
        {STEPS.map((st, i) => (
          <li key={st.title} className={h.step}>
            <span className={`${h.num} mono`}>{i + 1}</span>
            <div className={h.lead}>
              <h3 className={h.stepTitle}>{st.title}</h3>
              <p className={h.cost}>{st.cost}</p>
            </div>
            <p className={h.text}>{st.text}</p>
            <div className={h.mini} role="img" aria-label={`Illustration: after the ${st.title.toLowerCase()} step`}>
              {st.bars.map((k, j) => (
                <i key={j} className={h[k]} />
              ))}
            </div>
          </li>
        ))}
      </ol>
      <p className={h.note}>
        Illustration. Hollow bars are still in play; grey were ruled out by time range, blue by bloom filter, and orange were read. The
        demo draws the real thing for every search.
      </p>
    </section>
  );
}
