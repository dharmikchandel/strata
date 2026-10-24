import { describe, expect, it } from 'vitest';
import { configs, FULL_SCAN } from './bench';
import { repoBase } from './repo';

// The benchmarks page states findings in words. These check each statement
// against the result files it is built from, so re-running the benchmarks
// can't leave a sentence on the page that the numbers no longer support.
describe('what the benchmarks page says', () => {
  const [big, mid, off] = configs.map((c) => c.run);
  const q = (run: typeof big, name: string) => run.queries.find((x) => x.name === name)!;

  it('configurations go from fewest, largest segments to most, smallest', () => {
    const counts = configs.map((c) => c.run.settle.segments_after_compaction_settled);
    expect(counts[0]).toBeLessThan(counts[1]);
    expect(counts[1]).toBeLessThan(counts[2]);
  });

  it('"merging segments made selective searches slower": for every search that finds lines', () => {
    for (const { name, avg_hits } of mid.queries) {
      if (name === FULL_SCAN || avg_hits === 0) continue;
      expect(q(big, name).p50_ms, name).toBeGreaterThan(q(mid, name).p50_ms);
      expect(q(mid, name).p50_ms, name).toBeGreaterThan(q(off, name).p50_ms);
      expect(q(big, name).avg_mb_read, name).toBeGreaterThan(q(off, name).avg_mb_read);
    }
  });

  it('"a word that isn\'t in the data reads nothing, or one segment": the 16 MiB run read one', () => {
    const absent = 'word not in the data';
    expect(q(big, absent).avg_segments_scanned).toBe(0);
    expect(q(off, absent).avg_segments_scanned).toBe(0);
    expect(q(mid, absent).avg_segments_scanned).toBe(1);
  });

  it('"stored size didn\'t change" and "ingest was slower with it on"', () => {
    const ratios = configs.map((c) => c.run.settle.stored_bytes_per_dataset_byte);
    expect(Math.max(...ratios) - Math.min(...ratios)).toBeLessThan(0.03);
    expect(big.ingest.lines_per_sec).toBeLessThan(off.ingest.lines_per_sec);
    expect(mid.ingest.lines_per_sec).toBeLessThan(off.ingest.lines_per_sec);
  });

  it('"every segment holds the word" in the full scan, and nothing was lost', () => {
    for (const run of [big, mid, off]) {
      const f = q(run, FULL_SCAN);
      expect(f.avg_segments_scanned / f.avg_segments_considered).toBeGreaterThan(0.85);
      expect(run.verify.found_exactly).toBe(run.verify.sampled_lines);
    }
  });
});

describe('repoBase', () => {
  it('takes a GitHub repository from the about URL, and nothing else', () => {
    expect(repoBase('https://github.com/dharmikchandel/strata#readme')).toBe('https://github.com/dharmikchandel/strata');
    expect(repoBase('https://github.com/a/b.git')).toBe('https://github.com/a/b');
    expect(repoBase('https://example.com/a/b')).toBeNull();
    expect(repoBase(undefined)).toBeNull();
  });
});
