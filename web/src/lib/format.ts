const count = new Intl.NumberFormat('en-US');

export const n = (v: number) => count.format(v);

export const plural = (v: number, word: string) => `${n(v)} ${word}${v === 1 ? '' : 's'}`;

export function formatBytes(v: number): string {
  if (v < 1024) return v + ' B';
  const units = ['KiB', 'MiB', 'GiB', 'TiB'];
  let i = -1;
  do {
    v /= 1024;
    i++;
  } while (v >= 1024 && i < units.length - 1);
  return v.toFixed(1) + ' ' + units[i];
}

// Segment IDs are long; the end is the part that differs between neighbours.
export const shortId = (id: string) => (id.length > 8 ? '…' + id.slice(-8) : id);
