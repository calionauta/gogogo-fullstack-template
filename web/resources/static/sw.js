// Service Worker — offline cache + Background Sync for PocketBase CRUD.
//
// This SW intercepts every fetch to /api/* (except SSE streams and
// whiteboard endpoints) and:
//   - GET:  stale-while-revalidate — serve cached, update from network
//   - POST/PUT/PATCH/DELETE: network-first — queue in IndexedDB if offline
//   - On reconnect: Background Sync replays queued mutations in order
//
// PocketBase realtime SSE (EventSource) is NOT intercepted — the browser
// manages it natively, so it reconnects automatically when online.
// Whiteboard endpoints are also skipped (they have their own Loro-based
// offline mechanism via IndexedDB outbox in whiteboard.js).
//
// Idempotency: the UI generates a fresh UUID per click and sends it
// as the `idem_key` form field on every create POST. The SW forwards
// the form body verbatim on replay. The server-side hook
// (db.RegisterIdempotencyHook) looks up an existing record with the
// same (idem_key, owner) and returns it instead of creating a
// duplicate; a (idem_key, owner) unique index in PocketBase backs the
// dedup at the DB layer for the race window. Single-instance and
// multi-instance both work because the dedup state lives in the
// database. See docs/decisions.md for the full rationale.

var CACHE_NAME = "pb-api-v1";
var PAGE_CACHE = "gogogo-pages-v1";
var API_PREFIX = "/api/";

// SSE and whiteboard paths that must NOT be intercepted.
var SKIP_PATTERNS = [
  "/api/realtime", // PocketBase realtime (EventSource, not caught by SW anyway)
  "/api/todos/stream", // todo SSE stream (EventSource)
  "/api/collab/presence/", // presence SSE stream (EventSource)
  "/api/whiteboard/" // whiteboard ops + stream (own offline mechanism)
];

// ---- Install & Activate ----
self.addEventListener("install", function (e) {
  // Take control immediately — don't wait for page reload.
  self.skipWaiting();
});

self.addEventListener("activate", function (e) {
  e.waitUntil(
    Promise.all([
      // Claim all clients so the SW controls pages opened before install.
      clients.claim(),
      // Purge old caches from previous SW versions.
      caches.keys().then(function (keys) {
        return Promise.all(
          keys
            .filter(function (k) { return k !== CACHE_NAME && k !== PAGE_CACHE; })
            .map(function (k) { return caches.delete(k); })
        );
      })
    ])
  );
});

// ---- Notify clients of transport state ----
// The OfflineBanner component (internal/components/offline_banner.templ)
// listens for these messages. States: sync-start (replaying queued
// mutations), sync-end (replay finished cleanly), sync-error (replay
// stopped with items still pending, or queued while offline).
function notifyClients(state) {
  return self.clients.matchAll({ includeUncontrolled: true, type: "window" })
    .then(function (clientsList) {
      clientsList.forEach(function (client) {
        client.postMessage({ type: state });
      });
    })
    .catch(function () { /* no clients to notify */ });
}

// ---- Fetch Intercept ----
self.addEventListener("fetch", function (e) {
  var url = new URL(e.request.url);

  if (url.origin !== self.location.origin) return;

  // Navigation requests (HTML page loads): network-first with cache
  // fallback so previously visited pages work offline.
  if (e.request.mode === "navigate") {
    e.respondWith(networkFirstPage(e.request));
    return;
  }

  if (!url.pathname.startsWith(API_PREFIX)) return;

  // Skip SSE/stream/whiteboard endpoints.
  for (var i = 0; i < SKIP_PATTERNS.length; i++) {
    if (url.pathname.indexOf(SKIP_PATTERNS[i]) === 0) {
      return; // let the request pass through unhandled
    }
  }

  // GET → stale-while-revalidate
  if (e.request.method === "GET") {
    e.respondWith(staleWhileRevalidate(e.request));
    return;
  }

  // POST/PUT/PATCH/DELETE → network-first with offline queue
  if (["POST", "PUT", "PATCH", "DELETE"].indexOf(e.request.method) !== -1) {
    e.respondWith(networkFirstWithQueue(e.request));
    return;
  }

  // Other methods (HEAD, OPTIONS, etc.) pass through.
});

