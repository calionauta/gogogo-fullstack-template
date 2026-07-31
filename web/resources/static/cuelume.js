// Cuelume sound integration — VENDORED, plugin-style (features/sounds).
//
// Cuelume (https://github.com/Danilaa1/cuelume, MIT) is a curated palette
// of interaction sounds synthesized live with the Web Audio API — no audio
// files, no runtime dependencies. The library ships inside this repo under
// /static/cuelume/ (MIT license file alongside), so the binary has zero
// external deps. This module is the plugin's glue and owns the sound
// accessibility contract:
//
//   1. prefers-reduced-motion is respected by default — OS-level reduced
//      motion auto-mutes playback and reacts to runtime preference changes.
//      Opt out by clearing your OS setting; there is no in-app override
//      because this is an accessibility preference, not a taste setting.
//   2. Global mute — the navbar [data-sound-toggle] button persists a
//      preference in localStorage ("gogogo_sound", "on"|"off"), is
//      keyboard-accessible via aria-pressed, and survives Datastar morphs
//      (delegated click, same pattern as theme.js).
//   3. Subtle default volume — cuelume's mixer runs hotter than libraries
//      tuned for a 30% default; 0.4 keeps every cue audible without being
//      intrusive. Tune DEFAULT_VOLUME here (the API below also exposes
//      setVolume for a future settings panel).
//   4. Sounds are additive — every cue pairs with existing visual feedback
//      (toasts, button states, spinners) and never replaces it.
//
// Behavior wiring:
//   - bind()                     — enables declarative data-cuelume-* attrs.
//   - Global press               — pointer press on any interactive element
//                                 plays "press". Delegated (capture phase)
//                                 so Datastar-morphed DOM is covered;
//                                 elements with data-cuelume-press are left
//                                 to bind() to avoid double-firing. Works
//                                 on mouse, touch, and pen (pointer events).
//   - Toast feedback             — a MutationObserver on #toast-container
//                                 plays "success"/"error" when matching
//                                 toasts appear (visual equivalent always
//                                 present, so sound stays additive).
//
// Removing the plugin: delete features/sounds/, this file, the
// /static/cuelume/ directory, and the @sounds.SoundAssets() /
// @sounds.SoundToggle() calls from the templates.
import { bind, play, setEnabled, setVolume } from "./cuelume/index.js";

var STORAGE_KEY = "gogogo_sound";
var DEFAULT_VOLUME = 0.4;

bind();
setVolume(DEFAULT_VOLUME);

// --- preference state -----------------------------------------------------
// Two independent inputs, combined with logical AND:
//   userPref      — explicit navbar toggle, persisted in localStorage.
//   reducedMotion — OS-level prefers-reduced-motion (auto-mute).
var state = {
  userPref: readPref(),
  reducedMotion: false,
};

function readPref() {
  try {
    return localStorage.getItem(STORAGE_KEY) === "off" ? "off" : "on";
  } catch (e) {
    return "on";
  }
}

function effective() {
  return state.userPref === "on" && !state.reducedMotion;
}

function apply() {
  setEnabled(effective());
  syncToggleUI();
}

// Keep the navbar button in sync with the effective state (user pref AND
// OS reduced-motion). Exactly one icon visible; aria-pressed reflects the
// audible state so screen readers announce whether sound is on.
function syncToggleUI() {
  var btn = document.querySelector("[data-sound-toggle]");
  if (!btn) return;
  var on = effective();
  btn.setAttribute("aria-pressed", String(on));
  var icons = btn.querySelectorAll(".sound-toggle-icon");
  icons.forEach(function (el) {
    var isOffIcon = el.classList.contains("icon-sound-off");
    el.style.display = (on !== isOffIcon) ? "" : "none";
  });
  btn.setAttribute(
    "title",
    state.reducedMotion && state.userPref === "on"
      ? "Sound off — your device prefers reduced motion"
      : on ? "Mute sounds" : "Unmute sounds"
  );
}

function onToggleClick() {
  state.userPref = state.userPref === "on" ? "off" : "on";
  try {
    localStorage.setItem(STORAGE_KEY, state.userPref);
  } catch (e) {
    /* private mode / quota — preference applies for this session */
  }
  apply();
}

// Delegated toggle so it works regardless of when the navbar renders
// (Datastar morphs, re-renders). Same pattern as theme.js.
document.addEventListener("click", function (e) {
  var t =
    e.target && e.target.closest
      ? e.target.closest("[data-sound-toggle]")
      : null;
  if (!t) return;
  e.preventDefault();
  onToggleClick();
});

// OS-level reduced-motion: mute by default and react to live changes.
var mq = window.matchMedia("(prefers-reduced-motion: reduce)");
state.reducedMotion = mq.matches;
function onMqChange(e) {
  state.reducedMotion = e.matches;
  apply();
}
if (mq.addEventListener) mq.addEventListener("change", onMqChange);
else if (mq.addListener) mq.addListener(onMqChange); // Safari < 14

apply();

// --- global button-press feedback -----------------------------------------
// A muted knock on any pointer press over a button-like element. The
// selector mirrors cuelume's own defaults so "each action has a sound"
// holds without tagging every button. play() no-ops when muted.
function pressFromEvent(e) {
  var t =
    e.target && e.target.closest
      ? e.target.closest(
          'button, [role="button"], [role="tab"], [class*="btn"], input[type="checkbox"], input[type="submit"]'
        )
      : null;
  if (!t) return;
  if (t.hasAttribute("data-cuelume-press")) return;
  play("press");
}
document.addEventListener("pointerdown", pressFromEvent, true);

// --- success / error feedback via toasts ----------------------------------
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

// --- tiny public API for future settings surfaces --------------------------
window.Cuelume = {
  play: play,
  setEnabled: setEnabled,
  setVolume: setVolume,
  isEnabled: effective,
};
