package sync

import (
	"context"
	"github.com/BRO3886/go-eventkit"
	"github.com/nworb-cire/reminderrelay/internal/model"
	"github.com/nworb-cire/reminderrelay/internal/state"
	"testing"
)

func TestBeaconProjectionEditsAndNativePreservation(t *testing.T) {
	canonical := &model.Item{Title: "Bins", Assignment: &model.Assignment{ID: "stable-a", Name: "Alex"}, Tags: []string{"outside"}, RecurrenceRules: []eventkit.RecurrenceRule{eventkit.Weekly(1, eventkit.Monday)}}
	plain := &model.Item{Title: "Renamed", Description: "plain notes"}
	got := mergeHAProjection(canonical, plain)
	if got.Assignment.ID != "stable-a" || len(got.Tags) != 1 || len(got.RecurrenceRules[0].DaysOfTheWeek) != 1 {
		t.Fatal("Plain edits lost native metadata")
	}
	change := &model.Item{Title: "Renamed", AssignmentEditable: true, Assignment: &model.Assignment{Name: "Jordan"}, TagsEditable: true, FrequencyEditable: true, ProjectedFrequency: "daily", RecurrenceRules: []eventkit.RecurrenceRule{eventkit.Daily(1)}}
	got = mergeHAProjection(canonical, change)
	if got.Assignment.Name != "Jordan" || len(got.Tags) != 0 || got.RecurrenceRules[0].Frequency != eventkit.FrequencyDaily {
		t.Fatalf("Projection edits not applied: %#v", got)
	}
	change.Assignment = nil
	change.ProjectedFrequency = "once"
	change.RecurrenceRules = nil
	got = mergeHAProjection(canonical, change)
	if got.Assignment != nil || len(got.RecurrenceRules) != 0 {
		t.Fatal("Explicit unassignment/one-time edits not applied")
	}
	same := &model.Item{AssignmentEditable: true, Assignment: &model.Assignment{Name: "Alex"}, FrequencyEditable: true, ProjectedFrequency: "weekly", RecurrenceRules: []eventkit.RecurrenceRule{eventkit.Weekly(1)}}
	got = mergeHAProjection(canonical, same)
	if got.Assignment.ID != "stable-a" || len(got.RecurrenceRules[0].DaysOfTheWeek) != 1 {
		t.Fatal("Unchanged labels discarded stable identity or weekday constraints")
	}
}
func TestBeaconSummaryIncludesStableHAUIDAndFrequency(t *testing.T) {
	item := &model.Item{UID: "icloud-1", Title: "Bins", ListName: "Shopping", RecurrenceRules: []eventkit.RecurrenceRule{eventkit.Daily(1)}, Assignment: &model.Assignment{ID: "person-1", Name: "Alex"}}
	store := newMockStore()
	store.seed(&state.Item{RemindersUID: item.UID, HAUID: "ha-1", ListName: item.ListName, Title: item.Title, LastSyncHash: item.ProjectionHash()})
	ha := newMockHA()
	ha.addItems("todo.shopping", *item)
	r := NewReconciler(newMockReminders(item), ha, store, testLogger)
	if err := r.publishListSummary(context.Background(), item.ListName, "todo.shopping"); err != nil {
		t.Fatal(err)
	}
	task := ha.summaries[0].Assignees[0].Tasks[0]
	if task.HAUID != "ha-1" || task.Frequency != "daily" || task.Assignment.ID != "person-1" {
		t.Fatalf("Wrong summary identity: %#v", task)
	}
}
