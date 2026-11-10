// lib/theme.ts repeats these rules (theme.test.ts keeps them in step). The ClientRouter copies the
// new page's <html> attributes, so apply before and after the swap.
// A file, not inline, because the CSP refuses inline script.
(function () {
  var KEY = "octopus.theme";
  // --color-bg of each theme in tokens.css.
  var BAR = { dark: "#1a1a1a", light: "#f7f7f5" };
  var mq = window.matchMedia ? window.matchMedia("(prefers-color-scheme: light)") : null;

  function pref() {
    try {
      var v = window.localStorage.getItem(KEY);
      return v === "light" || v === "dark" ? v : "system";
    } catch (e) {
      return "system";
    }
  }

  function meta(doc, name) {
    var head = doc.head || doc.getElementsByTagName("head")[0];
    if (!head) return null;
    var el = head.querySelector('meta[name="' + name + '"]');
    if (!el) {
      el = doc.createElement("meta");
      el.setAttribute("name", name);
      head.appendChild(el);
    }
    return el;
  }

  function apply(doc) {
    var p = pref();
    var resolved = p === "system" ? (mq && mq.matches ? "light" : "dark") : p;
    var root = doc.documentElement;
    if (p === "system") root.removeAttribute("data-theme");
    else root.setAttribute("data-theme", p);
    var scheme = meta(doc, "color-scheme");
    if (scheme) scheme.setAttribute("content", p === "system" ? "light dark" : p);
    var bar = meta(doc, "theme-color");
    if (bar) bar.setAttribute("content", BAR[resolved]);
  }

  apply(document);

  // The router may rerun this file; the document listeners must be added only once.
  if (window.octopusTheme) return;
  window.octopusTheme = {
    apply: function () {
      apply(document);
    },
  };
  document.addEventListener("astro:before-swap", function (e) {
    if (e.newDocument) apply(e.newDocument);
  });
  document.addEventListener("astro:after-swap", function () {
    apply(document);
  });
  if (mq) {
    var onChange = function () {
      apply(document);
    };
    if (mq.addEventListener) mq.addEventListener("change", onChange);
    else if (mq.addListener) mq.addListener(onChange);
  }
  window.addEventListener("storage", function (e) {
    if (e.key === KEY || e.key === null) apply(document);
  });
})();