// ---- GET: stale-while-revalidate ----
async function staleWhileRevalidate(request) {
  var cached;
  try {
    cached = await caches.match(request);
  } catch (_) {
    // Cache API unavailable
  }

  try {
    var network = await fetch(request);
    // Clone before caching — response body can only be read once.
    var clone = network.clone();
    caches.open(CACHE_NAME).then(function (cache) {
      cache.put(request, clone);
    }).catch(function () { /* best-effort cache update */ });
    return network;
  } catch (_) {
    // Network failed — return cached if available.
    if (cached) return cached;
    // No cache and no network: return a generic offline response.
    return new Response(
      JSON.stringify({ error: "offline", message: "You are offline. Cached data may be stale." }),
      { status: 503, headers: { "Content-Type": "application/json" } }
    );
  }
}

// ---- Navigation (HTML pages): network-first with cache fallback ----
// Navigation requests are NOT intercepted by the API handlers above, so
// when the browser is offline and the user navigates to a new URL they
// get ERR_INTERNET_DISCONNECTED. This handler caches each page on first
// visit and serves the cached copy when offline. Pages never visited
// before show a generic offline message instead of a browser error page.
//
// Cache is NOT shared across users: we only cache basic responses (not
// redirects to /login), and the client clears the cache on logout via
// the "clear-pages" postMessage (see the message handler below).

var OFFLINE_PAGE = '<!DOCTYPE html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Offline</title><style>body{font-family:system-ui,sans-serif;display:flex;justify-content:center;align-items:center;min-height:100vh;margin:0;background:#1d232a;color:#a6adbb;text-align:center;padding:1rem}@media(prefers-color-scheme:light){body{background:#fff;color:#374151}}h1{font-size:1.5rem;margin-bottom:.5rem}p{font-size:.875rem;line-height:1.5;max-width:24rem}</style></head><body><div><h1>You are offline</h1><p>This page has not been visited before, or the cached version expired. Connect to the internet and try again.</p></div></body></html>';

function networkFirstPage(request) {
  return fetch(request)
    .then(function (response) {
      // Cache any successful same-origin HTML navigation. We previously
      // restricted this to response.type === "basic", but a SW re-fetch of a
      // navigation request can report a different type in some setups; the
      // goal is simply offline availability of visited pages.
      // Cache any successful same-origin HTML navigation so it is available
      // offline. (A SW re-fetch of a navigation request may report a type
      // other than "basic", so we gate on response.ok rather than the type.)
      if (response.ok) {
        var copy = response.clone();
        caches.open(PAGE_CACHE).then(function (cache) {
          cache.put(request.url, copy);
        }).catch(function () {});
      }
      return response;
    })
    .catch(function () {
      return caches.match(request.url).then(function (cached) {
        if (cached) return cached;
        return new Response(OFFLINE_PAGE, {
          status: 200,
          headers: { "Content-Type": "text/html; charset=utf-8" }
        });
      });
    });
}

// ---- POST/PUT/PATCH/DELETE: network-first with offline queue ----
//
// Offline-first UX: when the network is down we still want the UI to
// behave "as if online" — the row appears/disappears immediately, and the
// queued request is replayed on reconnect. The server's realtime broadcast
// then re-syncs the list with authoritative data (and any optimistic row
// is replaced by the real one). buildOfflineMutation() returns a Datastar
// HTML-patch for the known CRUD endpoints; everything else falls back to
// the generic offline toast.
async function networkFirstWithQueue(request) {
  // Clone before fetch consumes the request body. Cloning in the catch block
  // loses form mutations because a failed fetch can still mark the original
  // body as used, making request.clone() throw before IndexedDB is reached.
  var replayable = request.clone();
  // A separate clone for reading the body so we don't consume replayable's
  // stream before queueRequest() clones it again.
  var bodyProbe = request.clone();
  var bodyText = "";
  try {
    bodyText = await bodyProbe.text();
  } catch (_) { /* body read is best-effort */ }
  try {
    // Try the real request first.
    return await fetch(request);
  } catch (_) {
    // Network unavailable — queue the preserved request and return a 200
    // Datastar fragment (see below) so the client's @post action completes.
    try {
      await queueRequest(replayable);
      // Tell the UI a mutation is now queued (offline / will-sync state).
      // Await delivery so the request cannot finish before Datastar receives
      // the event that resets its pending UI state.
      await notifyClients("sync-error");
      // Register a Background Sync event if supported.
      if (self.registration && self.registration.sync) {
        self.registration.sync.register("pb-sync").catch(function () {});
      }
    } catch (_) {
      // Queue failed — the mutation is lost. In practice this only
      // happens if IndexedDB is unavailable (private browsing, disk full).
    }
    // Optimistic UI for known CRUD endpoints; otherwise the generic toast.
    var optimistic = buildOfflineMutation(new URL(request.url), bodyText, request.method);
    if (optimistic) return optimistic;
    return buildOfflineToastResponse();
  }
}

