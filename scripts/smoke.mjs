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

async function verifyOfflineTodoQueue(page, context) {
  const title = `offline-smoke-${Date.now()}`;
  console.log("→ Exercising offline todo add + delete queue…");
  await page.goto(BASE + "/todo", { waitUntil: "load", timeout: 20000 });
  await waitForServiceWorkerControl(page);

  const banner = page.locator("#offline-banner");
  if ((await banner.getAttribute("data-offline-sync")) !== "true") {
    throw new Error("offline banner rendered with offline sync disabled");
  }

  try {
    await context.setOffline(true);
    await page.waitForFunction(() =>
      document.querySelector("#offline-banner-text")?.textContent?.includes("queued"),
    );

    const titleInput = page.getByPlaceholder("Add a new todo...");
    const addButton = page.getByRole("button", { name: "Add" });
    await titleInput.fill(title);
    await addButton.click();
    await page.waitForFunction(() =>
      document.querySelector('input[name="title"]')?.value === "",
    );

    // A second title must re-enable Add. This catches the production bug
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
    const row = page.locator(".todo-item").filter({ hasText: title });
    await row.waitFor({ state: "visible", timeout: 20000 });
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
    await context.setOffline(true);
    await row.getByRole("button", { name: "Delete todo" }).click();
    const dialog = page.getByRole("dialog");
    await dialog.getByRole("button", { name: "Delete", exact: true }).click();
    await dialog.waitFor({ state: "hidden" });
    if ((await pendingMutationCount(page)) !== 1) {
      throw new Error("offline delete was not persisted in IndexedDB");
    }

    await context.setOffline(false);
    await row.waitFor({ state: "detached", timeout: 20000 });
    console.log("  ✓ offline add resets UI, queues, replays; offline delete replays");
  } finally {
    await context.setOffline(false);
  }
}

try {
  if (providedBin) {
    console.log(`→ Using prebuilt binary ${bin}…`);
  } else {
    console.log("→ Building binary (./cmd/web)…");
    execSync(`go build -o ${JSON.stringify(bin)} ./cmd/web`, {
      stdio: "inherit",
      timeout: 180000,
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
  await verifyOfflineTodoQueue(page, context);
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
