#!/usr/bin/env python3
"""
Migrate every non-test .go file in internal/ and features/ to the
two-axis SCOPE annotation:

    // SCOPE:layer=<infra|feature>,removal=<core|plugin|feature>

The script:

1. Searches the WHOLE file for an existing `// SCOPE:` comment line
   (not just the leading doc group; some files have SCOPEs in
   mid-file positions). Pulls it to the top of the file and removes
   any continuation comment lines that belonged to the same block.
2. Re-classifies the existing comment to the new two-axis format
   using the REMAPPING table at the bottom of this file. Strips
   "DO NOT REMOVE -", "REMOVE if not using X -", etc., because the
   new axes already encode removal risk.
3. If no SCOPE comment exists, infers one from the file's path
   via the same table (fallback for unannotated files).
4. Skips _test.go files: the check-scope linter does not require
   SCOPE on tests. Existing annotations on test files are left
   untouched.
5. Templ-generated files (those starting with "// Code generated
   by templ"): the SCOPE line is inserted into the doc group
   immediately after the templ banner, so it survives regeneration.
6. Preserves the rest of the leading doc comment.

Idempotent: running twice is a no-op. The migration is irreversible
without git history, so commit before running.
"""

from __future__ import annotations

import argparse
import os
import re
import sys
from pathlib import Path

# Match a SCOPE comment line.
SCOPE_RE = re.compile(r"^//\s*SCOPE:.*$")

# Match the new canonical format.
NEW_RE = re.compile(
    r"^//\s*SCOPE:layer=(infra|feature),removal=(core|plugin|feature)\b"
)


# Old-format -> new-format mapping. Each tuple is
# (path_pattern, (layer, removal), short_description).
#
# short_description is appended after the SCOPE axes as a single
# sentence. Use empty string for files where the axes alone are
# self-explanatory.
REMAPPING: list[tuple[re.Pattern[str], tuple[str, str], str]] = [
    # ---- internal/queue: SSE Hub + goqite + workers ----
    (re.compile(r"^internal/queue/ssehub\.go$"),
     ("infra", "core"), "SSE Hub (in-memory fan-out, replay buffer, backpressure)"),
    (re.compile(r"^internal/queue/goqite\.go$"),
     ("infra", "core"), "Job queue (goqite) + Worker Pool"),
    (re.compile(r"^internal/queue/.*\.go$"),
     ("infra", "core"), "Queue handlers + retry + workers"),
    # ---- internal/server: bootstrap, uuid, crdtstore wire ----
    (re.compile(r"^internal/server/boot\.go$"),
     ("infra", "core"), "Server bootstrap (PocketBase, queue, router)"),
    (re.compile(r"^internal/server/uuid\.go$"),
     ("infra", "core"), "UUID generator for per-process identifiers"),
    (re.compile(r"^internal/server/crdtstore_wire.*\.go$"),
     ("infra", "plugin"), "CRDT store transport wiring (post-Init)"),
    # ---- internal/datastar: rendering helpers ----
    (re.compile(r"^internal/datastar/render\.go$"),
     ("infra", "core"), "Datastar rendering helpers (RenderAndPatch, MergeSignals)"),
    (re.compile(r"^internal/datastar/.*\.go$"),
     ("infra", "core"), "Datastar SSE helpers"),
    # ---- internal/secrets: age is wired in config.Load ----
    (re.compile(r"^internal/secrets/.*\.go$"),
     ("infra", "core"), "Secret management with age"),
    # ---- internal/components: shared UI primitives ----
    (re.compile(r"^internal/components/.*\.go$"),
     ("infra", "core"), "Shared UI helpers (Toast, OfflineBanner, SafeJSON)"),
    # ---- internal/dagnats: workflow engine ----
    (re.compile(r"^internal/dagnats/.*\.go$"),
     ("infra", "plugin"), "DagNats durable workflow engine"),
    # ---- internal/nats: NATS JetStream + Leaf Node ----
    (re.compile(r"^internal/nats/.*\.go$"),
     ("infra", "plugin"), "NATS JetStream + Leaf Node + CRUD proxy"),
    # ---- internal/llm: LLM client ----
    (re.compile(r"^internal/llm/.*\.go$"),
     ("infra", "plugin"), "GoAI LLM client (used by Suggest)"),
    # ---- internal/collab: Loro CRDT backs the whiteboard feature ----
    (re.compile(r"^internal/collab/.*\.go$"),
     ("infra", "plugin"), "Loro CRDT + DocStore + sync workers + presence"),
    # ---- features/store: interface + pbstore are core, crdtstore is plugin ----
    (re.compile(r"^features/store/store\.go$"),
     ("infra", "core"), "EntityStore interface (the contract every storage strategy implements)"),
    (re.compile(r"^features/store/pbstore/.*\.go$"),
     ("infra", "core"), "Default PocketBase EntityStore"),
    (re.compile(r"^features/store/crdtstore/.*\.go$"),
     ("infra", "plugin"), "Loro CRDT-backed EntityStore strategy"),
    # ---- features/app: AppContext bundles cross-cutting deps ----
    (re.compile(r"^features/app/app\.go$"),
     ("feature", "core"), "AppContext (cross-cutting deps bundle)"),
    # ---- features/auth: UI=feature/feature, middleware=feature/core ----
    (re.compile(r"^features/auth/(auth|wiring)\.go$"),
     ("feature", "core"), "Auth middleware (cookie + login/logout)"),
    (re.compile(r"^features/auth/.*\.go$"),
     ("feature", "feature"), "Login UI"),
    # ---- features/landing: pure demo ----
    (re.compile(r"^features/landing/.*\.go$"),
     ("feature", "feature"), "Public marketing landing page (GET /)"),
    # ---- features/config: read-only demo view ----
    (re.compile(r"^features/config/.*\.go$"),
     ("feature", "feature"), "Auth-gated read-only /config view (masked secrets)"),
    # ---- features/todo: the demo MVC ----
    (re.compile(r"^features/todo/.*\.go$"),
     ("feature", "feature"), "Todo MVC example (reference implementation)"),
    # ---- features/whiteboard: collaborative canvas demo ----
    (re.compile(r"^features/whiteboard/.*\.go$"),
     ("feature", "feature"), "Collaborative whiteboard (Loro CRDT canvas)"),
]