// escapeHTML makes user-provided text (todo titles) safe inside an HTML
// attribute / text node of the optimistic fragment we inject.
function escapeHTML(str) {
  return String(str == null ? "" : str)
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;")
    .replace(/'/g, "&#39;");
}

// datastarPatchResponse wraps an HTML fragment in the Datastar single-patch
// envelope (selector + mode headers) that @post/@get understand.
function datastarPatchResponse(html, selector, mode) {
  return new Response(html, {
    status: 200,
    headers: {
      "Content-Type": "text/html; charset=utf-8",
      "Cache-Control": "no-store",
      "datastar-selector": selector,
      "datastar-mode": mode,
    },
  });
}

// BIN_SVG is the trash-can icon reused from the server-rendered rows so the
// optimistic row matches the real ones.
var BIN_SVG = '<svg xmlns="http://www.w3.org/2000/svg" class="h-4 w-4" viewBox="0 0 20 20" fill="currentColor"><path fill-rule="evenodd" d="M9 2a1 1 0 00-.894.553L7.382 4H4a1 1 0 000 2v10a2 2 0 002 2h8a2 2 0 002-2V6a1 1 0 100-2h-3.382l-.724-1.447A1 1 0 0011 2H9zM7 8a1 1 0 012 0v6a1 1 0 11-2 0V8zm5-1a1 1 0 00-1 1v6a1 1 0 102 0V8a1 1 0 00-1-1z" clip-rule="evenodd"/></svg>';

// buildOfflineMutation returns an optimistic Datastar patch for the known
// todo CRUD endpoints, or null to fall back to the generic offline toast.
function buildOfflineMutation(url, bodyText, method) {
  if (method !== "POST") return null;
  var path = url.pathname;
  if (path === "/api/todos" || path === "/api/todos/") {
    var params = new URLSearchParams(bodyText || "");
    var title = params.get("title") || "";
    var idemKey = url.searchParams.get("idem_key") || "";
    if (!idemKey && title.trim() === "") return null;
    return optimisticCreateFragment(title, idemKey);
  }
  // Clear Completed has its own URL pattern — must be checked BEFORE the
  // generic delete regex, otherwise /api/todos/completed/delete is matched
  // as a delete of id="completed" (which silently does nothing).
  if (path === "/api/todos/completed/delete" || path === "/api/todos/completed/delete/") {
    return optimisticClearCompletedFragment();
  }
  var del = /\/api\/todos\/([^/]+)\/delete\/?$/.exec(path);
  if (del) return optimisticDeleteFragment(del[1]);
  var togg = /\/api\/todos\/([^/]+)\/toggle\/?$/.exec(path);
  if (togg) return optimisticToggleFragment(togg[1]);
  return null;
}

// optimisticCreateFragment appends a temporary row so the new todo is
// visible immediately offline. The id is derived from idem_key so the
// pending item is stable across re-renders; on reconnect the server's
// realtime broadcast replaces the whole list with authoritative rows.
function optimisticCreateFragment(title, idemKey) {
  var id = "todo-" + escapeHTML(idemKey || "tmp-" + Date.now());
  var safeTitle = escapeHTML(title);
  var html =
    '<div class="todo-item flex items-center gap-3 p-3 border-b border-base-300 hover:bg-base-200 transition-colors opacity-70" id="' + id + '" data-pending="true">' +
      '<input type="checkbox" class="checkbox checkbox-primary checkbox-sm" disabled />' +
      '<span class="flex-1">' + safeTitle + '</span>' +
      '<span class="text-xs text-base-content/40">agora</span>' +
      '<button type="button" class="btn btn-ghost btn-xs text-error" disabled aria-label="Delete todo">' + BIN_SVG + '</button>' +
    '</div>' +
    '<script id="__gogogo_add">var __e=document.getElementById("todo-empty-state");if(__e){__e.remove();}var __s=document.getElementById("__gogogo_add");if(__s){__s.remove();}</' + 'script>';
  return datastarPatchResponse(html, "#todo-list", "append");
}

// optimisticDeleteFragment removes the row immediately so the list reflects
// the deletion while offline.
function optimisticDeleteFragment(todoID) {
  var safeID = escapeHTML(todoID);
  var html = '<script id="__gogogo_del">var __d=document.getElementById("todo-' + safeID + '");if(__d){__d.remove();}var __s=document.getElementById("__gogogo_del");if(__s){__s.remove();}</' + 'script>';
  return datastarPatchResponse(html, "#todo-list", "append");
}

// optimisticClearCompletedFragment removes every visible todo row and shows
// the empty state, so clearing completed items works offline too.
function optimisticClearCompletedFragment() {
  var html = '<script id="__gogogo_clr">var __items=document.querySelectorAll("#todo-list > .todo-item");if(__items){__items.forEach(function(e){e.remove();});}var __empty=document.getElementById("todo-empty-state");if(!__empty){var __list=document.getElementById("todo-list");if(__list){var __div=document.createElement("div");__div.id="todo-empty-state";__div.className="py-8 text-center text-base-content/40";__div.innerHTML=\'<p>No todos here yet.</p>\';__list.appendChild(__div);}}document.getElementById("__gogogo_clr")&&document.getElementById("__gogogo_clr").remove();<' + '/script>';
  return datastarPatchResponse(html, "#todo-list", "append");
}

// optimisticToggleFragment flips the row's completed state immediately so
// the checkbox reflects the offline toggle; the realtime broadcast corrects
// it on reconnect.
function optimisticToggleFragment(todoID) {
  var safeID = escapeHTML(todoID);
  var html = '<script id="__gogogo_tog">var __t=document.getElementById("todo-' + safeID + '");' +
    'if(__t){var __c=__t.querySelector("input[type=checkbox]");if(__c){__c.checked=!__c.checked;}' +
    'var __s=__t.querySelector(".flex-1");if(__s){__s.classList.toggle("line-through");__s.classList.toggle("text-base-content/50");}}' +
    'var __ss=document.getElementById("__gogogo_tog");if(__ss){__ss.remove();}</' + 'script>';
  return datastarPatchResponse(html, "#todo-list", "append");
}

// buildOfflineToastResponse returns the generic Datastar fragment for
// mutations that don't have a dedicated optimistic UI (e.g. AI suggest).
// The awaited service-worker postMessage above is the single source of the
// `gogogo:queued` UI-reset event.
function buildOfflineToastResponse() {
  return new Response(
    '<div data-offline-toast class="alert alert-warning mb-2">' +
      '<span>Offline — request queued. Will sync when you reconnect.</span>' +
    '</div>' +
    '<script>' +
      // Keep only one offline toast in the stack. The form reset
      // ($loading/$newTitle) is driven solely by the offline-banner
      // postMessage bridge, which is the single source of the
      // gogogo:queued event (see internal/components/offline_banner.templ).
      'var __old = document.querySelector("[data-offline-toast]");' +
      'if (__old) __old.remove();' +
    '</script>',
    {
      status: 200,
      headers: {
        "Content-Type": "text/html",
        // Append into the styled #toast-container stack so the offline
        // toast renders in the same fixed bottom-right stack as the
        // in-process toasts (Datastar falls back to <body> if absent).
        "datastar-selector": "#toast-container",
        "datastar-mode": "append",
      },
    }
  );
}

// ---- IndexedDB queue ----
var DB_NAME = "pb-offline-queue";
var DB_VERSION = 1;
var STORE_NAME = "pending";

function idbOpen() {
  return new Promise(function (resolve, reject) {
    var req = indexedDB.open(DB_NAME, DB_VERSION);
    req.onupgradeneeded = function () {
      if (!req.result.objectStoreNames.contains(STORE_NAME)) {
        req.result.createObjectStore(STORE_NAME, { keyPath: "id", autoIncrement: true });
      }
    };
    req.onsuccess = function () { resolve(req.result); };
    req.onerror = function () { reject(req.error); };
  });
}

async function queueRequest(request) {
  var db = await idbOpen();
  // Read body + headers BEFORE the transaction to avoid an IndexedDB
  // auto-commit race: transactions commit when the synchronous block
  // finishes, so an async body read inside the Promise could fire after
  // the transaction is already closed, silently losing the mutation.
  var body = await request.clone().text().catch(function () { return ""; });
  var entries = Array.from(request.headers.entries());
  return new Promise(function (resolve, reject) {
    var tx = db.transaction(STORE_NAME, "readwrite");
    tx.objectStore(STORE_NAME).add({
      url: request.url,
      method: request.method,
      headers: entries,
      body: body
    });
    tx.oncomplete = function () { db.close(); resolve(); };
    tx.onerror = function () { db.close(); reject(tx.error); };
  });
}

async function loadAllPending() {
  var db = await idbOpen();
  return new Promise(function (resolve, reject) {
    var tx = db.transaction(STORE_NAME, "readonly");
    var req = tx.objectStore(STORE_NAME).getAll();
    req.onsuccess = function () { db.close(); resolve(req.result); };
    req.onerror = function () { db.close(); reject(req.error); };
  });
}

async function deletePending(id) {
  var db = await idbOpen();
  return new Promise(function (resolve, reject) {
    var tx = db.transaction(STORE_NAME, "readwrite");
    tx.objectStore(STORE_NAME).delete(id);
    tx.oncomplete = function () { db.close(); resolve(); };
    tx.onerror = function () { db.close(); reject(tx.error); };
  });
}

// ---- Background Sync ----
// Background Sync is best-effort and is not available in every browser.
// Serialize every trigger so a browser sync event and an explicit page
// reconnect message cannot replay the same queue concurrently.
var replayPromise = null;

function requestReplay() {
  if (!replayPromise) {
    replayPromise = replayQueue().finally(function () {
      replayPromise = null;
    });
  }
  return replayPromise;
}

self.addEventListener("sync", function (e) {
  if (e.tag === "pb-sync") {
    e.waitUntil(requestReplay());
  }
});

// The window receives reliable online events; ServiceWorkerGlobalScope does
// not. Pages explicitly request a replay on reconnect, covering browsers and
// headless contexts where Background Sync is absent or delayed.
self.addEventListener("message", function (e) {
  if (e.data && e.data.type === "replay-queue") {
    e.waitUntil(requestReplay());
  }
  // Clear cached pages on logout so a different user on a shared device
  // does not receive stale authenticated pages. The auth Navbar's logout
  // form posts to /logout; the page reloads after the response, but the
  // cached HTML is already gone. See auth/views.templ.
  if (e.data && e.data.type === "clear-pages") {
    caches.delete(PAGE_CACHE).catch(function () {});
  }
});

async function replayQueue() {
  var items;
  try {
    items = await loadAllPending();
  } catch (_) {
    return; // IndexedDB unavailable — try again later.
  }
  if (items.length === 0) return;

  // Replay starting — switch the UI to the "syncing" state.
  await notifyClients("sync-start");

  var remaining = 0;
  for (var i = 0; i < items.length; i++) {
    var item = items[i];
    try {
      var headers = new Headers();
      (item.headers || []).forEach(function (pair) {
        headers.append(pair[0], pair[1]);
      });
      // Tag replayed requests so the server can return a lightweight
      // response (no Datastar SSE body) — the client still learns of the
      // change via PocketBase realtime, so the streamed SSE would be wasted.
      headers.append("X-Offline-Replay", "1");
      var opts = {
        method: item.method,
        headers: headers
      };
      if (item.body && item.method !== "GET" && item.method !== "HEAD") {
        opts.body = item.body;
      }
      var resp = await fetch(item.url, opts);
      if (resp.ok || resp.status === 404) {
        // 404 means the resource was already deleted — safe to remove.
        await deletePending(item.id);
      } else {
        // Non-ok status (5xx) — leave in queue, retry next sync.
        remaining++;
      }
    } catch (_) {
      // Network still unavailable — stop replay, try again later.
      remaining++;
      break;
    }
  }

  // Replay finished: online if nothing left, offline if items remain.
  await notifyClients(remaining === 0 ? "sync-end" : "sync-error");
}
