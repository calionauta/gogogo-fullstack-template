// SCOPE:core - Browser smoke test (headless). Builds the binary, boots it
// on an ephemeral port + data dir, creates a test user, loads the real app
// pages, and exercises offline todo create/delete through Service Worker +
// IndexedDB + reconnect replay. It FAILS on uncaught client-side JS errors,
// a stuck offline form, a mutation that was not queued, or replay that does
// not converge the UI. `make ci-local` / CI run it before deployment.
//
// No project cache is involved: a fresh browser context is used every run,
// so it loads the genuine served HTML (not a stale browser/Cloudflare copy).
//
// Requires: `npx playwright install chromium` (run by `make smoke`).

import { chromium } from "playwright";
import { spawn, spawnSync, execSync } from "node:child_process";
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";

const PORT = Number(process.env.SMOKE_PORT || 8099);
const BASE = `http://127.0.0.1:${PORT}`;
const SU_EMAIL = "smoke-superuser@local.dev";
const SU_PASS = "SmokeSuperuserPass!123";
const USER_EMAIL = "smoke-user@local.dev";
const USER_PASS = "SmokeUserPass!123";

const ROUTES = ["/todo", "/whiteboard", "/login"];

const fail = (msg) => {
  console.error("❌ " + msg);
  process.exitCode = 1;
};

const tmp = mkdtempSync(join(tmpdir(), "gogogo-smoke-"));
const pbDir = join(tmp, "pb");
const runtimeDir = join(tmp, "runtime");
mkdirSync(runtimeDir, { recursive: true });
const runtimeEnv = {
  ...process.env,
  DATA_DIR: runtimeDir,
  DATABASE_PATH: join(runtimeDir, "app.db"),
  NATS_ENABLED: "false",
  DAGNATS_ENABLED: "false",
  OFFLINE_SYNC_ENABLED: "true",
};
const providedBin = process.env.SMOKE_BIN;
const bin = providedBin ? resolve(providedBin) : join(tmp, "web");

let server = null;
let browser = null;

async function api(method, path, { body, token } = {}) {
  const res = await fetch(BASE + path, {
    method,
    headers: {
      "Content-Type": "application/json",
      ...(token ? { Authorization: "Bearer " + token } : {}),
    },
    body: body ? JSON.stringify(body) : undefined,
  });
  const text = await res.text();
  let json = null;
  try {
    json = text ? JSON.parse(text) : null;
  } catch {
    /* non-JSON */
  }
  return { status: res.status, json };
}

async function waitForHealth(timeoutMs = 30000) {
  const start = Date.now();
  while (Date.now() - start < timeoutMs) {
    try {
      const r = await fetch(BASE + "/health");
      if ((await r.text()).trim() === "ok") return;
    } catch {
      /* server not up yet */
    }
    await new Promise((r) => setTimeout(r, 500));
  }
  throw new Error("server did not become healthy in " + timeoutMs + "ms");
}

async function pendingMutationCount(page) {
  return page.evaluate(() =>
    Promise.race([
      new Promise((resolve, reject) => {
        const open = indexedDB.open("pb-offline-queue", 1);
        open.onerror = () => reject(open.error);
        open.onsuccess = () => {
          const db = open.result;
          const tx = db.transaction("pending", "readonly");
          const count = tx.objectStore("pending").count();
          count.onerror = () => reject(count.error);
          count.onsuccess = () => {
            db.close();
            resolve(count.result);
          };
        };
      }),
      new Promise((_, reject) =>
        setTimeout(() => reject(new Error("timed out reading offline queue")), 5000),
      ),
    ]),
  );
}

async function waitForServiceWorkerControl(page) {
  await page.evaluate(async () => {
    if (!("serviceWorker" in navigator)) throw new Error("service worker unsupported");
    await navigator.serviceWorker.ready;
  });
  if (!(await page.evaluate(() => Boolean(navigator.serviceWorker.controller)))) {
    await page.reload({ waitUntil: "load" });
    await page.evaluate(() => navigator.serviceWorker.ready);
  }
}

