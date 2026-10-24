// The project's repository, from the page server's -about-url flag, for
// links to the code and the tests. Only a GitHub URL becomes a repository
// base; anything else (or nothing) means the pages name files without links.
export function repoBase(about: string | undefined): string | null {
  if (!about) return null;
  const m = /^(https:\/\/github\.com\/[\w.-]+\/[\w.-]+?)(?:\.git)?\/?(?:#.*)?$/.exec(about);
  return m ? m[1] : null;
}

// A link to a file (and optionally a test function in it) in the repository.
export const fileUrl = (repo: string, path: string) => `${repo}/blob/main/${path}`;