# Common prose prefixes that decorate SCOPE comments. The new format
# encodes the same information in the removal= axis, so we strip them
# ALL (no dash required — the original comments are inconsistent).
_RE_RATIONAL_PREFIXES = [
    r"^DO NOT REMOVE\s*-\s*",
    r"^DO NOT REMOVE\s+",
    r"^REMOVE if not using\s+\S+(?:\s+plugin|\s+feature)?\s*-\s*",
    r"^REMOVE if not using\s+\S+(?:\s+plugin|\s+feature)?\s+",
    r"^REMOVE\s+by\s+deleting\s+\S+\s*(?:and\s+the\s+\S+\s*)?-\s*",
    r"^REMOVE\s+by\s+deleting\s+\S+\s*(?:and\s+the\s+\S+\s*)?",
    r"^REMOVE\s+if\s+you\s+don't\s+want\s+\S+\s*-\s*",
    r"^REMOVE\s+if\s+you\s+don't\s+want\s+\S+\s+",
]


def normalize_old_rationale(line: str) -> str:
    """Extract the rationale after the SCOPE: prefix, stripping the
    'DO NOT REMOVE -', 'REMOVE if not using X', 'REMOVE by deleting
    X', 'REMOVE if you don't want X' decorations. Returns just the
    description of what the file does.
    """
    # Strip the leading // SCOPE:<old> - (or // SCOPE:<old>) prefix.
    m = re.match(r"^//\s*SCOPE:(?:core|plugin|feature)\s*(?:-\s*)?(.*)$", line)
    if not m:
        return ""
    body = m.group(1).strip()
    for pat in _RE_RATIONAL_PREFIXES:
        body = re.sub(pat, "", body, flags=re.IGNORECASE)
    body = body.strip(" .,")
    return body


