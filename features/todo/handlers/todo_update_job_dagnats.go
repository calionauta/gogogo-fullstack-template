// SCOPE:layer=feature,removal=feature — Todo MVC example (reference implementation)
package handlers

import "github.com/calionauta/gogogo-fullstack-template/internal/queue"

// todoUpdateJob builds the queue.Job envelope for SSE-hub todo events.
// Record mutations (create/toggle/delete) now propagate through
// PocketBase realtime (the OnModelAfter*Success hooks broadcast to every
// subscriber of the "todos" topic), so this envelope is only used for
// EPHEMERAL signals still carried by the SSE hub — the durable
// workflow's "workflow-completed" / "workflow-error" notifications sent
// from onboarding.go via broadcaster.PublishTodoUpdate.
//
// Tagged dagnats because its only caller (onboarding.go) is dagnats-only;
// without the tag it would be dead code in the default build (unused lint).
func todoUpdateJob(event, source, id, title string, done bool) []byte {
	ev := mustJSON(map[string]any{
		"event":  event,
		"source": source,
		"id":     id,
		"title":  title,
		"done":   done,
	})
	j := mustJSON(queue.Job{Type: "todo", Payload: ev})
	return j
}
