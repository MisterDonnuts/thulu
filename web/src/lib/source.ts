// The dashboard's "Source code" link (AGPL §13). A release links to its tag; a dev build or an
// unknown version links to the repository.
export function sourceLink(base: string, version: string): string {
  const repo = base.replace(/\/+$/, "");
  if (/^\d+\.\d+\.\d+$/.test(version) && /^https:\/\/github\.com\//.test(repo)) {
    return `${repo}/tree/v${version}`;
  }
  return repo;
}
