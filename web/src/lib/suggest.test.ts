import { describe, expect, it } from 'vitest';
import { accept, suggest } from './suggest';

const tags = [
  { key: 'component', values: ['APP', 'KERNEL'], capped: false },
  { key: 'level', values: ['FATAL', 'INFO', 'WARNING'], capped: false },
];
const texts = (t: string, caret = t.length) => suggest(t, caret, tags).items.map((i) => i.text);

describe('suggest', () => {
  it('offers keys, then values, for the word at the caret, ignoring case', () => {
    expect(texts('parity lev')).toEqual(['level=']);
    expect(texts('level=')).toEqual(['level=FATAL', 'level=INFO', 'level=WARNING']);
    expect(texts('level=w')).toEqual(['level=WARNING']);
    expect(texts('fatal')).toEqual(['level=FATAL']); // a bare value finds its key
    expect(texts('k')).toEqual(['component=KERNEL']);
  });

  it('stays quiet when there is nothing to add', () => {
    expect(texts('')).toEqual([]);
    expect(texts('parity ')).toEqual([]); // caret after a space: no word
    expect(texts('level=INFO')).toEqual([]); // already complete
    expect(texts('nosuch=')).toEqual([]);
    expect(texts('=x')).toEqual([]);
  });

  it('works on the word under the caret, not only the last one', () => {
    const s = suggest('lev parity', 2, tags);
    expect([s.from, s.to, s.items[0].text]).toEqual([0, 3, 'level=']);
  });
});

describe('accept', () => {
  it('replaces the word, adding a space after a whole tag', () => {
    const s = suggest('parity level=f', 14, tags);
    expect(accept('parity level=f', s, s.items[0])).toEqual({ text: 'parity level=FATAL ', caret: 19 });
    const k = suggest('lev', 3, tags);
    expect(accept('lev', k, k.items[0])).toEqual({ text: 'level=', caret: 6 });
    const mid = suggest('fat error', 3, tags);
    expect(accept('fat error', mid, mid.items[0])).toEqual({ text: 'level=FATAL error', caret: 12 });
  });
});
