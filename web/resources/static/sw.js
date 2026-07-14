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
// Idempotency: PocketBase generates record IDs server-side, so a replayed
// POST creates a NEW record (it is NOT a retry of the original). This is
// acceptable for the demo; production uses would need a client-generated
// UUID (idempotency key) in the request body to detect duplicates.

var CACHE_NAME = "pb-api-v1";
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
            .filter(function (k) { return k !== CACHE_NAME; })
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
  self.clients.matchAll({ includeUncontrolled: true, type: "window" })
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

  // Only intercept API calls on our own origin.
  if (url.origin !== self.location.origin) return;
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

// ---- POST/PUT/PATCH/DELETE: network-first with offline queue ----
async function networkFirstWithQueue(request) {
  try {
    // Try the real request first.
    return await fetch(request);
  } catch (_) {
    // Network unavailable — queue the request and return 202 Accepted.
    try {
      var cloned = request.clone();
      await queueRequest(cloned);
      // Tell the UI a mutation is now queued (offline / will-sync state).
      notifyClients("sync-error");
      // Register a Background Sync event if supported.
      if (self.registration && self.registration.sync) {
        self.registration.sync.register("pb-sync").catch(function () {});
      }
    } catch (_) {
      // Queue failed — the mutation is lost. In practice this only
      // happens if IndexedDB is unavailable (private browsing, disk full).
    }
    return new Response(
      JSON.stringify({ queued: true, message: "Request queued for sync when online." }),
      { status: 202, headers: { "Content-Type": "application/json" } }
    );
  }
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
self.addEventListener("sync", function (e) {
  if (e.tag === "pb-sync") {
    e.waitUntil(replayQueue());
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
  notifyClients("sync-start");

  var remaining = 0;
  for (var i = 0; i < items.length; i++) {
    var item = items[i];
    try {
      var headers = new Headers();
      (item.headers || []).forEach(function (pair) {
        headers.append(pair[0], pair[1]);
      });
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
  notifyClients(remaining === 0 ? "sync-end" : "sync-error");
}
