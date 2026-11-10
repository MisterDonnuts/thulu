// Must run before first paint or the page jumps when the sidebar restores its state. A file, not
// inline, because the CSP refuses inline script.
(function () {
  var root = document.documentElement;
  // Keep in sync with the settings links in Sidebar.astro.
  var path = window.location.pathname.replace(/\/$/, "");
  var settingsPaths = ["/settings", "/mcps", "/credentials", "/sandboxes", "/usage", "/infra"];
  root.dataset.sbMode = settingsPaths.indexOf(path) >= 0 ? "settings" : "app";
  if (window.matchMedia("(max-width: 900px)").matches) {
    root.dataset.sidebar = "closed";
    return;
  }
  try {
    root.dataset.sidebar = localStorage.getItem("octopus.sidebar.open") === "0" ? "closed" : "open";
  } catch (e) {
    root.dataset.sidebar = "open";
  }
})();
