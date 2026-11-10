'use strict';

const [url, secs] = process.argv.slice(2);
const end = Date.now() + Number(secs || 0) * 1000;

(async () => {
  while (Date.now() < end) {
    try {
      if ((await fetch(url)).ok) process.exit(0);
    } catch {
      // Not listening yet.
    }
    await new Promise((r) => setTimeout(r, 500));
  }
  process.exit(1);
})();
