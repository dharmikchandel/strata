import { memo, useCallback, useState } from 'react';
import type { Hit } from '../lib/api';
import { highlight } from '../lib/tokenize';
import { CheckIcon, ChevronIcon, CopyIcon, PlusIcon } from './icons';
import s from './LogViewer.module.css';

interface Props {
  hits: Hit[];
  wrap: boolean; // whole messages, or one line each
  terms: Set<string>; // the search's terms, to mark in each line
  tags: Set<string>; // "key=value" tags already in the search
  onTag: (key: string, value: string) => void; // add the tag to the search, or take it out
}

const LEVEL_KEYS = ['level', 'severity', 'lvl'];
const ERROR_LEVELS = new Set(['FATAL', 'ERROR', 'ERR', 'SEVERE', 'FAILURE', 'CRITICAL', 'CRIT', 'EMERG', 'ALERT', 'PANIC']);
const WARN_LEVELS = new Set(['WARN', 'WARNING']);

// With a mouse, a tag in a row is a button that adds it to the search. On a
// touch screen a chip that small can't be a fair target, so it is a label,
// and the opened line offers the same action as full-size buttons.
const fine = typeof window !== 'undefined' && window.matchMedia('(pointer: fine)').matches;

const levelKey = (tags: Record<string, string> | undefined) => Object.keys(tags ?? {}).find((k) => LEVEL_KEYS.includes(k.toLowerCase()));
const severity = (v: string) => (ERROR_LEVELS.has(v.toUpperCase()) ? 'error' : WARN_LEVELS.has(v.toUpperCase()) ? 'warn' : undefined);

// The matching lines, one dense row each. Every row stays in the page, so the
// browser's find (Ctrl+F) and a screen reader's table navigation see all of
// them; what keeps 1,024 rows cheap is `content-visibility: auto` in the
// stylesheet, which lets the browser skip laying out and painting rows that
// are off screen. Log text is untrusted: React renders it as text.
export function LogViewer({ hits, wrap, terms, tags, onTag }: Props) {
  const [open, setOpen] = useState<Set<number>>(() => new Set());
  const hasLevel = hits.some((h) => levelKey(h.tags));
  const toggle = useCallback(
    (i: number) =>
      setOpen((prev) => {
        const next = new Set(prev);
        if (!next.delete(i)) next.add(i);
        return next;
      }),
    [],
  );

  return (
    <div className={s.table} role="table" aria-label="Matching lines" data-levels={hasLevel || undefined} data-wrap={wrap || undefined}>
      <div role="rowgroup" className={s.head}>
        <div role="row" className={s.row}>
          <span role="columnheader">Time (UTC)</span>
          {hasLevel && <span role="columnheader">Level</span>}
          <span role="columnheader">Message</span>
          <span role="columnheader" className={s.tagsHead}>Tags</span>
        </div>
      </div>
      <div role="rowgroup">
        {hits.map((h, i) => (
          <Line key={i} i={i} hit={h} wrap={wrap} hasLevel={hasLevel} open={open.has(i)} terms={terms} tags={tags} onToggle={toggle} onTag={onTag} />
        ))}
      </div>
    </div>
  );
}

interface LineProps {
  i: number;
  hit: Hit;
  wrap: boolean;
  hasLevel: boolean;
  open: boolean;
  terms: Set<string>;
  tags: Set<string>;
  onToggle: (i: number) => void;
  onTag: (key: string, value: string) => void;
}

