// Minimal search page. Log lines and everything else from the server are
// untrusted text: they are inserted with textContent, never as HTML.
const form = document.getElementById('form');
const $ = (id) => document.getElementById(id);

// ---- filters: folded away on small screens (the CSS decides when) ------------
const filtersBox = $('filters');
const filtersToggle = $('filters-toggle');

function setFiltersOpen(open) {
  filtersBox.classList.toggle('open', open);
  filtersToggle.setAttribute('aria-expanded', String(open));
}
function updateFiltersLabel() {
  const f = form.elements;
  const n = [f.tag.value, f.from.value, f.to.value].filter(v => v.trim() !== '').length;
  filtersToggle.textContent = n ? `Filters (${n})` : 'Filters';
  return n;
}
filtersToggle.addEventListener('click', () => setFiltersOpen(!filtersBox.classList.contains('open')));
form.addEventListener('input', updateFiltersLabel);
// A filter that fails the browser's own check (a date outside the data) must
// not be hidden: it could not be focused, and the form would silently refuse to submit.
form.addEventListener('invalid', () => setFiltersOpen(true), true);

let inFlight = null; // AbortController of the search currently running
let activeExample = null; // button of the example whose search is shown

form.addEventListener('submit', (ev) => {
  ev.preventDefault();
  setActiveExample(null); // a hand-made search is no longer "the example"
  runSearch(paramsFromForm(), {reveal: true});
});

// The Tags box takes several key=value pairs separated by spaces or commas.
function paramsFromForm() {
  const data = new FormData(form);
  const params = new URLSearchParams();
  for (const [k, v] of data.entries()) {
    const value = String(v).trim();
    if (value === '') continue;
    if (k === 'tag') {
      for (const t of value.split(/[\s,]+/)) if (t) params.append('tag', t);
    } else {
      params.set(k, value);
    }
  }
  return params;
}

async function runSearch(params, opts = {}) {
  // A newer search replaces an older one still in flight, so a slow answer can
  // never overwrite a fresh one.
  if (inFlight) inFlight.abort();
  const mine = new AbortController();
  inFlight = mine;

  $('error').hidden = true;
  $('status').className = '';
  $('status').textContent = 'Searching…';
  $('submit').disabled = true;
  $('metrics').classList.add('stale');
  $('results').classList.add('stale');
  try {
    const res = await fetch('/api/search?' + params.toString(), {signal: mine.signal});
    const body = await res.json();
    if (!res.ok) throw new Error(body.error || 'search failed');
    render(body);
    if (opts.reveal) revealResult();
  } catch (err) {
    if (err.name === 'AbortError') return;
    $('status').textContent = '';
    $('results').hidden = true;
    $('metrics').hidden = true;
    $('error').textContent = err.message;
    $('error').hidden = false;
  } finally {
    if (inFlight === mine) {
      inFlight = null;
      $('submit').disabled = false;
      $('metrics').classList.remove('stale');
      $('results').classList.remove('stale');
    }
  }
}

// After a search the user asked for, bring its result into view if the card is
// cut off by the bottom of the window (common on a phone, where the form is
// tall). A card that is already fully visible is left alone, and the search
// that runs by itself on load never moves the page.
function revealResult() {
  const m = $('metrics');
  if (m.hidden || m.getBoundingClientRect().bottom <= window.innerHeight) return;
  const calm = window.matchMedia('(prefers-reduced-motion: reduce)').matches;
  m.scrollIntoView({behavior: calm ? 'auto' : 'smooth', block: 'start'});
}

