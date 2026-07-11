# UX Critique — Codebase Mode

**Date:** 2026-07-11
**Mode:** Codebase (no browser; ~80% coverage)
**Scope:** UI changed this session — navbar (views.templ), whiteboard (components.templ + whiteboard.js), theme (theme.js + theme_head.templ), todo layout.
**Appetite:** Core

## Severity legend
🚨 P0 blocking · 🤔 P1 major · 🔎 P2 minor · ✨ P3 polish

---

## Accessibility (WCAG AA)

| # | Finding | Sev | File | Note |
|---|---------|-----|------|------|
| 1 | Color `<input type="color">` has no `<label>` | 🤔 P1 | whiteboard/components.templ | `name="color"` + `data-bind`, but no associated `<label for>`. Screen readers announce "color, button". Add `<label for="color-input">` or `aria-label`. |
| 2 | Toolbar buttons are icon-less text but lack `aria-pressed` for active tool | 🔎 P2 | whiteboard.js | Active tool toggled via `btn-primary` class only. Add `aria-pressed` / `role="radio"` group for state exposure. |
| 3 | Theme toggle button has `aria-label` ✓ but icon-only | ✨ P3 | views.templ | Good — `aria-label="Toggle dark/light theme"` present. OK. |
| 4 | Canvas is a non-semantic `<canvas>` with no text alternative | 🔎 P2 | components.templ | Expected for a drawing surface; acceptable but note: board title/peer count are real text (good). No keyboard path to draw (expected for canvas). |
| 5 | `escapeHtml` on cursor label ✓ | ✨ P3 | whiteboard.js | XSS-safe. Good. |

## Nielsen Heuristics

| # | Finding | Sev | Note |
|---|---------|-----|------|
| 6 | **Visibility of system status** — net-status + peer-count added this session | ✅ | Strong: user sees offline/replay state and live peer count. |
| 7 | **Consistency** — navbar uses `menu-horizontal` on `sm+` but whiteboard "Back" is a separate link; two nav paradigms | 🔎 P2 | Minor: board page relies on "Back" link instead of the navbar "Whiteboards" entry. Both work; slight redundancy. |
| 8 | **Error prevention** — degenerate shapes ignored on pointerup ✓ | ✅ | `tiny` check prevents 0-size shapes. |
| 9 | **User control / freedom** — no undo/redo, no clear-board, no delete shape | 🤔 P1 | Once drawn, a shape cannot be removed from the UI. For a whiteboard this is a real gap (users make mistakes). Server supports remove op but client has no button. |
| 10 | **Recognition over recall** — tool names (Rectangle/Ellipse/Line/Pen) are explicit text ✓ | ✅ | Good. |

## Visual Hierarchy & Cognitive Load

| # | Finding | Sev | Note |
|---|---------|-----|------|
| 11 | Toolbar density acceptable (5 tools + color + status + back) | ✅ | Fits one row on desktop; wraps on mobile (`flex` no-wrap → may overflow on narrow screens) | 🤔 P1 | `toolbar flex gap-2` has no `flex-wrap`; on <640px the 5 buttons + color + status + back may overflow horizontally. Add `flex-wrap`. |
| 12 | Primary action unclear on board page | 🔎 P2 | There is no "share board" / "copy link" — collaborative board but no obvious way to invite. URL is the invite (acceptable for demo). |

## Mobile / Responsive

| # | Finding | Sev | Note |
|---|---------|-----|------|
| 13 | Canvas uses `absolute inset-0 w-full h-full` + DPR scaling ✓ | ✅ | `fitCanvas()` handles devicePixelRatio. Good. |
| 14 | Touch: `pointerdown/move/up` used (not mouse-only) ✓ | ✅ | Works for touch. `setPointerCapture` present. |
| 15 | Toolbar overflow on mobile (see #11) | 🤔 P1 | Add `flex-wrap` + maybe `overflow-x-auto`. |
| 16 | `net-status` text "offline — drawing is buffered" long on mobile | 🔎 P2 | Truncate/abbreviate on small screens. |

## AI Slop Detection

| # | Finding | Sev | Note |
|---|---------|-----|------|
| 17 | UI is purpose-built (DaisyUI, consistent tokens) — no generic AI patterns | ✅ | Good. No lorem, no redundant microcopy. |

## Emotional Journey

- **Peak:** seeing a peer's cursor appear live + shapes converge. ✅
- **Anxiety valley:** drawing while offline with no clear "will this save?" — mitigated by net-status badge (added this session). ✅ improved.
- **End:** "Back" returns to list; board persists (PocketBase). ✅

## Personas

- **Jordan (first-timer):** finds Whiteboard in navbar, draws immediately. ✅
- **Morgan (a11y):** color input unlabeled (#1), no keyboard draw (acceptable for canvas). ⚠️
- **Taylor (mobile):** toolbar overflow (#11/#15) is the main friction. ⚠️
- **Alex (power):** wants undo/delete/export — missing (#9). ⚠️

---

## Gap Registry (UX)

| Gap | Type | Sev | Resolution | Status |
|-----|------|-----|------------|--------|
| Color input unlabeled | a11y | P1 | Add `aria-label`/`label` | DOCUMENTED |
| No undo/delete/clear shape | feature | P1 | Add remove op button (server supports it) | DOCUMENTED |
| Toolbar overflows on mobile | responsive | P1 | `flex-wrap` + `overflow-x-auto` | FIXED (below) |
| Toolbar lacks `aria-pressed` | a11y | P2 | role=radiogroup | DOCUMENTED |
| Nav redundancy (Back vs navbar) | consistency | P2 | Keep both, acceptable | DOCUMENTED |

## Decision
📝 Documented gaps; one P1 (toolbar overflow) fixed inline. Undo/delete + a11y label are next-cycle (medium effort, architecturally trivial — server already supports remove op).
