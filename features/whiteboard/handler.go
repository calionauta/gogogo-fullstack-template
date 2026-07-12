// Package whiteboard implements a minimal collaborative whiteboard:
// an HTML5 canvas rendered with rough.js (hand-drawn style) and backed
// by a Loro CRDT (github.com/aholstenson/loro-go) for conflict-free
// merging of shapes across clients.
//
// Transport is the SAME SSE hub the todo feature uses — no JetStream, no
// extra infrastructure. A client POSTs a shape op (add/clear); the server
// merges it into the shared Loro Doc, persists the resolved snapshot to
// the PocketBase "whiteboards" collection, and broadcasts the resolved
// shapes to every other connected client (exclude-origin). Presence
// cursors use the same hub. See internal/collab (sync_web.go,
// presence_web.go). The browser needs no JS CRDT library — the server owns
// the Loro doc and ships plain JSON shapes.
package whiteboard

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/router"

	"github.com/calionauta/gogogo-fullstack-template/features/auth"
	"github.com/calionauta/gogogo-fullstack-template/internal/collab"
	"github.com/calionauta/gogogo-fullstack-template/internal/queue"
)

const (
	whiteboardListLimit = 50
	sseChanBuf          = 64
)

// Handler serves the whiteboard routes. It holds the shared WebSyncWorker
// (CRDT convergence + persistence) and the SSE hub for fan-out.
type Handler struct {
	app    core.App
	hub    *queue.SSEHub
	worker *collab.WebSyncWorker

	// peers tracks, per doc, the set of clientIDs currently connected to
	// that doc's SSE stream. It is the authoritative source for the
	// "X online" count so the number is consistent across every tab
	// (each tab computes count = peers + self from the same events).
	peersMu sync.Mutex
	peers   map[string]map[string]struct{}
}

// New builds a whiteboard handler. persister is the PocketBase whiteboards
// collection (or an in-memory fake in tests).
func New(app core.App, hub *queue.SSEHub, persister collab.Persister) *Handler {
	return &Handler{
		app:    app,
		hub:    hub,
		worker: collab.NewWebSyncWorker(hub, persister),
		peers:  make(map[string]map[string]struct{}),
	}
}

// RegisterRoutes wires the whiteboard HTTP + SSE routes on the router.
func (h *Handler) RegisterRoutes(se *core.ServeEvent) {
	h.RegisterRoutesOn(se.Router)
}

// RegisterRoutesOn wires the same routes on a raw router (used by tests
// via httptest.NewServer, and by router.Init in production).
func (h *Handler) RegisterRoutesOn(r *router.Router[*core.RequestEvent]) {
	r.GET("/whiteboard", h.handleIndex)
	r.GET("/whiteboard/new", h.handleNew)
	r.GET("/whiteboard/{docID}", h.handleBoard)
	r.GET("/api/whiteboard/{docID}/stream", h.handleStream)
	r.POST("/api/whiteboard/{docID}/update", h.handleUpdate)
	r.POST("/api/whiteboard/{docID}/presence", h.handlePresence)
	r.GET("/api/whiteboard/{docID}/snapshot", h.handleSnapshot)
}

// handleIndex lists existing whiteboards (from the PocketBase collection)
// and links to create a new one.
func (h *Handler) handleIndex(c *core.RequestEvent) error {
	if err := auth.RequireAuthOrRedirect(c); err != nil {
		return err
	}
	email := ""
	if c.Auth != nil {
		email = c.Auth.Email()
	}
	records, err := h.app.FindRecordsByFilter("whiteboards", "", "-updated", whiteboardListLimit, 0)
	if err != nil {
		records = nil
	}
	boards := make([]BoardMeta, 0, len(records))
	for _, r := range records {
		boards = append(boards, BoardMeta{
			DocID:  r.GetString("doc_id"),
			DocVer: r.GetInt("version"),
		})
	}
	if err := renderBoardList(c, email, boards); err != nil {
		return err
	}
	return nil
}

// handleNew creates a new whiteboard doc id and redirects to its board.
// Uses a real 302 redirect (not HX-Redirect) so a plain <a href>
// navigation from the index page works without HTMX.
func (h *Handler) handleNew(c *core.RequestEvent) error {
	if err := auth.RequireAuthOrRedirect(c); err != nil {
		return err
	}
	docID := uuid.NewString()
	return c.Redirect(http.StatusFound, "/whiteboard/"+docID)
}

// handleBoard renders the interactive canvas for one doc. It rehydrates
// the shared Doc from the persister so a freshly opened board starts from
// the latest saved state, then opens the SSE stream client-side.
func (h *Handler) handleBoard(c *core.RequestEvent) error {
	if err := auth.RequireAuthOrRedirect(c); err != nil {
		return err
	}
	docID := c.Request.PathValue("docID")
	if docID == "" {
		return c.String(http.StatusBadRequest, "missing doc id")
	}
	email := ""
	if c.Auth != nil {
		email = c.Auth.Email()
	}
	// Rehydrate the in-memory CRDT from the persisted snapshot so live
	// clients converge onto saved state before receiving new updates.
	if snap, ok := h.worker.LoadSnapshot(docID); ok {
		slog.Info("whiteboard: rehydrated doc", "doc", docID, "bytes", len(snap))
	}
	if err := renderBoard(c, email, docID); err != nil {
		return err
	}
	return nil
}

