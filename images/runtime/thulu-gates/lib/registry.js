// Package registry lookups over HTTPS (egress 443): does a package exist, when was it first
// published, and under which license. fetch is injectable so tests never touch the network.
// Results: exists true/false, or null when the registry could not answer (offline, 5xx,
// private package).

const UA = "thulu-gates (+https://thulu.dev)";
const TIMEOUT_MS = 10_000;

async function getJSON(fetchFn, url) {
  try {
    const r = await fetchFn(url, { headers: { accept: "application/json", "user-agent": UA }, signal: AbortSignal.timeout(TIMEOUT_MS) });
    if (r.status === 404 || r.status === 410) return { status: r.status, missing: true };
    if (!r.ok) return { status: r.status, error: `HTTP ${r.status}` };
    const text = await r.text();
    try {
      return { status: r.status, body: JSON.parse(text), text };
    } catch {
      return { status: r.status, text };
    }
  } catch (err) {
    return { error: String(err?.cause?.code || err?.name || err) };
  }
}

// goEscape applies the module proxy's case encoding: an upper-case letter becomes "!" + lower.
export function goEscape(path) {
  return path.replace(/[A-Z]/g, (c) => "!" + c.toLowerCase());
}

export function matchGoPrivate(mod, patterns) {
  return (patterns || "")
    .split(",")
    .map((p) => p.trim())
    .filter(Boolean)
    .some((p) => {
      const re = new RegExp("^" + p.replace(/[.+^$()|[\]\\]/g, "\\$&").replace(/\*/g, "[^/]*") + "(/|$)");
      return re.test(mod);
    });
}

function spdxFromClassifiers(classifiers = []) {
  const map = {
    "MIT License": "MIT",
    "Apache Software License": "Apache-2.0",
    "BSD License": "BSD-3-Clause",
    "ISC License (ISCL)": "ISC",
    "Mozilla Public License 2.0 (MPL 2.0)": "MPL-2.0",
    "GNU General Public License v3 (GPLv3)": "GPL-3.0",
    "GNU General Public License v2 (GPLv2)": "GPL-2.0",
    "GNU Lesser General Public License v3 (LGPLv3)": "LGPL-3.0",
    "GNU Affero General Public License v3": "AGPL-3.0",
    "Python Software Foundation License": "PSF-2.0",
    "The Unlicense (Unlicense)": "Unlicense",
  };
  for (const c of classifiers) {
    const m = /^License :: (?:OSI Approved :: )?(.+)$/.exec(c);
    if (m && map[m[1]]) return map[m[1]];
  }
  return null;
}

// lookup answers {exists, created, versionExists, license, note, error} for one dependency.
export async function lookup(dep, { fetchFn = globalThis.fetch, env = process.env } = {}) {
  const { ecosystem: eco, name } = dep;
  const version = dep.exact || null;
  if (eco === "npm") {
    const r = await getJSON(fetchFn, `https://registry.npmjs.org/${name.replace("/", "%2f")}`);
    if (r.missing) return { exists: false };
    if (!r.body) return { exists: null, error: r.error || "unreadable response" };
    const v = version ? r.body.versions?.[version] : null;
    const latest = r.body["dist-tags"]?.latest;
    const lic = (v || r.body.versions?.[latest] || r.body).license;
    return {
      exists: true,
      created: r.body.time?.created || null,
      versionExists: version ? Boolean(v) : null,
      license: typeof lic === "string" ? lic : lic?.type || null,
    };
  }
  if (eco === "pypi") {
    const r = await getJSON(fetchFn, `https://pypi.org/pypi/${encodeURIComponent(name)}/json`);
    if (r.missing) return { exists: false };
    if (!r.body) return { exists: null, error: r.error || "unreadable response" };
    let created = null;
    for (const files of Object.values(r.body.releases || {})) {
      for (const f of files || []) if (f.upload_time_iso_8601 && (!created || f.upload_time_iso_8601 < created)) created = f.upload_time_iso_8601;
    }
    const info = r.body.info || {};
    const license = info.license_expression || (info.license && info.license.length < 40 ? info.license : null) || spdxFromClassifiers(info.classifiers);
    return { exists: true, created, versionExists: version ? Boolean(r.body.releases?.[version]) : null, license };
  }
  if (eco === "go") {
    if (matchGoPrivate(name, env.GOPRIVATE) || matchGoPrivate(name, env.GONOPROXY)) return { exists: null, note: "private" };
    const r = await getJSON(fetchFn, `https://proxy.golang.org/${goEscape(name)}/@v/${goEscape(version || dep.version)}.info`);
    if (r.missing) return { exists: false };
    if (!r.body) return { exists: null, error: r.error || "unreadable response" };
    return { exists: true, created: r.body.Time || null, versionExists: true, license: null };
  }
  if (eco === "cargo") {
    const r = await getJSON(fetchFn, `https://crates.io/api/v1/crates/${encodeURIComponent(name)}`);
    if (r.missing) return { exists: false };
    if (!r.body) return { exists: null, error: r.error || "unreadable response" };
    const vs = r.body.versions || [];
    const v = version ? vs.find((x) => x.num === version) : vs[0];
    return { exists: true, created: r.body.crate?.created_at || null, versionExists: version ? Boolean(v) : null, license: v?.license || null };
  }
  if (eco === "maven") {
    const [g, a] = name.split(":");
    try {
      const res = await fetchFn(`https://repo.maven.apache.org/maven2/${g.replace(/\./g, "/")}/${a}/maven-metadata.xml`, {
        headers: { "user-agent": UA },
        signal: AbortSignal.timeout(TIMEOUT_MS),
      });
      if (res.status === 404) return { exists: false };
      if (!res.ok) return { exists: null, error: `HTTP ${res.status}` };
      const xml = await res.text();
      return { exists: true, created: null, versionExists: version ? xml.includes(`<version>${version}</version>`) : null, license: null };
    } catch (err) {
      return { exists: null, error: String(err?.cause?.code || err?.name || err) };
    }
  }
  return { exists: null, note: "unsupported ecosystem" };
}

const DEPS_DEV_SYSTEM = { npm: "npm", go: "go", pypi: "pypi", cargo: "cargo", maven: "maven" };

// depsDevLicense asks deps.dev (Open Source Insights) for a version's licenses; it covers the
// registries that publish none (Go, Maven).
export async function depsDevLicense(dep, version, { fetchFn = globalThis.fetch } = {}) {
  const sys = DEPS_DEV_SYSTEM[dep.ecosystem];
  if (!sys || !version) return null;
  const r = await getJSON(fetchFn, `https://api.deps.dev/v3/systems/${sys}/packages/${encodeURIComponent(dep.name)}/versions/${encodeURIComponent(version)}`);
  if (!r.body) return null;
  const lic = r.body.licenses || [];
  return lic.length ? lic.join(" AND ") : null;
}
