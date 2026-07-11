// Whiteboard client — minimal collaborative canvas.
//
// Transport:
//   - SSE stream (`/api/whiteboard/<doc>/stream`) carries two event kinds:
//       {type:"shapes", shapes:[...]}  -> re-render the whole shape set
//       {type:"cursor"|"join"|"leave", user,x,y} -> remote presence
//   - Drawing POSTs a shape op to `/api/whiteboard/<doc>/update`; the
//     server merges it into the Loro CRDT, persists, and broadcasts the
//     resolved shapes back to every OTHER client (exclude-origin).
//   - Mouse moves POST cursor presence to `/api/whiteboard/<doc>/presence`.
//
// No JS CRDT dependency: the server owns the Loro doc and ships plain
// JSON shapes. rough.js (loaded from CDN in the page) gives the
// hand-drawn look.

(function () {
  "use strict";

  const docID = window.WB_DOC_ID;
  if (!docID) {
    console.error("WB_DOC_ID missing");
    return;
  }
  const clientID =
    new URLSearchParams(location.search).get("clientID") ||
    "wb-" + Math.random().toString(36).slice(2, 10);
  const user = "user-" + clientID.slice(3, 8);

  const canvas = document.getElementById("wb-canvas");
  const wrap = document.getElementById("canvas-wrap");
  const ctx = canvas.getContext("2d");
  const cursorsEl = document.getElementById("cursors");

  let shapes = []; // authoritative shape list from server
  let tool = "rect";
  let color = "#1f2937";
  let drawing = null; // in-progress shape
  let rc = null;

  function fitCanvas() {
    const r = wrap.getBoundingClientRect();
    const dpr = window.devicePixelRatio || 1;
    canvas.width = Math.max(1, Math.floor(r.width * dpr));
    canvas.height = Math.max(1, Math.floor(r.height * dpr));
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    if (window.rough) rc = rough.canvas(canvas);
    render();
  }

  // ---- SSE stream ----
  const stream = new EventSource(
    "/api/whiteboard/" + encodeURIComponent(docID) + "/stream?clientID=" + encodeURIComponent(clientID)
  );
  stream.onmessage = function (ev) {
    let msg;
    try {
      msg = JSON.parse(ev.data);
    } catch (e) {
      return;
    }
    if (msg.type === "shapes") {
      shapes = msg.shapes || [];
      render();
    } else if (msg.type === "cursor" || msg.type === "join" || msg.type === "leave") {
      handlePresence(msg);
    }
  };
  stream.onerror = function () {
    /* EventSource auto-reconnects; flush any buffered ops when it does. */
    updateNetStatus();
  };
  stream.onopen = function () {
    flushOutbox();
    updateNetStatus();
  };

  // ---- network status indicator ----
  function updateNetStatus() {
    const el = document.getElementById("net-status");
    if (!el) return;
    const online = navigator.onLine;
    el.textContent = online ? "online" : "offline — drawing is buffered";
    el.className = "text-xs " + (online ? "text-success" : "text-warning");
  }
  window.addEventListener("online", function () { updateNetStatus(); flushOutbox(); });
  window.addEventListener("offline", updateNetStatus);
  updateNetStatus();

  // ---- offline-first outbox ----
  // Ops drawn while offline are buffered here and replayed on reconnect
  // (or when the browser reports it is back online). The server merges
  // each op into the shared Loro doc, so replay is convergent and safe.
  const outbox = [];
  let flushing = false;
  function flushOutbox() {
    if (flushing || outbox.length === 0) return;
    if (!navigator.onLine) return;
    flushing = true;
    const pending = outbox.splice(0, outbox.length);
    Promise.all(pending.map(function (op) {
      return fetch(
        "/api/whiteboard/" + encodeURIComponent(docID) + "/update?clientID=" + encodeURIComponent(clientID),
        { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(op) }
      ).catch(function (e) { outbox.push(op); console.warn("op replay failed, requeued", e); });
    })).finally(function () { flushing = false; if (outbox.length) setTimeout(flushOutbox, 300); });
  }
  // ---- POST helpers ----
  function postOp(op) {
    if (!navigator.onLine) { outbox.push(op); return; }
    fetch(
      "/api/whiteboard/" + encodeURIComponent(docID) + "/update?clientID=" + encodeURIComponent(clientID),
      {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(op),
      }
    ).catch(function (e) {
      outbox.push(op);
      console.warn("op post failed, buffered for replay", e);
    });
  }

  function postPresence(x, y) {
    fetch(
      "/api/whiteboard/" + encodeURIComponent(docID) + "/presence?clientID=" + encodeURIComponent(clientID),
      {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ type: "cursor", doc: docID, user: user, x: x, y: y, ts: Date.now() }),
      }
    ).catch(function () {});
  }

  // ---- drawing ----
  function localPos(e) {
    const r = canvas.getBoundingClientRect();
    return { x: e.clientX - r.left, y: e.clientY - r.top };
  }

  canvas.addEventListener("pointerdown", function (e) {
    const p = localPos(e);
    canvas.setPointerCapture(e.pointerId);
    if (tool === "pen") {
      drawing = { id: "s-" + Math.random().toString(36).slice(2, 9), type: "pen", points: [p.x, p.y], color: color };
    } else {
      drawing = { id: "s-" + Math.random().toString(36).slice(2, 9), type: tool, x: p.x, y: p.y, w: 0, h: 0, color: color };
    }
  });

  canvas.addEventListener("pointermove", function (e) {
    const p = localPos(e);
    const r = canvas.getBoundingClientRect();
    postPresence((p.x / r.width).toFixed(4), (p.y / r.height).toFixed(4));
    if (!drawing) return;
    if (tool === "pen") {
      drawing.points.push(p.x, p.y);
    } else {
      drawing.w = p.x - drawing.x;
      drawing.h = p.y - drawing.y;
    }
    render();
  });

  canvas.addEventListener("pointerup", function (e) {
    if (!drawing) return;
    // ignore degenerate shapes
    const tiny =
      tool === "pen"
        ? drawing.points.length < 4
        : Math.abs(drawing.w) < 4 && Math.abs(drawing.h) < 4;
    const done = drawing;
    drawing = null;
    if (tiny) {
      render();
      return;
    }
    postOp({ op: "add", shape: done });
  });

  // toolbar (Datastar sets window signals; mirror here for plain JS)
  document.querySelectorAll("[data-tool]").forEach(function (btn) {
    btn.addEventListener("click", function () {
      tool = btn.getAttribute("data-tool");
      document.querySelectorAll("[data-tool]").forEach(function (b) {
        b.classList.toggle("btn-primary", b === btn);
      });
    });
  });
  const colorInput = document.querySelector('input[data-bind="color"]');
  if (colorInput) {
    colorInput.addEventListener("input", function () {
      color = colorInput.value;
    });
  }

  // ---- rendering ----
  function render() {
    if (!ctx) return;
    const r = wrap.getBoundingClientRect();
    ctx.clearRect(0, 0, r.width, r.height);
    const all = drawing ? shapes.concat([drawing]) : shapes;
    for (const s of all) drawShape(s);
  }

  function drawShape(s) {
    if (!rc) {
      // fallback: plain stroke
      ctx.strokeStyle = s.color || "#1f2937";
      ctx.lineWidth = 2;
      ctx.strokeRect(s.x, s.y, s.w, s.h);
      return;
    }
    const opts = { stroke: s.color || "#1f2937", roughness: 1.4, seed: hashSeed(s.id) };
    if (s.type === "rect") {
      rc.rectangle(s.x, s.y, s.w, s.h, opts);
    } else if (s.type === "ellipse") {
      rc.ellipse(s.x + s.w / 2, s.y + s.h / 2, Math.abs(s.w), Math.abs(s.h), opts);
    } else if (s.type === "line") {
      rc.line(s.x, s.y, s.x + s.w, s.y + s.h, opts);
    } else if (s.type === "pen" && s.points && s.points.length >= 4) {
      const pts = [];
      for (let i = 0; i < s.points.length; i += 2) pts.push({ x: s.points[i], y: s.points[i + 1] });
      rc.curve(pts, opts);
    }
  }

  function hashSeed(id) {
    let h = 0;
    for (let i = 0; i < id.length; i++) h = (h * 31 + id.charCodeAt(i)) | 0;
    return Math.abs(h) % 100000;
  }

  // ---- presence ----
  const peers = {}; // user -> {x,y,ts}
  function updatePeerCount() {
    const el = document.getElementById("peer-count");
    if (el) el.textContent = String(Object.keys(peers).length + 1); // +self
  }
  function handlePresence(msg) {
    if (msg.user === user) return;
    if (msg.type === "leave") {
      delete peers[msg.user];
    } else {
      peers[msg.user] = msg;
    }
    updatePeerCount();
    renderCursors();
  }
  function renderCursors() {
    const r = wrap.getBoundingClientRect();
    cursorsEl.innerHTML = "";
    Object.keys(peers).forEach(function (u) {
      const p = peers[u];
      const el = document.createElement("div");
      el.style.position = "absolute";
      el.style.left = (p.x * r.width) + "px";
      el.style.top = (p.y * r.height) + "px";
      el.style.transform = "translate(-2px,-2px)";
      el.style.pointerEvents = "none";
      el.innerHTML =
        '<svg width="16" height="16" viewBox="0 0 16 16"><path d="M0 0 L0 12 L4 9 L7 14 L9 13 L6 8 L11 8 Z" fill="#ef4444"/></svg>' +
        '<span class="badge badge-sm ml-1" style="background:#ef4444;color:#fff">' +
        escapeHtml(u) +
        "</span>";
      cursorsEl.appendChild(el);
    });
  }
  function escapeHtml(s) {
    return String(s).replace(/[&<>"']/g, function (c) {
      return { "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c];
    });
  }

  window.addEventListener("resize", fitCanvas);
  fitCanvas();
  setInterval(renderCursors, 4000); // prune stale handled server-side too
})();
