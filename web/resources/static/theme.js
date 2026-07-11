// Theme controller — dark/light mode with localStorage persistence.
//
// Design notes (kept deliberately small and dependency-free):
//   - The actual theme is applied via the `data-theme` attribute on <html>,
//     which DaisyUI v5 reads to swap its design tokens. No class toggling,
//     no CSS rebuild.
//   - A tiny inline script in the document <head> (ThemeHead) sets the
//     attribute BEFORE first paint to avoid a flash of the wrong theme.
//     This module owns everything AFTER that: persisting the choice and
//     toggling it from the navbar button.
//   - Storage key is "theme"; value is "light" | "dark". Falls back to the
//     OS preference (prefers-color-scheme) when nothing is stored, and to
//     "light" if even that is unavailable (older browsers / private mode).
(function () {
  "use strict";

  var KEY = "theme";
  var DARK = "dark";
  var LIGHT = "light";

  function current() {
    var t = document.documentElement.getAttribute("data-theme");
    if (t === DARK || t === LIGHT) return t;
    return prefersDark() ? DARK : LIGHT;
  }

  function prefersDark() {
    return (
      window.matchMedia &&
      window.matchMedia("(prefers-color-scheme: dark)").matches
    );
  }

  function apply(theme) {
    document.documentElement.setAttribute("data-theme", theme);
    // Keep the inline head script and any listener in sync.
    try {
      localStorage.setItem(KEY, theme);
    } catch (e) {
      /* private mode / quota — non-fatal, theme still applies this session */
    }
    document.dispatchEvent(
      new CustomEvent("themechange", { detail: { theme: theme } })
    );
  }

  // Expose a stable API for the navbar toggle button.
  window.Theme = {
    get: current,
    set: apply,
    toggle: function () {
      apply(current() === DARK ? LIGHT : DARK);
      return current();
    },
  };

  // React to OS-level preference changes only when the user hasn't made an
  // explicit choice (nothing in localStorage yet). Once they pick, their
  // choice wins until they toggle again.
  if (window.matchMedia) {
    var mq = window.matchMedia("(prefers-color-scheme: dark)");
    var listener = function (e) {
      var stored;
      try {
        stored = localStorage.getItem(KEY);
      } catch (err) {
        stored = null;
      }
      if (!stored) {
        document.documentElement.setAttribute(
          "data-theme",
          e.matches ? DARK : LIGHT
        );
      }
    };
    if (mq.addEventListener) mq.addEventListener("change", listener);
    else if (mq.addListener) mq.addListener(listener); // Safari < 14
  }
})();