# Redundant hints that duplicate the removal= axis. If the normalized
# rationale is JUST one of these, we drop it and let the axis carry
# the info — otherwise we'd write "// SCOPE:layer=...,removal=plugin
# — REMOVE if not using NATS" which is pure noise.
_RE_REDUNDANT_HINTS = [
    r"^REMOVE if not using\s+\S+(?:\s+plugin|\s+feature)?$",
    r"^REMOVE\s+by\s+deleting\s+\S+$",
    r"^REMOVE\s+if\s+you\s+don't\s+want\s+\S+$",
    r"^DO NOT REMOVE$",
]


def strip_redundant_hints(text: str) -> str:
    for pat in _RE_REDUNDANT_HINTS:
        if re.match(pat, text, re.IGNORECASE):
            return ""
    return text


def is_meaningful_description(text: str) -> bool:
    """True when the extracted text is a real description, not a
    dangling conjunction like 'and the call site' or '+ the file'.
    """
    if len(text) < 20:
        return False
    # Drop if it starts with a conjunction / article that suggests
    # we stripped too much.
    if re.match(r"^(and|or|the|to|see|with|of|in|for|by)\s", text, re.IGNORECASE):
        return False
    if text.startswith(("+", "-", ",", ".")):
        return False
    # Reject if a `+` or `-` separator appears mid-string: typical of
    # a list we partially stripped ("package + the call site").
    if re.search(r"\s[+\-]\s", text):
        return False
    return True


def find_continuation_lines(lines: list[str], scope_idx: int) -> int:
    """Return the number of additional `//` lines immediately after
    scope_idx that are continuations of the SCOPE sentence (start
    with a lowercase word, no blank `//` separator). Stops at the
    first blank `//` line, the first line starting with a capital
    word (Package / Handler / etc.), or the next SCOPE line.
    """
    count = 0
    j = scope_idx + 1
    while j < len(lines):
        line = lines[j]
        if not line.startswith("//"):
            break
        body = line[2:].strip()
        if not body:
            # blank comment line `//` ends the SCOPE sentence.
            break
        if SCOPE_RE.match(line):
            break
        # Capitalized first word -> new sentence / new paragraph.
        # Exception: single-letter connectors (a, I).
        first = body[0]
        if first.isupper() and body[:1] not in ("A", "I"):
            break
        # Starts with a lowercase letter or punctuation: continuation.
        count += 1
        j += 1
    return count


def classify(path: Path) -> tuple[str, str, str]:
    """Return (layer, removal, short_description) for a file path."""
    rel = str(path)
    for pat, axes, desc in REMAPPING:
        if pat.search(rel):
            return axes[0], axes[1], desc
    if rel.startswith("internal/"):
        return "infra", "plugin", ""
    if rel.startswith("features/"):
        return "feature", "feature", ""
    return "infra", "core", ""


def render_new_comment(layer: str, removal: str, description: str) -> str:
    """Build the new SCOPE line."""
    if description:
        return f"// SCOPE:layer={layer},removal={removal} — {description}"
    return f"// SCOPE:layer={layer},removal={removal}"


