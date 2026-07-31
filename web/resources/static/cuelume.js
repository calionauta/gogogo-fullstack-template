// Cuelume sound integration — VENDORED, not a package dependency.
//
// Cuelume (https://github.com/Danilaa1/cuelume, MIT) is a curated palette
// of interaction sounds synthesized live with the Web Audio API — no audio
// files, no runtime dependencies. The library is shipped inside this repo
// under /static/cuelume/ (MIT license file alongside it) so the binary has
// zero external deps. This module is the glue:
//
//   1. bind()           — enables any declarative data-cuelume-* attributes
//                         (hover / press / release / toggle) used in markup.
//   2. Global press     — every pointer press on an interactive element
//                         plays the "press" sound. Delegated on document
//                         (capture phase) so it also covers elements that
//                         Datastar morphs in later; elements that already
//                         declare data-cuelume-press are left to bind() to
//                         avoid double-firing.
//   3. Toast feedback   — toasts appended to #toast-container carry an
//                         alert-success / alert-error class; a Mutation-
//                         Observer plays the matching "success" / "error"
//                         sound the moment they appear.
import { bind, play } from "./cuelume/index.js";

bind();

// --- 2. Global button-press feedback -------------------------------------
// A muted knock on any pointer press over a button-like element. The
// selector mirrors cuelume's own defaults (buttons, role=button/tab,
// .btn links, checkboxes) so "each action has a sound" holds without
// tagging every button by hand.
function pressFromEvent(e) {
  var t =
    e.target && e.target.closest
      ? e.target.closest(
          'button, [role="button"], [role="tab"], [class*="btn"], input[type="checkbox"], input[type="submit"]'
        )
      : null;
  if (!t) return;
  // Elements carrying the declarative attribute are handled by bind()'s
  // own pointerdown listener — skip to avoid two sounds for one press.
  if (t.hasAttribute("data-cuelume-press")) return;
  play("press");
}
document.addEventListener("pointerdown", pressFromEvent, true);

// --- 3. Success / error feedback via toasts ------------------------------
// Every backend toast lands in #toast-container as a .toast-msg whose
// inner alert carries the type class (alert-success / alert-error).
function toastAdded(mutations) {
  for (var i = 0; i < mutations.length; i++) {
    var added = mutations[i].addedNodes;
    for (var j = 0; j < added.length; j++) {
      var node = added[j];
      if (!node || node.nodeType !== 1) continue;
      var toast =
        node.classList && node.classList.contains("toast-msg")
          ? node
          : node.querySelector && node.querySelector(".toast-msg");
      if (!toast) continue;
      if (toast.querySelector(".alert-success")) play("success");
      else if (toast.querySelector(".alert-error")) play("error");
    }
  }
}
function watchToasts() {
  var container = document.getElementById("toast-container");
  if (!container) return;
  new MutationObserver(toastAdded).observe(container, { childList: true });
}
if (document.readyState === "loading") {
  document.addEventListener("DOMContentLoaded", watchToasts);
} else {
  watchToasts();
}
