export function webRepoURL(clone: string): string {
  if (!clone) return "";
  if (clone.startsWith("https://")) return clone.replace(/\.git$/, "");
  const m = clone.match(/^git@([^:]+):(.+?)(\.git)?$/);
  return m ? `https://${m[1]}/${m[2]}` : "";
}