// memo: opening one row re-renders that row, not all thousand.
const Line = memo(function Line({ i, hit, wrap, hasLevel, open, terms, tags, onToggle, onTag }: LineProps) {
  const lk = levelKey(hit.tags);
  const level = lk ? hit.tags![lk] : '';
  const others = Object.entries(hit.tags ?? {}).filter(([k]) => k !== lk);
  const pieces = highlight(hit.message, terms);
  const id = `line-${i}`;

  const tagButton = (k: string, v: string, cls: string, children?: React.ReactNode) => {
    const on = tags.has(`${k}=${v}`);
    const sev = cls === s.level ? severity(v) : undefined;
    if (!fine) {
      return (
        <span key={k} className={cls} data-sev={sev} data-on={on || undefined}>
          {children ?? `${k}=${v}`}
        </span>
      );
    }
    return (
      <button
        key={k}
        type="button"
        className={cls}
        data-sev={sev}
        aria-pressed={on}
        title={on ? `Take ${k}=${v} out of the search` : `Add ${k}=${v} to the search`}
        onClick={() => onTag(k, v)}
      >
        {children ?? `${k}=${v}`}
      </button>
    );
  };

  return (
    <>
      <div
        role="row"
        className={s.row}
        data-open={open || undefined}
        onClick={(e) => {
          // A click on a control is that control's; a drag that selected text
          // is someone copying, not asking to open the line.
          if ((e.target as HTMLElement).closest('button')) return;
          if (window.getSelection()?.toString()) return;
          onToggle(i);
        }}
      >
        <span role="cell" className={s.ts}>
          <button type="button" className={s.toggle} aria-expanded={open} aria-controls={open ? id : undefined} aria-label={`${open ? 'Close' : 'Open'} the line at ${hit.time}`}>
            <ChevronIcon className={s.chev} />
          </button>
          <span className="mono">{hit.time}</span>
        </span>
        {hasLevel && (
          <span role="cell" className={s.lvl}>
            {level && tagButton(lk!, level, s.level, level)}
          </span>
        )}
        <span role="cell" className={`${s.msg} mono`}>
          {pieces.map((p, k) => (p.hit ? <mark key={k}>{p.text}</mark> : p.text))}
        </span>
        <span role="cell" className={s.tags}>
          {others.map(([k, v]) => tagButton(k, v, s.tag))}
        </span>
      </div>
      {open && <Detail id={id} hit={hit} pieces={wrap ? null : pieces} tags={tags} onTag={onTag} />}
    </>
  );
});

// Under an opened row: where the line came from, and the whole line when the
// row shows only its first part (pieces is null when rows are wrapped).
function Detail({ id, hit, pieces, tags, onTag }: { id: string; hit: Hit; pieces: ReturnType<typeof highlight> | null; tags: Set<string>; onTag: (k: string, v: string) => void }) {
  const [copied, setCopied] = useState(false);
  const all = Object.entries(hit.tags ?? {});
  return (
    <div role="row" className={s.detail} id={id}>
      <div role="cell" className={s.detailCell}>
        {pieces && <p className={`${s.full} mono`}>{pieces.map((p, k) => (p.hit ? <mark key={k}>{p.text}</mark> : p.text))}</p>}
        <dl className={s.facts}>
          <dt>Time (UTC)</dt>
          <dd className="mono">{hit.time}</dd>
          <dt>Segment</dt>
          <dd className="mono">{hit.segment_id}</dd>
          {all.length > 0 && (
            <>
              <dt>Tags</dt>
              <dd className={s.tagList}>
                {all.map(([k, v]) => {
                  const on = tags.has(`${k}=${v}`);
                  return (
                    <button key={k} type="button" className={s.addTag} aria-pressed={on} onClick={() => onTag(k, v)}>
                      {on ? <CheckIcon /> : <PlusIcon />}
                      <span className="mono">{k}={v}</span>
                      <span className="sr-only">{on ? ', in the search; press to take it out' : ', press to add it to the search'}</span>
                    </button>
                  );
                })}
              </dd>
            </>
          )}
        </dl>
        <button
          type="button"
          className={s.copy}
          onClick={async () => {
            try {
              await navigator.clipboard.writeText(`${hit.time} ${hit.message}`);
              setCopied(true);
              setTimeout(() => setCopied(false), 1500);
            } catch {
              // No clipboard (insecure origin or denied): the text is selectable.
            }
          }}
        >
          {copied ? <CheckIcon /> : <CopyIcon />}
          {copied ? 'Copied' : 'Copy line'}
        </button>
      </div>
    </div>
  );
}

// Placeholder rows while the first search runs, in the same grid, so the page
// doesn't jump when the real rows arrive.
export function LogSkeleton({ rows = 8 }: { rows?: number }) {
  return (
    <div className={s.table} aria-hidden="true" data-skeleton>
      {Array.from({ length: rows }, (_, i) => (
        <div key={i} className={s.row}>
          <span className={s.bone} />
          <span className={s.bone} data-w={(i * 37) % 4} />
          <span />
        </div>
      ))}
    </div>
  );
}