function render(r) {
  const m = r.metrics || {};
  const total = m.segments_considered || 0;
  const skipped = (m.skipped_by_time || 0) + (m.skipped_by_bloom || 0);
  const scanned = m.segments_scanned || 0;
  const plural = (n, w) => `${n.toLocaleString('en-US')} ${w}${n === 1 ? '' : 's'}`;

  // The headline says the result in a sentence: what was skipped, what was read.
  const head = $('headline');
  head.replaceChildren();
  const skippedText = document.createElement('span');
  const readText = document.createElement('span');
  readText.className = 'read';
  if (total === 1) {
    skippedText.textContent = scanned ? '' : 'Skipped the only segment. ';
    readText.textContent = scanned ? `Read the only segment (${formatBytes(m.bytes_read || 0)}).` : 'Read nothing.';
  } else if (scanned === 0) {
    skippedText.textContent = `Skipped all ${plural(total, 'segment')}. `;
    readText.textContent = 'Read nothing.';
  } else if (scanned === total) {
    readText.textContent = `Read all ${plural(total, 'segment')} (${formatBytes(m.bytes_read || 0)}).`;
  } else {
    skippedText.textContent = `Skipped ${skipped.toLocaleString('en-US')} of ${plural(total, 'segment')}. `;
    readText.textContent = `Read ${scanned.toLocaleString('en-US')} (${formatBytes(m.bytes_read || 0)}).`;
  }
  head.append(skippedText, readText);

  $('metrics').hidden = total === 0; // visible before drawing: the strip measures its own width
  drawStrip(r, m, total);

  $('m-time').textContent = m.skipped_by_time || 0;
  $('m-bloom').textContent = m.skipped_by_bloom || 0;
  $('m-scanned').textContent = scanned;
  $('lg-time').classList.toggle('zero', !m.skipped_by_time);
  $('lg-bloom').classList.toggle('zero', !m.skipped_by_bloom);
  $('lg-scanned').classList.toggle('zero', !scanned);
  $('summary').textContent =
    `server ${(m.server_micros / 1000).toFixed(1)} ms · round trip ${r.round_trip_ms.toFixed(1)} ms` +
    (m.retries ? ` · restarted ${m.retries}×` : '');
  drawReadList(r.segments || []);

  const body = $('results').querySelector('tbody');
  body.replaceChildren();
  for (const h of r.hits) {
    const tr = document.createElement('tr');
    tr.setAttribute('role', 'row');
    for (const [cls, text] of [['ts', h.time], ['msg', h.message], ['tags', Object.entries(h.tags || {}).map(([k, v]) => k + '=' + v).join(' ')]]) {
      const td = document.createElement('td');
      td.className = cls;
      td.setAttribute('role', 'cell');
      td.textContent = text;
      tr.appendChild(td);
    }
    body.appendChild(tr);
  }
  $('results').hidden = r.hits.length === 0;

  const status = $('status');
  if (r.hits.length === 0) {
    // An empty result is a useful answer here, so say what it means and what to try.
    status.className = 'empty-help';
    status.textContent = total > 0
      ? `No line matched. The search still ran: ${m.segments_scanned || 0} of ${total} segments were read. A line must contain every word, so try fewer words or a wider time range.`
      : 'No line matched.';
  } else {
    status.className = '';
    status.textContent = `${r.hits.length} line${r.hits.length === 1 ? '' : 's'}` + (r.truncated ? ' (more match; showing the earliest)' : '');
  }
}

const shortId = (id) => id.length > 8 ? '…' + id.slice(-8) : id;
const KIND_TEXT = {time: 'skipped by time range', bloom: 'skipped by bloom filter', scanned: 'read'};