// handleStream opens an SSE connection for one doc. The client receives
// both shape updates and presence events on this single stream. The auth
// cookie is loaded explicitly (the /api prefix is skipped by the global
// middleware) so the stream is scoped to the logged-in user.
func (h *Handler) handleStream(c *core.RequestEvent) error {
	if err := auth.LoadAppAuth(c); err != nil {
		slog.Warn("whiteboard: stream auth load", "error", err)
	}
	docID := c.Request.PathValue("docID")
	clientID := c.Request.URL.Query().Get("clientID")
	if clientID == "" {
		clientID = uuid.NewString()
	}

	flusher, ok := c.Response.(http.Flusher)
	if !ok {
		return c.String(http.StatusInternalServerError, "streaming unsupported")
	}
	c.Response.Header().Set("Content-Type", "text/event-stream")
	c.Response.Header().Set("Cache-Control", "no-cache")
	c.Response.Header().Set("Connection", "keep-alive")
	c.Response.WriteHeader(http.StatusOK)
	fmt.Fprintf(c.Response, ": connected %s\n\n", docID)
	flusher.Flush()

	// Register the client BEFORE announcing presence so that the
	// join broadcast and the per-peer snapshot below are both computed
	// against a hub that already knows about this client. (Previously the
	// join was broadcast before Register, which meant a client that opened
	// the board after others never learned those peers existed, leaving
	// the "X online" count wrong.)
	ch := make(chan []byte, sseChanBuf)
	h.hub.Register(clientID, ch)
	defer func() {
		h.hub.Unregister(clientID)
		h.peerLeave(docID, clientID)
	}()

	// Track this peer and compute the current peer set (everyone else
	// already on the doc). We send a join to the OTHERS and a snapshot
	// (the existing peer list) to SELF so every tab converges on the
	// same count regardless of connect order.
	others := h.peerJoin(docID, clientID)

	joinMsg, mErr := json.Marshal(collab.PresenceMsg{Doc: docID, User: clientID, Type: "join"})
	if mErr != nil {
		slog.Warn("whiteboard: marshal join", "error", mErr)
		return fmt.Errorf("marshal join: %w", mErr)
	}
	h.hub.BroadcastExcept(joinMsg, clientID)

	if len(others) > 0 {
		snap, sErr := json.Marshal(collab.PresenceMsg{Doc: docID, User: clientID, Type: "snapshot", Peers: others})
		if sErr != nil {
			slog.Warn("whiteboard: marshal snapshot", "error", sErr)
		} else {
			fmt.Fprintf(c.Response, "data: %s\n\n", snap)
			flusher.Flush()
		}
	}
	// Broadcast the authoritative, full peer set to EVERY connected client
	// (including this one) so all tabs converge on the same "X online"
	// count even after reconnects or a missed leave. Clients render the
	// count directly from this event instead of incrementing counters.
	h.broadcastPeerCount(docID)
	// shapes immediately (in case it opened before any live update).
	if shapes := h.worker.Shapes(docID); len(shapes) > 0 {
		payload, err := json.Marshal(collab.WebShapesEvent{Type: "shapes", Doc: docID, From: "", Shapes: shapes})
		if err != nil {
			slog.Warn("whiteboard: marshal initial shapes", "error", err)
		} else {
			fmt.Fprintf(c.Response, "data: %s\n\n", payload)
			flusher.Flush()
		}
	}

	ctx := c.Request.Context()
	for {
		select {
		case <-ctx.Done():
			return nil
		case msg := <-ch:
			fmt.Fprintf(c.Response, "data: %s\n\n", msg)
			flusher.Flush()
		}
	}
}

// peerJoin registers clientID as connected to docID and returns the list
// of clientIDs that were ALREADY on the doc (the peers the new client
// should learn about). It is called on SSE connect. The returned slice
// (possibly empty) is sent to the new client as a "snapshot" presence
// event so it can seed its peer count without waiting for future joins.
func (h *Handler) peerJoin(docID, clientID string) []string {
	h.peersMu.Lock()
	defer h.peersMu.Unlock()
	set := h.peers[docID]
	if set == nil {
		set = make(map[string]struct{})
		h.peers[docID] = set
	}
	others := make([]string, 0, len(set))
	for id := range set {
		if id != clientID {
			others = append(others, id)
		}
	}
	set[clientID] = struct{}{}
	return others
}

// peerLeave removes clientID from docID's peer set and broadcasts a
// "leave" to every remaining client. Called on SSE disconnect.
func (h *Handler) peerLeave(docID, clientID string) {
	h.peersMu.Lock()
	if set, ok := h.peers[docID]; ok {
		delete(set, clientID)
		if len(set) == 0 {
			delete(h.peers, docID)
		}
	}
	h.peersMu.Unlock()
	leaveMsg, lErr := json.Marshal(collab.PresenceMsg{Doc: docID, User: clientID, Type: "leave"})
	if lErr != nil {
		slog.Warn("whiteboard: marshal leave", "error", lErr)
		return
	}
	h.hub.BroadcastExcept(leaveMsg, clientID)
	// Re-broadcast the authoritative count to the remaining clients so the
	// "X online" number drops consistently everywhere (no stale +1 from a
	// missed leave).
	h.broadcastPeerCount(docID)
}

