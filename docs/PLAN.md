# PLAN.md — multi-session roadmap for gogogo-fullstack-template

> Living document. Each numbered section is a **future session**.
> Read "What we'll do" before opening the session to know the starting
> point; "Exit criteria" tells you when the session is done.
>
> Update this file at the end of each session, not during. The session
> that closes work also commits the updated PLAN.md.

---

## 1. Wails v3 — desktop + mobile builds

### What we'll do

Add an optional second frontend target to the same project: a native
desktop app (Windows/macOS/Linux) and a mobile app (Android/iOS),
both built on top of the same Go business logic that already powers
the web app. Wails v3 wraps the existing handlers in a webview and
binds Go methods to JS — we add two thin entry points (`cmd/desktop/`,
`cmd/mobile/`) without rewriting the web stack.

CI strategy: GitHub Actions runners build all platform artifacts.
A forker who can push a tag gets a release with `.dmg`, `.msi`,
`.deb`, `.AppImage`, `.apk`, `.ipa` attached — zero local
toolchains required.

### Exit criteria

- [ ] `make desktop` produces a runnable macOS `.app` locally
- [ ] `make android` produces a runnable `.apk`
- [ ] `.github/workflows/build-platforms.yml` runs on every push and
      uploads 6 platform artifacts
- [ ] `cmd/desktop/main.go` and `cmd/mobile/main.go` are < 200 lines
      each, reusing the existing router/handlers
- [ ] README has a "Desktop & Mobile" section with screenshot

---

## 2. Bug #6 — RESOLVED in commit 23d69b5 ✅

**Status**: fixed. No further action.

The 303 redirect loop on `/` and `/login` was caused by registering
routes inside `app.OnServe().BindFunc(...)` from inside another
`OnServe` handler. PocketBase's `Hook.Trigger` snapshots registered
handlers before any of them run, so nested `BindFunc`s are silently
a no-op. Fix: `RegisterAuth` now takes `*core.ServeEvent` and wires
routes directly on `se.Router` inside the existing top-level
router.OnServe hook. See AGENTS.md §"Wiring HTTP routes" for the
full rule and `http://localhost:8080/<path>` curl recipes.

**Follow-up still pending (manual, dashboard-only):** the Cloudflare
Tunnel `b56a1467-...` does not include `gogogo.calionauta.com`
in its local ingress config (DNS CNAME points at the tunnel, but
cloudflared doesn't have a rule for that hostname). Until the
tunnel config is updated in the Cloudflare Zero Trust dashboard,
requests to `gogogo.calionauta.com` resolve through Cloudflare
Edge but don't terminate at the local server. Operator action:
https://dash.cloudflare.com → `calionauta.com` → Zero Trust →
Networks → Tunnels → current tunnel → Public hostname → add
`gogogo.calionauta.com` → service `http://127.0.0.1:8080`.

---

## 3. Local-first repo CI as reusable workflow

The current `make check` / `make gate` works locally. Next session
extracts this into a reusable GitHub Actions workflow
(`.github/workflows/lint-test.yml`) that downstream forks can
inherit with zero config.

---

## 4. Multi-project promotion

If `gogogo-fullstack-template` becomes the foundation for `stelow`
or `datastar-lint`, the canonical `/var/lib/<name>/data/` bind-mount
layout is the shared convention. Extract `scripts/deploy-prod.sh`
into a separate `gogogo-deploy` repo so the three projects share
the same deploy runner.

---

## 5. Pending cleanups

- [ ] `/home/deploy/gogogo-template/` (legacy deploy dir on the
      server) can be removed once `gogogo-fullstack-template` is
      stable for a full week
- [ ] Old `gogogo.calionauta.com` install link JWTs have all expired
      — current install link printed by `docker logs` on every
      restart; operator creates superuser via the UI at that link
- [ ] The first superuser for `gogogo.calionauta.com` has not yet
      been created — operator must open the install link and submit
      the form (see Bug #6 status)

---

## 6. On-prem deploy permission gotcha (real bug, real fix)

**Symptom** (this session, 2026-07-08): PocketBase container fails
on every restart with `sqlite3: unable to open database file:
permission denied`. The data dir is on a Docker named volume
(`gogogo-fullstack-template-data:/var/lib/.../data`). Docker
creates new volumes with `root:root` ownership, but the container
runs as `USER 65532:65532`. Result: crash loop on first deploy to a
fresh server.

**Root cause**: Docker volume permissions are not negotiated against
the container's `USER`. Bind mounts work because `deploy-prod.sh`
can `chown 65532:65532` the host directory before starting.

**Fix shipped in commit `2c8a1f6`** (this session):
- `deploy/docker-compose.prod.yml`: switched to bind mount
  `/var/lib/${APP_NAME}/data:/var/lib/${APP_NAME}/data`
- `scripts/deploy-prod.sh`: creates `/var/lib/${APP_NAME}/data` with
  `chown 65532:65532` before bringing the container up

**Lesson for other projects**: prefer bind mounts over named volumes
when the container runs as a non-root UID. Bind mounts give the
operator full file-system access for inspection, backup, and
permission repair. Named volumes are appropriate only when the
container runs as root (rare for production).

---

## Notes for whoever picks this up

- The deploy pipeline is **Pattern B** (shell key, image built on
  server) — see `~/.agents/skills/cali-ops-deploy-github-tailscale/`
  for the two patterns and when to use each.
- The redirect-loop and `http.ServeMux` subtree-matching gotchas are
  documented in AGENTS.md §"Wiring HTTP routes".
- The privacy filter-repo pass is documented in the git history;
  backup mirror at `~/backups/gogogo-template-*.git/`.
- For PocketBase permissions, the rule is: **`scripts/deploy-prod.sh`
  creates `/var/lib/<APP_NAME>/data` with `chown 65532:65532`**,
  and `deploy/docker-compose.prod.yml` bind-mounts it. Don't switch
  to named volumes unless the container runs as root.