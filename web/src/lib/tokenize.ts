// The page's copy of the server's tokenizer (internal/segment/tokenize.go):
// lowercase, maximal runs of Unicode letters and digits. Highlighting has to
// split text exactly as the index did, or it would mark words that didn't
// match, or miss ones that did.

// Go's IsLetter is category L and IsDigit is category Nd.
const RUN = /[\p{L}\p{Nd}]+/gu;

// Go lowercases one code point at a time (strings.ToLower uses the simple
// mapping). JavaScript's toLowerCase on a whole string applies context rules
// instead: a final capital sigma becomes "ς" rather than "σ". Lowercasing each
// code point on its own avoids that; "İ" is the one letter whose lowercase is
// two code points in JavaScript and one ("i") in Go.
export function lowerLikeGo(s: string): string {
  let out = '';
  for (const ch of s) out += ch === 'İ' ? 'i' : ch.toLowerCase();
  return out;
}

export function tokenize(s: string): string[] {
  return lowerLikeGo(s).match(RUN) ?? [];
}

// The distinct terms a search's words become, in order.
export function terms(words: string): string[] {
  return [...new Set(tokenize(words))];
}

export interface Piece {
  text: string;
  hit: boolean;
}

// Splits text into pieces, marking each run whose term is in the set. The
// original text is kept as it was; only the comparison is lowercased.
export function highlight(text: string, wanted: Set<string>): Piece[] {
  if (wanted.size === 0) return [{ text, hit: false }];
  const out: Piece[] = [];
  let at = 0;
  for (const m of text.matchAll(RUN)) {
    const i = m.index;
    if (!wanted.has(lowerLikeGo(m[0]))) continue;
    if (i > at) out.push({ text: text.slice(at, i), hit: false });
    out.push({ text: m[0], hit: true });
    at = i + m[0].length;
  }
  if (at < text.length) out.push({ text: text.slice(at), hit: false });
  return out;
}