def migrate_file(path: Path, dry_run: bool) -> tuple[bool, list[str] | None]:
    """Return (changed, new_lines_or_None). When changed is True,
    new_lines holds the post-migration content; the caller is
    responsible for writing it.
    """
    text = path.read_text()
    lines = text.split("\n")

    # Skip _test.go files entirely.
    if str(path).endswith("_test.go"):
        return False, None

    # Find package declaration line.
    pkg_idx = None
    for i, line in enumerate(lines):
        if line.startswith("package "):
            pkg_idx = i
            break
    if pkg_idx is None:
        return False, None

    # Find the first SCOPE line anywhere in the file.
    old_scope_idx: int | None = None
    for j, line in enumerate(lines):
        if SCOPE_RE.match(line):
            old_scope_idx = j
            break

    layer, removal, desc = classify(path)

    # If a SCOPE exists, derive the description from it — but only
    # when the extracted rationale is a real sentence, not a
    # stripped-to-the-bone conjunction or a redundant REMOVE hint
    # already encoded in the removal= axis. Otherwise fall back to
    # the REMAPPING table default.
    if old_scope_idx is not None:
        old_rationale = normalize_old_rationale(lines[old_scope_idx])
        old_rationale = strip_redundant_hints(old_rationale)
        if is_meaningful_description(old_rationale):
            desc = old_rationale

    new_comment = render_new_comment(layer, removal, desc)

    # Idempotent: if the leading doc group already has the new SCOPE
    # at the canonical position, do nothing.
    if old_scope_idx is not None and old_scope_idx < pkg_idx:
        i = pkg_idx - 1
        while i >= 0 and (lines[i].startswith("//") or lines[i].strip() == ""):
            i -= 1
        start = i + 1
        for j in range(start, pkg_idx):
            if NEW_RE.match(lines[j]):
                return False, None
        # Old SCOPE is in the leading group but not in new format.
        # Drop the old line + its continuation block.
        cont = find_continuation_lines(lines, old_scope_idx)
        if dry_run:
            print(f"{path}: would replace lines {old_scope_idx + 1}-{old_scope_idx + cont + 1} with: {new_comment!r}")
        del lines[old_scope_idx:old_scope_idx + 1 + cont]
        pkg_idx -= 1 + cont
        # The old SCOPE line is gone; clear old_scope_idx so the
        # mid-file branch below doesn't try to drop a line at the
        # same index (which would now be the package decl).
        old_scope_idx = None
        # Fall through to the insertion block below — we still need
        # to add the new SCOPE line.

    # If SCOPE exists but is mid-file, drop it + continuation.
    if old_scope_idx is not None and old_scope_idx >= pkg_idx:
        cont = find_continuation_lines(lines, old_scope_idx)
        if dry_run:
            print(f"{path}: would drop mid-file SCOPE on lines {old_scope_idx + 1}-{old_scope_idx + cont + 1}")
        del lines[old_scope_idx:old_scope_idx + 1 + cont]

    # Find the leading doc group extent for insertion.
    i = pkg_idx - 1
    while i >= 0 and (lines[i].startswith("//") or lines[i].strip() == ""):
        i -= 1
    start = i + 1

    # Templ-generated file: keep SCOPE inside the banner block.
    # Insert AFTER both "// Code generated by templ" and
    # "// templ: version: ..." lines, after any blank comment lines,
    # and BEFORE the package doc paragraphs. Existing convention puts
    # the SCOPE between the version banner and the `// Package …`
    # doc.
    banner_idx = None
    for j in range(0, pkg_idx):
        if lines[j].startswith("// Code generated by templ"):
            banner_idx = j
            break
    if banner_idx is not None:
        end = banner_idx + 1
        # Advance past the templ banner (// Code generated and
        # // templ: version lines) and any blank comment lines that
        # may separate the banner from the doc paragraphs.
        while end < pkg_idx and (
            lines[end].startswith("// templ:")
            or lines[end].startswith("// Code generated")
            or lines[end].strip() == ""
        ):
            end += 1
        if dry_run:
            print(f"{path}: would insert at line {end + 1}: {new_comment!r}")
        lines.insert(end, new_comment)
        return True, lines

    # Hand-written file with no leading doc group: insert above package.
    if start == pkg_idx:
        if dry_run:
            print(f"{path}: would insert at line {pkg_idx + 1}: {new_comment!r}")
        lines.insert(pkg_idx, new_comment)
        return True, lines

    # Hand-written file with existing doc group: insert at top of group.
    if dry_run:
        print(f"{path}: would insert at line {start + 1}: {new_comment!r}")
    lines.insert(start, new_comment)
    return True, lines


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--dry-run", action="store_true", help="print changes without writing")
    ap.add_argument("--paths", default="internal,features", help="comma-separated roots")
    args = ap.parse_args()

    roots = [p.strip() for p in args.paths.split(",") if p.strip()]
    changed = 0
    for root in roots:
        for dirpath, _, filenames in os.walk(root):
            for name in filenames:
                if not name.endswith(".go"):
                    continue
                p = Path(dirpath) / name
                was_changed, new_lines = migrate_file(p, args.dry_run)
                if was_changed:
                    changed += 1
                    if not args.dry_run and new_lines is not None:
                        p.write_text("\n".join(new_lines))

    if args.dry_run:
        print(f"--dry-run: would modify {changed} file(s)")
    else:
        print(f"migrated {changed} file(s)")
    return 0


if __name__ == "__main__":
    sys.exit(main())