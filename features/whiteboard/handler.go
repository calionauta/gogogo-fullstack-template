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
}

// New builds a whiteboard handler. persister is the PocketBase whiteboards
// collection (or an in-memory fake in tests).
func New(app core.App, hub *queue.SSEHub, persister collab.Persister) *Handler {
	return &Handler{
		app:    app,
		hub:    hub,
		worker: collab.NewWebSyncWorker(hub, persister),
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

	// Announce this client joined so every other peer's "X online" count
	// increments. Without this, a second tab opening the same board never
	// received any event and the count stayed stuck at 1.
	joinMsg, _ := json.Marshal(collab.PresenceMsg{Doc: docID, User: "client-" + clientID, Type: "join"})
	h.hub.BroadcastExcept(joinMsg, clientID)
	defer func() {
		leaveMsg, _ := json.Marshal(collab.PresenceMsg{Doc: docID, User: "client-" + clientID, Type: "leave"})
		h.hub.BroadcastExcept(leaveMsg, clientID)
	}()

	ch := make(chan []byte, sseChanBuf)
	h.hub.Register(clientID, ch)
	defer h.hub.Unregister(clientID)
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
