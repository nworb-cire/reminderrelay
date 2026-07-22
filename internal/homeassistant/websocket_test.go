package homeassistant

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestWebsocketURL(t *testing.T) {
	got, err := websocketURL("https://ha.example.com/base/")
	if err != nil {
		t.Fatal(err)
	}
	if got != "wss://ha.example.com/base/api/websocket" {
		t.Fatalf("websocket URL = %q", got)
	}
}

func TestSubscribeTodoItemsUsesNativeItemStream(t *testing.T) {
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		_ = conn.WriteJSON(map[string]interface{}{"type": "auth_required"})
		var auth map[string]interface{}
		_ = conn.ReadJSON(&auth)
		_ = conn.WriteJSON(map[string]interface{}{"type": "auth_ok"})
		var subscription map[string]interface{}
		_ = conn.ReadJSON(&subscription)
		if subscription["type"] != "todo/item/subscribe" {
			t.Errorf("subscription type = %v", subscription["type"])
		}
		id := int(subscription["id"].(float64))
		_ = conn.WriteJSON(map[string]interface{}{"id": id, "type": "result", "success": true})
		_ = conn.WriteJSON(map[string]interface{}{"id": id, "type": "event", "event": map[string]interface{}{"items": []interface{}{}}})
		<-r.Context().Done()
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	adapter := &Adapter{
		wsURL:  "ws" + strings.TrimPrefix(server.URL, "http"),
		token:  "test-token",
		logger: slog.Default(),
	}
	received := make(chan string, 1)
	done := make(chan error, 1)
	go func() {
		done <- adapter.subscribeTodoItemsOnce(ctx, []string{"todo.chores"}, func(entityID string) {
			received <- entityID
			cancel()
		})
	}()

	select {
	case entityID := <-received:
		if entityID != "todo.chores" {
			t.Fatalf("callback entity = %q", entityID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for todo item event")
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("subscription did not stop after cancellation")
	}
}
