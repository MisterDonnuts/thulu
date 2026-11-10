// Must run before deferred module scripts: bootstrap fetches fire before daemonFetch wraps fetch
// and would 401 without the cookie.
// Mirrors daemonFetch in lib/daemon-fetch.ts. A file, not inline, because the CSP refuses inline
// script.
(function () {
  var orig = window.fetch.bind(window);
  function readCookie(name) {
    var prefix = name + "=";
    var parts = document.cookie.split(";");
    for (var i = 0; i < parts.length; i++) {
      var c = parts[i].trim();
      if (c.indexOf(prefix) === 0) return decodeURIComponent(c.slice(prefix.length));
    }
    return null;
  }
  // Same rule as apiBase()/isDaemonURL() in lib/auth.ts. Behind a proxy the daemon's URLs have no
  // :8585, so don't match on that substring.
  var daemonOrigin = (function () {
    var l = window.location;
    if (l.port === "" || l.port === "8585") return l.origin;
    return l.protocol + "//" + l.hostname + ":8585";
  })();
  function isDaemonURL(u) {
    try {
      return new URL(u, window.location.href).origin === daemonOrigin;
    } catch (e) {
      return false;
    }
  }
  window.fetch = function (input, init) {
    try {
      var url =
        typeof input === "string" ? input : input && input.url ? input.url : String(input);
      if (isDaemonURL(url)) {
        init = Object.assign({}, init, { credentials: "include" });
        var method = (
          (init && init.method) ||
          (input && input.method) ||
          "GET"
        ).toUpperCase();
        if (method !== "GET" && method !== "HEAD" && method !== "OPTIONS") {
          var token = readCookie("octopus_csrf");
          if (token) {
            var headers = new Headers((init && init.headers) || {});
            if (!headers.has("X-CSRF-Token")) headers.set("X-CSRF-Token", token);
            init.headers = headers;
          }
        }
      }
    } catch (e) {
      /* fall through to an uncredentialed fetch */
    }
    return orig(input, init);
  };
})();
