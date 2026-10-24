import type { SearchError } from './api';

export interface Explained {
  message: string;
  retry: boolean; // whether trying the same search again could work
}

// The page server's error text is safe but written for developers ("time range
// must have 0 <= from < to"). This says the same in plain words, and what to
// do next. Anything not recognised is shown as the server wrote it.
export function explain(err: SearchError): Explained {
  const raw = err.message;
  switch (err.status) {
    case 0:
      return { message: "Couldn't reach this page's server. Check that stratactl ui is still running, then try again.", retry: true };
    case 429:
      return { message: 'The server is busy with other searches. Try again in a moment.', retry: true };
    case 502:
      return raw.includes('could not be read')
        ? { message: "Some stored data couldn't be read, so the answer would be incomplete. Nothing is shown rather than a partial result.", retry: false }
        : { message: "Couldn't reach the Strata server. Check that it is running, then try again.", retry: true };
    case 503:
      return { message: 'The Strata server is not accepting searches right now. Try again in a moment.', retry: true };
    case 504:
      return { message: 'The search took too long and was stopped. Add a word or narrow the time range, so fewer segments have to be read.', retry: false };
    case 400:
      if (/time range must have/.test(raw)) return { message: 'The time range is empty: its start is not before its end.', retry: false };
      if (/^(from|to): /.test(raw)) return { message: `${raw.startsWith('from') ? 'From' : 'To'} isn't a date and time. Use YYYY-MM-DD HH:MM in UTC.`, retry: false };
      if (/^text is \d+ bytes/.test(raw)) return { message: 'The search is too long. Use fewer words (at most 4,096 bytes).', retry: false };
      if (/^tag .* must look like key=value$/.test(raw)) return { message: 'A tag must look like key=value, for example level=FATAL.', retry: false };
      if (/^limit /.test(raw)) return { message: 'The number of lines to show must be from 1 to 1,024.', retry: false };
      return { message: raw ? `The server refused this search: ${raw}` : 'The server refused this search.', retry: false };
    default:
      return { message: 'Something went wrong on the server, so the search did not finish. Try again.', retry: true };
  }
}
