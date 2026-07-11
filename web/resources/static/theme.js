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
    syncIcons(theme);
    document.dispatchEvent(
      new CustomEvent("themechange", { detail: { theme: theme } })
    );
  }

  // Explicitly show only the icon that matches the active theme. In
  // light mode the moon (icon-dark-mode) is shown so the button reads
  // "switch to dark"; in dark mode the sun is shown. The CSS [data-theme]
  // rules are the primary mechanism, but iconify-icon is a custom element
  // whose own stylesheet can override the host display; setting it
  // directly here guarantees exactly one icon is visible regardless of
  // cascade order. Called on init and after every change.
  function syncIcons(theme) {
    var icons = document.querySelectorAll(".theme-toggle-icon");
    icons.forEach(function (el) {
      var isDarkIcon = el.classList.contains("icon-dark-mode");
      // In light mode show the sun (light icon); in dark mode show the moon.
      var active = (theme === LIGHT) !== isDarkIcon;
      el.style.display = active ? "" : "none";
    });
  }

  // Expose a stable API for the navbar toggle button.
  window.Theme = {
    get: current,
    current: current,
    set: apply,
    toggle: function () {
      var next = current() === DARK ? LIGHT : DARK;
      apply(next);
      // Micro-animation: briefly spin/scale the toggle icon (matches the
      // treinador project's feel). The CSS class is added then removed.
      var icons = document.querySelectorAll(".theme-toggle-icon");
      icons.forEach(function (el) {
        el.classList.remove("theme-toggle-spin");
        // Force reflow so the animation can retrigger on rapid toggles.
        void el.offsetWidth;
        el.classList.add("theme-toggle-spin");
        setTimeout(function () {
          el.classList.remove("theme-toggle-spin");
        }, 320);
      });
      return next;
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

  // Initial icon sync: ensure exactly one toggle icon is visible based
  // on the theme already applied by ThemeHead before paint.
  syncIcons(current());
})();
