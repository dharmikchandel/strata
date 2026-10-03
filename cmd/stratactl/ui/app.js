// Minimal search page. Log lines are untrusted text: everything from the
// server is inserted with textContent, never as HTML.
const form = document.getElementById('form');
const $ = (id) => document.getElementById(id);

form.addEventListener('submit', async (ev) => {
  ev.preventDefault();
  const data = new FormData(form);
  const params = new URLSearchParams();
  for (const [k, v] of data.entries()) {
    if (String(v).trim() !== '') params.set(k, String(v).trim());
  }
  $('error').hidden = true;
  $('status').textContent = 'Searching…';
  try {
    const res = await fetch('/api/search?' + params.toString());
    const body = await res.json();
    if (!res.ok) throw new Error(body.error || 'search failed');
    render(body);
  } catch (err) {
    $('status').textContent = '';
    $('results').hidden = true;
    $('metrics').hidden = true;
    $('error').textContent = err.message;
    $('error').hidden = false;
  }
});

function render(r) {
  const m = r.metrics || {};
  const total = m.segments_considered || 0;
  const bar = $('bar');
  bar.replaceChildren();
  for (const [cls, n] of [['time', m.skipped_by_time], ['bloom', m.skipped_by_bloom], ['scanned', m.segments_scanned]]) {
    if (!n) continue;
    const seg = document.createElement('div');
    seg.className = cls;
    seg.style.width = (n / total * 100) + '%';
    if (cls === 'scanned') seg.style.minWidth = '3px'; // a read is always visible
    bar.appendChild(seg);
  }
  $('m-time').textContent = m.skipped_by_time || 0;
  $('m-bloom').textContent = m.skipped_by_bloom || 0;
  $('m-scanned').textContent = m.segments_scanned || 0;
  $('summary').textContent =
    `Read ${m.segments_scanned || 0} of ${total} segments (${formatBytes(m.bytes_read || 0)}) · ` +
    `server ${(m.server_micros / 1000).toFixed(1)} ms · round trip ${r.round_trip_ms.toFixed(1)} ms` +
    (m.retries ? ` · restarted ${m.retries}×` : '');
  $('metrics').hidden = total === 0;

  const body = $('results').querySelector('tbody');
  body.replaceChildren();
  for (const h of r.hits) {
    const tr = document.createElement('tr');
    for (const [cls, text] of [['time', h.time], ['msg', h.message], ['tags', Object.entries(h.tags || {}).map(([k, v]) => k + '=' + v).join(' ')]]) {
      const td = document.createElement('td');
      td.className = cls;
      td.textContent = text;
      tr.appendChild(td);
    }
    body.appendChild(tr);
  }
  $('results').hidden = r.hits.length === 0;
  $('status').textContent = r.hits.length === 0 ? 'No matching lines.' :
    `${r.hits.length} line${r.hits.length === 1 ? '' : 's'}` + (r.truncated ? ' (more match; showing the earliest)' : '');
}

function formatBytes(n) {
  if (n < 1024) return n + ' B';
  const units = ['KiB', 'MiB', 'GiB', 'TiB'];
  let i = -1;
  do { n /= 1024; i++; } while (n >= 1024 && i < units.length - 1);
  return n.toFixed(1) + ' ' + units[i];
}