// Draw every segment as one bar, oldest on the left. If the server judged there
// were too many to list, draw the three totals as proportional blocks instead.
function drawStrip(r, m, total) {
  const strip = $('strip');
  strip.replaceChildren();
  strip.className = '';
  const segs = r.segments || [];
  const summary = `${total.toLocaleString('en-US')} segments: ` +
    `${m.skipped_by_time || 0} skipped by time range, ${m.skipped_by_bloom || 0} skipped by bloom filter, ${m.segments_scanned || 0} read.`;
  strip.setAttribute('aria-label', summary);

  if (segs.length === 0) { // too many to draw one by one (or none known)
    strip.classList.add('blocks');
    for (const [cls, n] of [['seg-time', m.skipped_by_time], ['seg-bloom', m.skipped_by_bloom], ['seg-scanned', m.segments_scanned]]) {
      if (!n) continue;
      const b = document.createElement('i');
      b.className = cls;
      b.style.flexBasis = (n / total * 100) + '%';
      if (cls === 'seg-scanned') b.style.minWidth = '3px'; // a read is always visible
      strip.appendChild(b);
    }
    $('strip-box').style.setProperty('--n', 9999);
    $('axis-first').textContent = '';
    $('axis-last').textContent = '';
    $('strip-note').textContent = r.segments_truncated ? 'Too many segments to draw one by one, so they are shown as proportions.' : '';
    return;
  }

  $('strip-box').style.setProperty('--n', segs.length);
  // Bars are at least 1px wide with a 1px gap; when that doesn't fit the strip
  // (hundreds of segments, or a phone), drop the gaps so nothing overflows.
  strip.classList.toggle('dense', segs.length * 2 > strip.clientWidth);
  strip.classList.toggle('flat', !segs.some(s => s.kind === 'scanned'));
  let order = 0;
  for (const s of segs) {
    const bar = document.createElement('i');
    bar.className = 'seg-' + s.kind;
    const read = s.kind === 'scanned';
    bar.title = `${shortId(s.id)} · ${s.first} to ${s.last} UTC · ${KIND_TEXT[s.kind]}` +
      (read ? ` · ${formatBytes(s.bytes_read)} · ${s.hits} line${s.hits === 1 ? '' : 's'} shown` : '');
    if (read) bar.style.transitionDelay = Math.min(order++ * 12, 400) + 'ms'; // a short, bounded wave
    strip.appendChild(bar);
  }
  // The rise only plays when something was read; it is set up for the next frame.
  strip.classList.add('rise');
  requestAnimationFrame(() => requestAnimationFrame(() => strip.classList.add('go')));

  $('axis-first').textContent = segs[0].first.slice(0, 10);
  $('axis-last').textContent = segs[segs.length - 1].last.slice(0, 10);
  $('strip-note').textContent = 'Each bar is one segment, oldest to newest. Tall bars were read.';
}

// Which segments were read, and how many of the lines shown each one supplied:
// the answer to "I got 20 lines, why did it read 171 segments?".
function drawReadList(segs) {
  const read = segs.filter(s => s.kind === 'scanned');
  const box = $('read-list');
  box.hidden = read.length === 0;
  if (read.length === 0) return;
  box.open = false;
  $('read-summary').textContent = `Which segments were read (${read.length.toLocaleString('en-US')})`;
  const body = $('read-table').querySelector('tbody');
  body.replaceChildren();
  const SHOW = 12;
  for (const s of read.slice(0, SHOW)) {
    const tr = document.createElement('tr');
    tr.setAttribute('role', 'row');
    const cells = [['id', 'segment', shortId(s.id)], ['covers', 'covers', `${s.first} to ${s.last}`], ['num', 'size', formatBytes(s.bytes_read)], ['num', 'lines shown', String(s.hits)]];
    for (const [cls, label, text] of cells) {
      const td = document.createElement('td');
      td.className = cls;
      td.setAttribute('role', 'cell');
      td.dataset.label = label; // shown as a prefix on phones, where the column headers are hidden
      td.textContent = text;
      tr.appendChild(td);
    }
    tr.firstChild.title = s.id;
    body.appendChild(tr);
  }
  if (read.length > SHOW) {
    const tr = document.createElement('tr');
    tr.className = 'more';
    const td = document.createElement('td');
    td.colSpan = 4;
    td.textContent = `and ${(read.length - SHOW).toLocaleString('en-US')} more`;
    tr.appendChild(td);
    body.appendChild(tr);
  }
}