async function verifySingleOnlineTodoSubmit(page, skin) {
  if (skin !== "basecoat") return;

  const title = `online-single-submit-${Date.now()}`;
  const skinPath = `/todo?skin=${encodeURIComponent(skin)}`;

  console.log("→ Exercising Basecoat rapid online submit guard…");
  await page.goto(BASE + skinPath, { waitUntil: "load", timeout: 20000 });
  await page.evaluate(() => {
    const originalFetch = window.fetch.bind(window);
    let release;
    window.__smokeHeldTodoPost = {
      count: 0,
      release: () => release?.(),
      restore: () => {
        window.fetch = originalFetch;
      },
    };
    window.fetch = async (input, init) => {
      const url = typeof input === "string" ? input : input.url;
      if ((init?.method || "GET").toUpperCase() !== "POST" || !url.includes("/api/todos")) {
        return originalFetch(input, init);
      }
      window.__smokeHeldTodoPost.count++;
      await new Promise((resolve) => {
        release = resolve;
      });
      return originalFetch(input, init);
    };
  });

  try {
    const titleInput = page.getByPlaceholder("Add a new todo...");
    await titleInput.fill(title);
    await titleInput.press("Enter");
    await titleInput.press("Enter");
    await page.waitForFunction(() => window.__smokeHeldTodoPost.count >= 1);
    const postCount = await page.evaluate(() => window.__smokeHeldTodoPost.count);
    if (postCount !== 1) {
      throw new Error(`rapid Basecoat submit made ${postCount} requests; expected exactly one`);
    }
  } finally {
    await page.evaluate(() => {
      window.__smokeHeldTodoPost.release();
      window.__smokeHeldTodoPost.restore();
    });
  }

  const row = page
    .locator('[id^="todo-"]:not(#todo-list)')
    .filter({ hasText: title });
  await row.waitFor({ state: "visible", timeout: 20000 });
  console.log("  ✓ Basecoat rapid online submit sends one request");
}

