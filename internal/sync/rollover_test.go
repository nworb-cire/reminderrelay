package sync

import (
	"context"
	"github.com/BRO3886/go-eventkit"
	"github.com/nworb-cire/reminderrelay/internal/model"
	"github.com/nworb-cire/reminderrelay/internal/state"
	"testing"
	"time"
)

type completionMockStore struct {
	*mockStore
	links []state.CompletionLink
}

func (s *completionMockStore) SaveCompletionLink(_ context.Context, link state.CompletionLink) error {
	s.links = append(s.links, link)
	return nil
}
func (s *completionMockStore) GetCompletionLinksForList(_ context.Context, list string) ([]state.CompletionLink, error) {
	var out []state.CompletionLink
	for _, l := range s.links {
		if l.ListName == list {
			out = append(out, l)
		}
	}
	return out, nil
}

type rolloverReminders struct{ *mockReminders }

func (r *rolloverReminders) Update(_ context.Context, uid string, item *model.Item) (*model.Item, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	archive := *item
	archive.UID = "archive"
	archive.Completed = true
	archive.RecurrenceRules = nil
	r.items[archive.UID] = &archive
	master := *item
	master.UID = uid
	master.Completed = false
	due := item.DueDate.AddDate(0, 0, 7)
	master.DueDate = &due
	master.CompletedOccurrenceUID = "archive"
	r.items[uid] = &master
	return &master, nil
}
func TestRolloverKeepsMasterAndPublishesCompletedOccurrenceLink(t *testing.T) {
	due := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)
	master := &model.Item{UID: "master", Title: "Bins", ListName: "Shopping", DueDate: &due, RecurrenceRules: []eventkit.RecurrenceRule{eventkit.Weekly(1)}, Assignment: &model.Assignment{ID: "person", Name: "Alex"}}
	store := &completionMockStore{mockStore: newMockStore()}
	store.seed(&state.Item{RemindersUID: master.UID, HAUID: "ha-master", ListName: master.ListName, Title: master.Title, LastSyncHash: master.ProjectionHash()})
	ha := newMockHA()
	command := *master
	command.UID = "ha-master"
	command.Completed = true
	ha.addItems("todo.shopping", command)
	rem := &rolloverReminders{newMockReminders(master)}
	r := NewReconciler(rem, ha, store, testLogger)
	if _, err := r.Run(context.Background(), testMappings); err != nil {
		t.Fatal(err)
	}
	if len(store.links) != 1 || store.links[0].OriginHAUID != "ha-master" || store.links[0].OriginDue != "2026-10-11" || store.links[0].CompletedHAUID == "ha-master" {
		t.Fatalf("Wrong completed linkage: %#v", store.links)
	}
	summaries := ha.summaries
	last := summaries[len(summaries)-1]
	if len(last.Assignees[0].CompletionLinks) != 1 {
		t.Fatalf("Completion linkage not exposed: %#v", last)
	}
	if _, err := r.Run(context.Background(), testMappings); err != nil {
		t.Fatal(err)
	}
	if len(ha.getItems("todo.shopping")) != 2 {
		t.Fatal("Archived occurrence duplicated on next reconciliation")
	}
}

func TestRolloverLinkRetainsTimedOccurrence(t *testing.T) {
	due := time.Date(2026, 10, 11, 21, 0, 0, 0, time.FixedZone("Denver", -6*3600))
	master := &model.Item{UID: "master", Title: "Timed chore", ListName: "Shopping", DueDate: &due, RecurrenceRules: []eventkit.RecurrenceRule{eventkit.Weekly(1)}}
	store := &completionMockStore{mockStore: newMockStore()}
	store.seed(&state.Item{RemindersUID: master.UID, HAUID: "ha-master", ListName: master.ListName, Title: master.Title, LastSyncHash: master.ProjectionHash()})
	ha := newMockHA()
	command := *master
	command.UID = "ha-master"
	command.Completed = true
	ha.addItems("todo.shopping", command)
	r := NewReconciler(&rolloverReminders{newMockReminders(master)}, ha, store, testLogger)
	if _, err := r.Run(context.Background(), testMappings); err != nil {
		t.Fatal(err)
	}
	if len(store.links) != 1 || store.links[0].OriginDue != "2026-10-12T03:00:00Z" {
		t.Fatalf("Timed occurrence lost precision: %#v", store.links)
	}
}

func TestRolloverLinkRetainsExplicitMidnightTimestamp(t *testing.T) {
	due := time.Date(2026, 10, 11, 0, 0, 0, 0, time.FixedZone("Denver", -6*3600))
	master := &model.Item{UID: "master", Title: "Midnight chore", ListName: "Shopping", DueDate: &due, RecurrenceRules: []eventkit.RecurrenceRule{eventkit.Weekly(1)}}
	store := &completionMockStore{mockStore: newMockStore()}
	store.seed(&state.Item{RemindersUID: master.UID, HAUID: "ha-master", ListName: master.ListName, Title: master.Title, LastSyncHash: master.ProjectionHash()})
	ha := newMockHA()
	command := *master
	command.UID = "ha-master"
	command.Completed = true
	command.HAOccurrenceDue = "2026-10-11T00:00:00-06:00"
	ha.addItems("todo.shopping", command)
	r := NewReconciler(&rolloverReminders{newMockReminders(master)}, ha, store, testLogger)
	if _, err := r.Run(context.Background(), testMappings); err != nil {
		t.Fatal(err)
	}
	if len(store.links) != 1 || store.links[0].OriginDue != "2026-10-11T06:00:00Z" {
		t.Fatalf("Midnight occurrence lost timestamp: %#v", store.links)
	}
}
