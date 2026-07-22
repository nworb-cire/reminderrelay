package homeassistant

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/njoerd114/reminderrelay/internal/model"
)

func TestPublishListSummaryFiresHomeAssistantEvent(t *testing.T) {
	var received model.ListSummary
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/events/reminderrelay_list_summary" {
			t.Errorf("event path = %q", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("authorization header = %q", r.Header.Get("Authorization"))
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"message":"Event reminderrelay_list_summary fired."}`))
	}))
	defer server.Close()

	wrapper := &haClientWrapper{baseURL: server.URL, token: "test-token", hc: server.Client()}
	adapter := NewAdapterWithClient(wrapper, slog.Default())
	summary := model.ListSummary{
		ListName:        "Shared Tasks",
		TodoEntityID:    "todo.shared_tasks",
		Remaining:       2,
		ByAssignee:      map[string]int{"Alex Smith": 2},
		TasksByAssignee: map[string][]model.SummaryTask{"Alex Smith": {{UID: "rem-1", Title: "Task"}}},
		UpdatedAt:       time.Now().UTC(),
	}

	if err := adapter.PublishListSummary(context.Background(), summary); err != nil {
		t.Fatal(err)
	}
	if received.ListName != "Shared Tasks" || received.ByAssignee["Alex Smith"] != 2 {
		t.Fatalf("received summary = %#v", received)
	}
}
