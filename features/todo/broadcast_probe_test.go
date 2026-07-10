package todo_test

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestIntegration_BroadcastAcrossTwoClients(t *testing.T) {
	base, _, _, _, cleanup := testFixture(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	clientA := "bcast-A-" + time.Now().Format(clientIDSuffixFormat)
	clientB := "bcast-B-" + time.Now().Format(clientIDSuffixFormat)

	streamA := openSSEWithCtx(ctx, t, base, clientA)
	defer func() { _ = streamA.Body.Close() }()
	streamB := openSSEWithCtx(ctx, t, base, clientB)
	defer func() { _ = streamB.Body.Close() }()

	time.Sleep(200 * time.Millisecond)

	if _, err := postForm(ctx, base+"/api/todos", url.Values{titleField: {"broadcast me"}}); err != nil {
		t.Fatalf("create: %v", err)
	}

	fullB := pumpSSEUntil(t, streamB, 10*time.Second, func(s string) bool {
		return strings.Contains(s, "broadcast me")
	})
	if !strings.Contains(fullB, "broadcast me") {
		t.Fatalf("client B did NOT receive broadcast todo: %s", tailString(fullB, 800))
	}
	t.Logf("client B received broadcast: OK")
}