async function verifyOfflineTodoQueue(page, context, skin = "daisyui") {
  const title = `offline-smoke-${Date.now()}`;
  const skinPath = skin === "daisyui" ? "/todo" : `/todo?skin=${encodeURIComponent(skin)}`;
  console.log(`→ Exercising offline todo add + delete queue (skin=${skin})…`);
  await page.goto(BASE + skinPath, { waitUntil: "load", timeout: 20000 });
  await waitForServiceWorkerControl(page);

  const banner = page.locator("#offline-banner");
  if ((await banner.getAttribute("data-offline-sync")) !== "true") {
    throw new Error("offline banner rendered with offline sync disabled");
  }

  // CAL-34 regression guard: the SW postMessage bridge in
  // internal/components/offline_banner.templ dispatches the event as
  // `gogogo:queued` (Datastar event-namespace separator is the colon).
  // The morpheus + basecoat skins previously listened for
  // `gogogo__queued` (double underscore) — a typo that left $loading
  // stuck true after an offline add, blocking every follow-up submit.
  // Catch that here by asserting the rendered attribute uses the
  // colon separator before the window modifier.
  const queuedListener = await page.evaluate(() => {
    for (const el of document.querySelectorAll("*")) {
      for (const attr of el.attributes) {
        if (attr.name.startsWith("data-on:gogogo") && attr.name.includes("queued")) {
          return attr.name;
        }
      }
    }
    return null;
  });
  if (queuedListener !== "data-on:gogogo:queued__window") {
    throw new Error(
      `skin=${skin} listens for "${queuedListener ?? "<none>"}"; ` +
        'expected "data-on:gogogo:queued__window" (Datastar event namespace is ":", not "__")',
    );
  }

  try {
    await context.setOffline(true);
    await page.waitForFunction(() =>
      document.querySelector("#offline-banner-text")?.textContent?.includes("queued"),
    );

    const titleInput = page.getByPlaceholder("Add a new todo...");
    const addButton = page.getByRole("button", { name: "Add" });
    await titleInput.fill(title);
    // Submit via Enter on the input — works for every skin regardless
    // of whether the Add trigger is a native <button> (DaisyUI/Basecoat)
    // or a <neo-button> web component (Morpheus). Clicking the neo-button
    // doesn't always dispatch a native form-submit event in headless
    // Chromium, which is fine for users (they click too) but breaks the
    // headless harness; Enter on the input bypasses the question.
    await titleInput.press("Enter");
    await page.waitForFunction(() =>
      document.querySelector('input[name="title"]')?.value === "",
    );

    // A second title must re-enable Add. This catches the CAL-34 bug
    // where the first offline request left $loading=true forever.
    await titleInput.fill(title + "-probe");
    if (await addButton.isDisabled()) {
      throw new Error("Add stayed disabled after an offline mutation queued");
    }
    await titleInput.fill("");

    if ((await pendingMutationCount(page)) !== 1) {
      throw new Error("offline create was not persisted in IndexedDB");
    }

    await context.setOffline(false);
    // Row class differs per skin (DaisyUI/Morpheus use .todo-item, Basecoat
    // uses .item); every skin uses id="todo-<id>" for the row, and the
    // container is #todo-list (which itself starts with "todo-"). Scope by
    // id prefix, exclude the list container, and narrow by text for the
    // exact row match.
    const row = page
      .locator('[id^="todo-"]:not(#todo-list)')
      .filter({ hasText: title });
    await row.waitFor({ state: "visible", timeout: 20000 });
    // Wait for replay to drain the IndexedDB queue. The morpheus
    // skin's SSE patch is structurally different from the SW's
    // optimistic row (DaisyUI-classed div vs. neo-button row), so the
    // data-pending swap that DaisyUI gets is unreliable across skins.
    // The pending-count drain is the single source of truth for
    // "the server accepted the replay".
    await page.waitForFunction(async () => {
      const open = indexedDB.open("pb-offline-queue", 1);
      const db = await new Promise((resolve, reject) => {
        open.onerror = () => reject(open.error);
        open.onsuccess = () => resolve(open.result);
      });
      const tx = db.transaction("pending", "readonly");
      const request = tx.objectStore("pending").count();
      const count = await new Promise((resolve, reject) => {
        request.onerror = () => reject(request.error);
        request.onsuccess = () => resolve(request.result);
      });
      db.close();
      return count === 0;
    }, null, { timeout: 15000 });
    // DaisyUI: also wait for the SW's optimistic row to be replaced by
    // the server row (needed because the delete test below clicks the
    // row's enabled delete button, which the disabled optimistic
    // button would block). The other skins handle the optimistic→server
    // swap differently, so this guard is daisyui-only.
    if (skin === "daisyui") {
      await page.waitForFunction(
        (t) => {
          const els = document.querySelectorAll('[id^="todo-"]:not(#todo-list)');
          for (const el of els) {
            if (el.textContent?.includes(t) && el.getAttribute("data-pending") !== "true") {
              return true;
            }
          }
          return false;
        },
        title,
        { timeout: 20000 },
      );
    }
    await page.waitForFunction(async () => {
      const open = indexedDB.open("pb-offline-queue", 1);
      const db = await new Promise((resolve, reject) => {
        open.onerror = () => reject(open.error);
        open.onsuccess = () => resolve(open.result);
      });
      const tx = db.transaction("pending", "readonly");
      const request = tx.objectStore("pending").count();
      const count = await new Promise((resolve, reject) => {
        request.onerror = () => reject(request.error);
        request.onsuccess = () => resolve(request.result);
      });
      db.close();
      return count === 0;
    }, null, { timeout: 10000 });

    // Queue a delete offline too, then prove it replays and the UI converges.
    // The delete-confirm dialog auto-open behaviour varies per skin:
    //   - DaisyUI: the row button calls .showModal() inline.
    //   - Basecoat/Morpheus: the row button only sets $confirmingDeleteId;
    //     the dialog's open/close wiring is skin-specific. Keep this part
    //     of the sweep daisyui-only so the harness stays decoupled from
    //     pre-existing skin-specific dialog wiring; the CAL-34 contract
    //     (offline add resets UI + queue + replay) is already covered
    //     above for every skin.
    if (skin !== "daisyui") {
      console.log(`  ✓ skin=${skin}: offline add resets UI, queues, replays`);
      return;
    }
    await context.setOffline(true);
    // The delete-button shape varies per skin:
    //   - DaisyUI: button[title="Delete todo"] (no aria-label)
    //   - Basecoat: button[aria-label="Delete"]
    //   - Morpheus: neo-button[data-neo-dialog-trigger="confirm-delete-modal"]
    // The row locator above already targets the right row (filtered by
    // text), so scope the click to that row and match any of the three
    // shapes. The confirm-modal "Delete" button lives inside the
    // dialog, so scoping to the row keeps the locator unambiguous.
    await row.locator(
      'button[aria-label*="Delete"], button[title*="Delete"], neo-button[data-neo-dialog-trigger="confirm-delete-modal"]',
    ).first().click();
    const dialog = page.getByRole("dialog");
    await dialog.getByRole("button", { name: "Delete", exact: true }).click();
    await dialog.waitFor({ state: "hidden" });
    if ((await pendingMutationCount(page)) !== 1) {
      throw new Error("offline delete was not persisted in IndexedDB");
    }

    await context.setOffline(false);
    await row.waitFor({ state: "detached", timeout: 20000 });
    console.log(`  ✓ skin=${skin}: offline add resets UI, queues, replays; offline delete replays`);
  } finally {
    await context.setOffline(false);
  }
}

