// SCOPE:layer=feature,removal=feature — Todo SSE stream: toast + retry/AI-Suggest dispatch helpers
package handlers

import (
	"encoding/json"
	"fmt"

	sdk "github.com/starfederation/datastar-go/datastar"

	ic "github.com/calionauta/gogogo-fullstack-template/internal/components"
	dshelpers "github.com/calionauta/gogogo-fullstack-template/internal/datastar"
)

const (
	retryStatusSuccess = "success"
	retryStatusAttempt = "attempt"
)

func (h *TodoHandler) streamToast(sse *sdk.ServerSentEventGenerator, payload []byte) error {
	var p struct {
		ToastType string `json:"toastType"`
		Message   string `json:"message"`
	}
	if err := json.Unmarshal(payload, &p); err != nil {
		return fmt.Errorf("decode toast payload: %w", err)
	}
	if p.ToastType == "" {
		p.ToastType = "info"
	}
	return emitToast(sse, p.Message, p.ToastType)
}

// retryEvent is the parsed payload of a retry / AI-Suggest broadcast.
type retryEvent struct {
	Operation string `json:"operation"`
	Attempt   int    `json:"attempt"`
	Status    string `json:"status"`
	Error     string `json:"error"`
	Raw       []byte `json:"-"`
}

// retrySignalFields builds the Datastar signal map for a retry / AI-Suggest
// event. AI Suggest jobs drive a dedicated stepper (aiStep/aiPending); the
// Queue + Retry demo drives demoStep — running one never lights the other.
func retrySignalFields(p retryEvent) map[string]any {
	fields := map[string]any{
		"lastRetry":          string(p.Raw),
		"lastRetryOperation": p.Operation,
		"lastRetryStatus":    p.Status,
		"lastRetryAttempt":   p.Attempt,
	}
	isSuggest := p.Operation == signalJobSuggest || p.Operation == signalJobSuggestSimulated
	if isSuggest {
		if p.Status == retryStatusSuccess {
			fields["aiStep"] = 3
			fields["aiPending"] = false
		} else {
			fields["aiStep"] = 2
			fields["aiPending"] = true
		}
		return fields
	}
	fields["demoStep"] = 2
	if p.Status == retryStatusSuccess {
		fields["demoStep"] = 3
	}
	return fields
}

// retryToastMessage returns the user-facing toast text and kind for a
// retry / AI-Suggest event.
func retryToastMessage(p retryEvent) (msg, kind string) {
	verb := p.Operation
	if verb == signalJobSuggestSimulated {
		verb = "suggest (simulated)"
	}
	switch p.Status {
	case retryStatusSuccess:
		return fmt.Sprintf("%s: completed", verb), retryStatusSuccess
	default:
		msg = fmt.Sprintf("%s: attempt %d failed", verb, p.Attempt)
		if p.Error != "" {
			msg += " (" + p.Error + ")"
		}
		msg += ", retrying…"
		return msg, "warning"
	}
}

func (h *TodoHandler) streamRetry(sse *sdk.ServerSentEventGenerator, payload []byte) error {
	var p retryEvent
	if err := json.Unmarshal(payload, &p); err != nil {
		return fmt.Errorf("decode retry payload: %w", err)
	}
	p.Raw = payload
	isSuggest := p.Operation == signalJobSuggest || p.Operation == signalJobSuggestSimulated
	if err := dshelpers.MergeSignals(sse, retrySignalFields(p)); err != nil {
		return err
	}
	if p.Status == retryStatusSuccess && !isSuggest {
		if err := h.applyTechStep(sse, signalJobRetryDemo, true, ""); err != nil {
			return err
		}
	}
	msg, kind := retryToastMessage(p)
	return emitToast(sse, msg, kind)
}

// emitToast renders a toast component and appends it to the
// toast-container. The toast's open state, dismiss timer, and progress
// bar are all driven by Datastar attributes on the rendered template.
func emitToast(sse *sdk.ServerSentEventGenerator, message, toastType string) error {
	return dshelpers.RenderAndPatch(
		sse,
		ic.Toast(message, toastType, ic.NewToastID()),
		sdk.WithSelectorID("toast-container"),
		sdk.WithModeAppend(),
	)
}
