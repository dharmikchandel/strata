import s from './Architecture.module.css';

interface Box {
  x: number;
  y: number;
  w: number;
  h: number;
  title: string;
  sub: string;
  kind?: 'store'; // where data lives, drawn on the raised tone
}

// The layout, in viewBox units (1000 × 470). Writes run along the top, reads
// along the middle, and object storage is the floor everything rests on.
const boxes: Record<string, Box> = {
  clients: { x: 20, y: 30, w: 160, h: 64, title: 'Clients', sub: 'gRPC stream' },
  buffer: { x: 250, y: 30, w: 190, h: 64, title: 'Ingest buffer', sub: 'in memory' },
  segment: { x: 510, y: 30, w: 190, h: 64, title: 'Segment', sub: 'immutable file' },
  manifest: { x: 510, y: 170, w: 190, h: 64, title: 'Manifest', sub: 'SQLite', kind: 'store' },
  searches: { x: 20, y: 270, w: 160, h: 64, title: 'Searches', sub: 'gRPC or web page' },
  engine: { x: 250, y: 270, w: 190, h: 64, title: 'Query engine', sub: 'three filters' },
  compactor: { x: 760, y: 270, w: 200, h: 64, title: 'Compactor', sub: 'merge, verify, swap' },
  storage: { x: 20, y: 396, w: 960, h: 56, title: 'Object storage', sub: 'S3-compatible: the segment files', kind: 'store' },
};

interface Edge {
  d: string; // the path; it ends at the arrowhead
  label: string;
  lx: number;
  ly: number;
  anchor?: 'start' | 'middle' | 'end';
  read?: boolean; // a download from storage: the one place data is read
}

const edges: Edge[] = [
  { d: 'M180 62 H244', label: 'batches', lx: 212, ly: 52 },
  { d: 'M440 62 H504', label: 'seal', lx: 472, ly: 52 },
  { d: 'M605 94 V164', label: 'record', lx: 615, ly: 134, anchor: 'start' },
  { d: 'M700 62 H970 V390', label: 'upload', lx: 960, ly: 230, anchor: 'end' },
  { d: 'M180 302 H244', label: 'search', lx: 212, ly: 292 },
  { d: 'M440 290 L504 228', label: '1 time range · 2 bloom filter', lx: 455, ly: 236, anchor: 'end' },
  { d: 'M345 334 V390', label: '3 download only the survivors', lx: 357, ly: 368, anchor: 'start', read: true },
  { d: 'M760 290 L706 228', label: 'one-transaction swap', lx: 742, ly: 236, anchor: 'start' },
  { d: 'M860 334 V390', label: 'read, write, check', lx: 848, ly: 368, anchor: 'end' },
];

// How the parts fit together: an authored diagram for wide screens, and the
// same two flows as ordered lists, which phones show instead and screen
// readers always get.
export function Architecture() {
  return (
    <figure className={s.figure}>
      <svg className={s.svg} viewBox="0 0 1000 470" role="img" aria-labelledby="arch-flows">
        <defs>
          <marker id="arch-arrow" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse">
            <path d="M0 0 L10 5 L0 10 z" className={s.head} />
          </marker>
          <marker id="arch-arrow-read" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse">
            <path d="M0 0 L10 5 L0 10 z" className={s.headRead} />
          </marker>
        </defs>
        {edges.map((e) => (
          <g key={e.label}>
            <path d={e.d} className={e.read ? s.edgeRead : s.edge} markerEnd={`url(#${e.read ? 'arch-arrow-read' : 'arch-arrow'})`} />
            <text x={e.lx} y={e.ly} textAnchor={e.anchor ?? 'middle'} className={s.edgeLabel}>
              {e.label}
            </text>
          </g>
        ))}
        {Object.entries(boxes).map(([id, b]) => (
          <g key={id}>
            <rect x={b.x} y={b.y} width={b.w} height={b.h} rx="8" className={b.kind === 'store' ? s.store : s.box} />
            <text x={b.x + 14} y={b.y + (b.h > 60 ? 28 : 25)} className={s.boxTitle}>
              {b.title}
            </text>
            <text x={b.x + 14} y={b.y + (b.h > 60 ? 48 : 43)} className={s.boxSub}>
              {b.sub}
            </text>
          </g>
        ))}
      </svg>
      <div id="arch-flows" className={s.flows}>
        <p className={s.flowTitle}>Writing</p>
        <ol>
          <li>Clients stream batches of lines over gRPC to the ingest buffer, in memory.</li>
          <li>At 4 MiB or 5 seconds the buffer seals them into an immutable segment and uploads it to object storage.</li>
          <li>The segment is recorded in the manifest (SQLite) with its time range and bloom filter; only then is it searchable.</li>
          <li>The compactor merges small segments: it reads and writes object storage and swaps them in the manifest in one transaction.</li>
        </ol>
        <p className={s.flowTitle}>Searching</p>
        <ol>
          <li>A search (gRPC, or the web page) reaches the query engine.</li>
          <li>The manifest rules segments out by time range, then by bloom filter, with no download.</li>
          <li>Only the segments left are downloaded from object storage and searched with their own index.</li>
        </ol>
      </div>
      <figcaption className={s.caption}>
        Writes run along the top, searches along the middle; object storage holds the segment files and the manifest says which of them exist. The orange arrow is
        the only place a search reads data.
      </figcaption>
    </figure>
  );
}