async function verifyOfflineUx(page, context) {
  // Bundles the CAL-9 offline-UX contract under one harness run:
  //   1. the header presence pill (.online-pill) greys out (is-offline) when the
  //      network drops — this is the bug the v0.23.5 fix closed (the header used
  //      to render a live-green dot via Tailwind bg-success/animate-ping instead
  //      of the shared .online-pill that reflectPresence() drives off navigator.onLine);
  //   2. navigating to a VISITED page while offline is served from the SW PAGE_CACHE
  //      (network-first with cache fallback), not ERR_INTERNET_DISCONNECTED;
  //   3. the auth navbar logout posts {type:'clear-pages'} so the page cache is
  //      purged on sign-out (shared-device safety).
  const pill = page.locator(".online-pill");
  console.log("→ Exercising offline-UX (presence pill + SW nav cache + clear-pages)…");

  // Cache /todo under the SW BEFORE going offline. The very first navigation
  // may predate SW control (or clients.claim() may take over the page without
  // a fresh navigation), so we explicitly (re)load once control is established
  // — that navigation is what networkFirstPage intercepts and caches. Without
  // this, caches.match('/todo') is empty and the offline navigation test below
  // has nothing to serve from the SW cache.
  await page.goto(BASE + "/todo", { waitUntil: "load", timeout: 20000 });
  await waitForServiceWorkerControl(page);
  await page.reload({ waitUntil: "load" });
  await waitForServiceWorkerControl(page);

  // 1) online => pill must NOT be greyed out. Allow a brief settle in case
  // navigator.onLine reported false transiently during SW install/claim.
  await new Promise((r) => setTimeout(r, 1500));
  const pills = await page.evaluate(() => {
    const els = [...document.querySelectorAll(".online-pill")];
    return els.map((e) => ({ cls: e.className, text: e.textContent.trim().slice(0, 30), online: navigator.onLine }));
  });
  console.log("ALL_PILLS:", JSON.stringify(pills));
  const pillState = await pill.first().evaluate((el) => ({
    online: navigator.onLine,
    off: el.classList.contains("is-offline"),
    cls: el.className,
  }));
  console.log("PILL_STATE:", JSON.stringify(pillState));
  if (pillState.off) {
    throw new Error("presence pill shows is-offline while online (should be live)");
  }

  // The page must be cached by the SW for offline navigation to have something to serve.
  // networkFirstPage caches asynchronously after the response, so poll until it lands.
  let cached = false;
  for (let i = 0; i < 16; i++) {
    cached = await page.evaluate(
      (u) => caches.match(u).then((r) => !!r),
      BASE + "/todo",
    );
    if (cached) break;
    await new Promise((r) => setTimeout(r, 500));
  }
  if (!cached) {
    const dump = await page.evaluate(async () => {
      const out = {};
      for (const n of await caches.keys()) {
        const c = await caches.open(n);
        out[n] = (await c.keys()).map((r) => r.url + " [" + r.method + "/" + r.mode + "]");
      }
      return out;
    });
    console.log("CACHE DUMP:", JSON.stringify(dump));
    throw new Error("todo page was not cached by the service worker (PAGE_CACHE)");
  }

  try {
    // 2) offline => pill greys out (reproduces the reported bug when broken).
    await context.setOffline(true);
    await page.waitForFunction(
      () => document.querySelector(".online-pill")?.classList.contains("is-offline"),
      null,
      { timeout: 8000 },
    );

    // 3) offline navigation to a visited page is served from the SW cache.
    await page.goto(BASE + "/todo", { waitUntil: "load", timeout: 20000 });
    await page.getByPlaceholder("Add a new todo...").waitFor({ state: "visible", timeout: 15000 });
    console.log("  ✓ offline navigation served the cached todo page");

    // 4) logout form must wire the clear-pages purge message.
    const logoutForm = page.locator('form[action="/logout"]');
    const onSubmit = await logoutForm.getAttribute("data-on:submit");
    if (!onSubmit || !onSubmit.includes("clear-pages")) {
      throw new Error("logout form does not post clear-pages to the service worker");
    }
  } finally {
    await context.setOffline(false);
  }

  // 5) logout (online) => SW purges the cached page.
  console.log("→ Logging out to verify clear-pages purge…");
  await page.locator('form[action="/logout"] button[type="submit"]').click();
  await page.waitForFunction(() => location.pathname === "/login", null, { timeout: 15000 });
  await page.waitForFunction(
    (u) => caches.match(u).then((r) => !r),
    BASE + "/todo",
    { timeout: 8000 },
  );
  console.log("  ✓ logout purged the cached todo page (clear-pages)");
}

