package reminders

import (
	ek "github.com/BRO3886/go-eventkit/reminders"
	"github.com/nworb-cire/reminderrelay/internal/model"
	"testing"
	"time"
)

func TestResolveArchivedCompletionUsesNewIdentityAndRejectsAmbiguity(t *testing.T) {
	due := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)
	item := &model.Item{Title: "Bins", DueDate: &due}
	archived := ek.Reminder{ID: "new-completed", Title: "Bins", Completed: true, DueDate: &due}
	known := map[string]bool{"master": true, "older-identical": true}
	old := archived
	old.ID = "older-identical"
	active := archived
	active.ID = "master"
	active.Completed = false
	uid, err := resolveArchivedCompletion(known, []ek.Reminder{old, active, archived}, item)
	if err != nil || uid != "new-completed" {
		t.Fatalf("Resolution = %q/%v", uid, err)
	}
	duplicate := archived
	duplicate.ID = "another-new-completed"
	if _, err := resolveArchivedCompletion(known, []ek.Reminder{archived, duplicate}, item); err == nil {
		t.Fatal("Ambiguous new identities must not be guessed")
	}
	if _, err := resolveArchivedCompletion(known, []ek.Reminder{old, active}, item); err == nil {
		t.Fatal("Missing archive must not target an existing reminder")
	}
}