function formatBytes(n) {
  if (n < 1024) return n + ' B';
  const units = ['KiB', 'MiB', 'GiB', 'TiB'];
  let i = -1;
  do { n /= 1024; i++; } while (n >= 1024 && i < units.length - 1);
  return n.toFixed(1) + ' ' + units[i];
}

// ---- first view: what is stored, what to try ------------------------------

function setActiveExample(btn) {
  if (activeExample) activeExample.removeAttribute('aria-current');
  activeExample = btn;
  if (btn) btn.setAttribute('aria-current', 'true');
}

function fillForm(ex) {
  // Every field is set, so an example never inherits leftovers from the last one.
  form.elements.text.value = ex.text || '';
  form.elements.tag.value = Object.entries(ex.tags || {}).map(([k, v]) => k + '=' + v).join(' ');
  form.elements.from.value = ex.from || '';
  form.elements.to.value = ex.to || '';
  form.elements.limit.value = ex.limit || 50;
  // Show the filters an example uses, so what ran is visible; fold them otherwise.
  setFiltersOpen(updateFiltersLabel() > 0);
}

function runExample(ex, btn) {
  fillForm(ex);
  setActiveExample(btn);
  runSearch(paramsFromForm(), {reveal: true});
}

function renderCorpus(c) {
  const p = $('corpus');
  p.replaceChildren();
  if (!c) {
    const span = document.createElement('span');
    span.className = 'corpus-empty';
    span.textContent = 'Nothing is stored yet. Send some logs with “stratactl ingest”, then search them here.';
    p.appendChild(span);
    p.hidden = false;
    return;
  }
  const n = new Intl.NumberFormat('en-US');
  if (c.name) {
    const b = document.createElement('b');
    b.textContent = c.name;
    p.append(b, ': ');
  }
  p.append(`${n.format(c.entries)} lines in ${n.format(c.segments)} segment${c.segments === 1 ? '' : 's'} (${formatBytes(c.stored_bytes)}), ${c.first} to ${c.last} UTC`);
  p.hidden = false;
  // The date pickers only offer dates that have data.
  for (const name of ['from', 'to']) {
    form.elements[name].min = c.min_input;
    form.elements[name].max = c.max_input;
  }
}

function renderExamples(list) {
  const ul = $('example-list');
  ul.replaceChildren();
  for (const ex of list) {
    const li = document.createElement('li');
    const btn = document.createElement('button');
    btn.type = 'button';
    btn.className = 'example';
    const title = document.createElement('span');
    title.className = 'ex-title';
    title.textContent = ex.title;
    btn.appendChild(title);
    if (ex.note) {
      const note = document.createElement('span');
      note.className = 'ex-note';
      note.textContent = ex.note;
      btn.appendChild(note);
    }
    btn.addEventListener('click', () => runExample(ex, btn));
    li.appendChild(btn);
    ul.appendChild(li);
  }
  $('examples').hidden = list.length === 0;
}

async function loadOverview() {
  let o;
  try {
    const res = await fetch('/api/overview');
    if (!res.ok) return; // the page works without its introduction
    o = await res.json();
  } catch (_) {
    return;
  }
  renderCorpus(o.corpus);
  renderExamples(o.examples || []);

  if (o.credit || o.about_url) {
    $('credit').textContent = o.credit || '';
    if (o.about_url) {
      $('about').href = o.about_url;
      $('about').hidden = false;
    }
    $('foot').hidden = false;
  }

  // Never open on an empty page: run the first example straight away, so the
  // first thing a visitor sees is a real search and what it skipped.
  const first = $('example-list').querySelector('button');
  if (o.corpus && first) {
    fillForm(o.examples[0]);
    setActiveExample(first);
    runSearch(paramsFromForm()); // no reveal: do not move the page on load
  }
}

// A cursor in the search box is a convenience with a keyboard and a hazard on a
// phone, where focusing it opens the on-screen keyboard over the page. So only
// pointer devices get it.
if (window.matchMedia('(pointer: fine)').matches) form.elements.text.focus({preventScroll: true});

updateFiltersLabel();
loadOverview();
