// Applies the saved color theme before first paint. Loaded synchronously in <head>.
// "system" (or nothing saved) leaves data-theme unset so prefers-color-scheme decides.
(function () {
  "use strict";
  var KEY = "himay-theme";
  function read() {
    try { return localStorage.getItem(KEY) || "system"; } catch (e) { return "system"; }
  }
  function apply(mode) {
    var root = document.documentElement;
    if (mode === "light" || mode === "dark") root.setAttribute("data-theme", mode);
    else root.removeAttribute("data-theme");
  }
  function sync(mode) {
    var btns = document.querySelectorAll("[data-theme-set]");
    for (var i = 0; i < btns.length; i++) btns[i].setAttribute("aria-pressed", String(btns[i].dataset.themeSet === mode));
  }
  apply(read());
  window.himayTheme = {
    get: read,
    set: function (mode) {
      try { if (mode === "system") localStorage.removeItem(KEY); else localStorage.setItem(KEY, mode); } catch (e) {}
      apply(mode);
      sync(mode);
    },
    sync: function () { sync(read()); }
  };
  document.addEventListener("DOMContentLoaded", window.himayTheme.sync);
  document.addEventListener("click", function (e) {
    var b = e.target.closest && e.target.closest("[data-theme-set]");
    if (b) window.himayTheme.set(b.dataset.themeSet);
  });
  // Keep other open Himay tabs in step.
  window.addEventListener("storage", function (e) {
    if (e.key === KEY) { apply(read()); sync(read()); }
  });
})();
