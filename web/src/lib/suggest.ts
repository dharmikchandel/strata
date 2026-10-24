import type { TagValues } from './api';

export interface Suggestion {
  text: string; // what replaces the word being typed
  complete: boolean; // a whole key=value (a space follows it), or only key=
}

export interface Suggestions {
  from: number; // the word being typed is text.slice(from, to)
  to: number;
  items: Suggestion[];
}

// Tag suggestions for the word at the caret, from the tag values the server
// has stored. "lev" offers level=; "level=f" offers level=FATAL; a bare value
// such as "fatal" offers level=FATAL too. Matching ignores case, but what is
// inserted keeps the stored case, because tags match exactly.
export function suggest(text: string, caret: number, tags: TagValues[], max = 8): Suggestions {
  let from = caret;
  while (from > 0 && !/\s/.test(text[from - 1])) from--;
  let to = caret;
  while (to < text.length && !/\s/.test(text[to])) to++;
  const word = text.slice(from, to);
  const none = { from, to, items: [] };
  if (!word) return none;

  const lower = word.toLowerCase();
  const items: Suggestion[] = [];
  const eq = word.indexOf('=');
  if (eq > 0) {
    const key = tags.find((t) => t.key === word.slice(0, eq));
    const prefix = word.slice(eq + 1).toLowerCase();
    for (const v of key?.values ?? []) {
      if (v.toLowerCase().startsWith(prefix) && v !== word.slice(eq + 1)) items.push({ text: `${key!.key}=${v}`, complete: true });
    }
  } else if (eq < 0) {
    for (const t of tags) if (t.key.toLowerCase().startsWith(lower)) items.push({ text: `${t.key}=`, complete: false });
    for (const t of tags) {
      for (const v of t.values) if (v.toLowerCase().startsWith(lower)) items.push({ text: `${t.key}=${v}`, complete: true });
    }
  }
  return { from, to, items: items.slice(0, max) };
}

// The text after accepting a suggestion, and where the caret goes.
export function accept(text: string, s: Suggestions, pick: Suggestion): { text: string; caret: number } {
  const after = text.slice(s.to);
  const insert = pick.text + (pick.complete && !after.startsWith(' ') ? ' ' : '');
  const next = text.slice(0, s.from) + insert + after;
  return { text: next, caret: s.from + insert.length + (pick.complete && after.startsWith(' ') ? 1 : 0) };
}
