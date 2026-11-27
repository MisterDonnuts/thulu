// Statistics for benchmark comparison: median, a distribution-free 95% CI for the median, and the
// two-sided Mann-Whitney U test benchstat also uses (exact without ties, normal approximation
// with tie correction otherwise).

export function median(xs) {
  const s = [...xs].sort((a, b) => a - b);
  const n = s.length;
  if (!n) return NaN;
  return n % 2 ? s[(n - 1) / 2] : (s[n / 2 - 1] + s[n / 2]) / 2;
}

function binomCdf(k, n) {
  // P(X <= k) for X ~ Bin(n, 0.5)
  let sum = 0;
  let c = 1;
  for (let i = 0; i <= k; i++) {
    if (i > 0) c = (c * (n - i + 1)) / i;
    sum += c;
  }
  return sum / 2 ** n;
}

// medianCI returns [lo, hi] from order statistics, the narrowest pair with confidence >= conf; for
// tiny samples it falls back to [min, max].
export function medianCI(xs, conf = 0.95) {
  const s = [...xs].sort((a, b) => a - b);
  const n = s.length;
  if (!n) return [NaN, NaN];
  const alpha = (1 - conf) / 2;
  let k = 0;
  while (k < n && binomCdf(k, n) <= alpha) k++;
  const lo = Math.max(0, k - 1);
  return [s[lo], s[n - 1 - lo]];
}

function normalCdf(z) {
  // Abramowitz-Stegun 7.1.26 on erf
  const x = Math.abs(z) / Math.SQRT2;
  const t = 1 / (1 + 0.3275911 * x);
  const y = 1 - ((((1.061405429 * t - 1.453152027) * t + 1.421413741) * t - 0.284496736) * t + 0.254829592) * t * Math.exp(-x * x);
  return z >= 0 ? (1 + y) / 2 : (1 - y) / 2;
}

// exactUDist counts, for sample sizes m and n, how many orderings give each U.
function exactUDist(m, n) {
  // f[i][j][u] built iteratively over i, j; keep a 2D table of arrays.
  let prev = [];
  for (let j = 0; j <= n; j++) prev.push([1]);
  for (let i = 1; i <= m; i++) {
    const cur = [[1]];
    for (let j = 1; j <= n; j++) {
      const size = i * j + 1;
      const arr = new Array(size).fill(0);
      const a = prev[j]; // f(i-1, j, u - j)
      for (let u = 0; u < a.length; u++) arr[u + j] += a[u];
      const b = cur[j - 1]; // f(i, j-1, u)
      for (let u = 0; u < b.length; u++) arr[u] += b[u];
      cur.push(arr);
    }
    prev = cur;
  }
  return prev[n];
}

export function mannWhitney(a, b) {
  const m = a.length;
  const n = b.length;
  if (!m || !n) return { u: NaN, p: 1 };
  const all = [...a.map((v) => ({ v, g: 0 })), ...b.map((v) => ({ v, g: 1 }))].sort((x, y) => x.v - y.v);
  const ranks = new Array(all.length);
  let ties = false;
  let tieTerm = 0;
  for (let i = 0; i < all.length; ) {
    let j = i;
    while (j + 1 < all.length && all[j + 1].v === all[i].v) j++;
    const r = (i + j) / 2 + 1;
    for (let k = i; k <= j; k++) ranks[k] = r;
    const tcount = j - i + 1;
    if (tcount > 1) {
      ties = true;
      tieTerm += tcount ** 3 - tcount;
    }
    i = j + 1;
  }
  let r1 = 0;
  all.forEach((x, i) => {
    if (x.g === 0) r1 += ranks[i];
  });
  const u1 = r1 - (m * (m + 1)) / 2;
  const u = Math.min(u1, m * n - u1);
  if (!ties && m <= 30 && n <= 30) {
    const dist = exactUDist(m, n);
    const total = dist.reduce((s, x) => s + x, 0);
    let le = 0;
    for (let k = 0; k <= Math.floor(u); k++) le += dist[k] || 0;
    return { u: u1, p: Math.min(1, (2 * le) / total) };
  }
  const N = m + n;
  const mu = (m * n) / 2;
  const sigma = Math.sqrt(((m * n) / 12) * (N + 1 - tieTerm / (N * (N - 1))));
  if (sigma === 0) return { u: u1, p: 1 };
  const z = (Math.abs(u1 - mu) - 0.5) / sigma;
  return { u: u1, p: Math.min(1, 2 * (1 - normalCdf(z))) };
}