try {
  if (providedBin) {
    console.log(`→ Using prebuilt binary ${bin}…`);
  } else {
    console.log("→ Building binary (./cmd/web)…");
    // 240s: the unified binary embeds PocketBase+NATS+DagNats+Loro etc., and
    // `go build ./cmd/web` can exceed 180s on a cold cache, tripping a false
    // failure right after a clean ci-local pass. Prefer using SMOKE_BIN to
    // reuse the binary ci-local already built.
    execSync(`go build -o ${JSON.stringify(bin)} ./cmd/web`, {
      stdio: "inherit",
      timeout: 240000,
    });
  }

  console.log(`→ Starting server on ${BASE} (data dir ${pbDir})…`);
  server = spawn(bin, ["serve", "--http", `127.0.0.1:${PORT}`, "--dir", pbDir], {
    stdio: "ignore",
    env: runtimeEnv,
  });
  server.on("exit", (code) => {
    if (code && code !== 0) console.error(`server exited with code ${code}`);
  });

  await waitForHealth();

  console.log("→ Creating superuser + test user…");
  const upsert = spawnSync(
    bin,
    ["superuser", "upsert", "--dir", pbDir, SU_EMAIL, SU_PASS],
    { encoding: "utf8", env: runtimeEnv },
  );
  if (upsert.status !== 0) {
    throw new Error(`superuser upsert failed: ${upsert.stderr || upsert.stdout}`);
  }
  const su = await api("POST", "/api/collections/_superusers/auth-with-password", {
    body: { identity: SU_EMAIL, password: SU_PASS },
  });
  if (!su.json?.token) throw new Error("superuser auth failed: " + JSON.stringify(su.json));
  const suToken = su.json.token;

  // Create a regular app user (retry briefly in case seeding is still in flight).
  let created = false;
  for (let i = 0; i < 10 && !created; i++) {
    const r = await api("POST", "/api/collections/users/records", {
      token: suToken,
      body: { email: USER_EMAIL, password: USER_PASS, passwordConfirm: USER_PASS },
    });
    if (r.status === 200 || r.status === 400) created = true; // 400 = already exists
    else await new Promise((res) => setTimeout(res, 500));
  }
  const auth = await api("POST", "/api/collections/users/auth-with-password", {
    body: { identity: USER_EMAIL, password: USER_PASS },
  });
  if (!auth.json?.token) throw new Error("user auth failed: " + JSON.stringify(auth.json));
  const userToken = auth.json.token;

  console.log(`→ Launching headless Chromium; testing routes: ${ROUTES.join(", ")}`);
  browser = await chromium.launch({ headless: true, args: ["--no-sandbox"] });
  const context = await browser.newContext();
  await context.addCookies([
    { name: "gogogo_auth", value: userToken, url: BASE + "/" },
  ]);

  const pageErrors = [];
  const consoleErrors = [];
  const page = await context.newPage();
  page.on("pageerror", (err) =>
    pageErrors.push({ msg: String(err), stack: err.stack || "", url: page.url() }),
  );
  page.on("console", (msg) => {
    if (msg.type() === "error") consoleErrors.push(msg.text());
  });

  for (const route of ROUTES) {
    pageErrors.length = 0;
    consoleErrors.length = 0;
    try {
      await page.goto(BASE + route, { waitUntil: "load", timeout: 20000 });
    } catch (e) {
      console.error(`  ! navigation to ${route} failed: ${e.message}`);
    }
    if (route === "/todo") {
      await page.content().then((c) => writeFileSync("/tmp/served_todos.html", c));
    }
    // Give inline scripts + SSE a moment to execute.
    await page.waitForTimeout(1000);
    if (pageErrors.length > 0) {
      fail(`uncaught JS error on ${route}: ${pageErrors.map((e) => e.msg).join(" | ")}`);
      for (const e of pageErrors) {
        console.error(`      at ${e.url}`);
        if (e.stack) console.error(e.stack.split("\n").slice(0, 4).join("\n"));
      }
    }
    const tag = pageErrors.length ? "❌" : "✓";
    console.log(`  ${tag} ${route} (console errors: ${consoleErrors.length})`);
    for (const ce of consoleErrors) console.log(`      console.error: ${ce}`);
  }

  pageErrors.length = 0;
  consoleErrors.length = 0;
  // CAL-34: sweep every UI skin through the offline-queue contract.
  // Each skin has its own `.templ`, and a typo in the offline-reset
  // listener (data-on:gogogo:queued__window vs. gogogo__queued) used
  // to slip through because the harness only covered daisyui.
  const offlineSkins = ["daisyui", "basecoat", "morpheus"];
  for (const skin of offlineSkins) {
    pageErrors.length = 0;
    await verifySingleOnlineTodoSubmit(page, skin);
    await verifyOfflineTodoQueue(page, context, skin);
    if (pageErrors.length > 0) {
      fail(`uncaught JS error during offline queue test (skin=${skin}): ${pageErrors.map((e) => e.msg).join(" | ")}`);
    }
  }
  await verifyOfflineUx(page, context);
  if (pageErrors.length > 0) {
    fail(`uncaught JS error during offline queue test: ${pageErrors.map((e) => e.msg).join(" | ")}`);
  }

  await context.close();
} catch (e) {
  fail(e.stack || String(e));
} finally {
  if (browser) await browser.close().catch(() => {});
  if (server) server.kill("SIGKILL");
  rmSync(tmp, { recursive: true, force: true });
}

if (process.exitCode === 1) {
  console.error("\n❌ Browser smoke test FAILED");
} else {
  console.log("\n✅ Browser smoke test passed (no uncaught client JS errors)");
}