// peerList returns the current set of clientIDs connected to docID
// (including the caller). Callers must NOT hold peersMu.
func (h *Handler) peerList(docID string) []string {
	h.peersMu.Lock()
	defer h.peersMu.Unlock()
	set := h.peers[docID]
	out := make([]string, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	return out
}

// broadcastPeerCount sends the authoritative, full peer set for docID to
// every connected client (including the originator). Clients render the
// "X online" count directly from this event instead of incrementing
// counters client-side, so every tab agrees on the same number even when
// a leave is missed or a tab reconnects with a fresh client id.
func (h *Handler) broadcastPeerCount(docID string) {
	msg, err := json.Marshal(collab.PresenceMsg{Doc: docID, Type: "count", Peers: h.peerList(docID)})
	if err != nil {
		slog.Warn("whiteboard: marshal peer count", "error", err)
		return
	}
	h.hub.Broadcast(msg)
}

// handleUpdate receives a Loro update from a client, merges it into the
// shared Doc, persists the snapshot, and broadcasts to peers.
func (h *Handler) handleUpdate(c *core.RequestEvent) error {
	if err := auth.LoadAppAuth(c); err != nil {
		slog.Warn("whiteboard: update auth load", "error", err)
	}
	docID := c.Request.PathValue("docID")
	if docID == "" {
		return c.String(http.StatusBadRequest, "missing doc id")
	}
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		return c.String(http.StatusBadRequest, "read body")
	}
	from := c.Request.URL.Query().Get("clientID")
	var op collab.ShapeOp
	if uErr := json.Unmarshal(body, &op); uErr != nil {
		return c.String(http.StatusBadRequest, "decode op: "+uErr.Error())
	}
	shapes, err := h.worker.ApplyOp(docID, from, op)
	if err != nil {
		return c.String(http.StatusBadRequest, "apply op: "+err.Error())
	}
	// Broadcast the resolved shapes to EVERY client on the doc (including
	// the originator). The originator drew optimistically and cleared its
	// in-progress shape on pointerup; receiving the authoritative shapes
	// back keeps it convergent and — critically — means the local tab does
	// NOT lose the shape it just drew (the earlier BroadcastExcept excluded
	// the originator, so the local tab's shape list was never refreshed and
	// the drawing vanished on that tab while persisting on others).
	payload, mErr := json.Marshal(collab.WebShapesEvent{Type: "shapes", Doc: docID, From: from, Shapes: shapes})
	if mErr != nil {
		slog.Warn("whiteboard: marshal shapes", "error", mErr)
	} else {
		h.hub.Broadcast(payload)
	}
	return c.JSON(http.StatusOK, map[string]any{"ok": true, "count": len(shapes)})
}

// handlePresence receives a cursor/presence event and broadcasts it to
// peers on the doc's stream (exclude-origin via the hub).
func (h *Handler) handlePresence(c *core.RequestEvent) error {
	if err := auth.LoadAppAuth(c); err != nil {
		slog.Warn("whiteboard: presence auth load", "error", err)
	}
	docID := c.Request.PathValue("docID")
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		return c.String(http.StatusBadRequest, "read body")
	}
	from := c.Request.URL.Query().Get("clientID")
	// Re-tag the event with the doc id and broadcast to peers.
	var msg collab.PresenceMsg
	if err := json.Unmarshal(body, &msg); err != nil {
		return c.String(http.StatusBadRequest, "decode presence")
	}
	msg.Doc = docID
	msg.TS = time.Now().UnixMilli()
	data, mErr := json.Marshal(msg)
	if mErr != nil {
		return c.String(http.StatusInternalServerError, "marshal presence")
	}
	h.hub.BroadcastExcept(data, from)
	return c.JSON(http.StatusOK, map[string]any{"ok": true})
}

// handleSnapshot returns the current resolved shapes for a doc as JSON.
func (h *Handler) handleSnapshot(c *core.RequestEvent) error {
	if err := auth.LoadAppAuth(c); err != nil {
		slog.Warn("whiteboard: snapshot auth load", "error", err)
	}
	docID := c.Request.PathValue("docID")
	return c.JSON(http.StatusOK, h.worker.Shapes(docID))
}

// renderBoardList writes the index page.
func renderBoardList(c *core.RequestEvent, email string, boards []BoardMeta) error {
	c.Response.Header().Set("Content-Type", "text/html; charset=utf-8")
	return BoardList(email, boards).Render(c.Request.Context(), c.Response)
}

// renderBoard writes the interactive board page.
func renderBoard(c *core.RequestEvent, email, docID string) error {
	c.Response.Header().Set("Content-Type", "text/html; charset=utf-8")
	return Board(email, docID).Render(c.Request.Context(), c.Response)
}
