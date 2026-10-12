package state

import (
	"context"
	"github.com/nworb-cire/reminderrelay/internal/model"
	"path/filepath"
	"testing"
)

func TestCompletionLinksSurviveReopenWithoutChangingItemLinkage(t *testing.T) {
	file := filepath.Join(t.TempDir(), "state.db")
	s, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	item := &Item{RemindersUID: "master", HAUID: "ha-master", ListName: "Chores", Title: "Bins"}
	if err := s.UpsertItem(ctx, item); err != nil {
		t.Fatal(err)
	}
	link := CompletionLink{ListName: "Chores", OriginHAUID: "ha-master", OriginDue: "2026-10-11", CompletedHAUID: "ha-archive", CompletedRemindersUID: "archive", Assignment: &model.Assignment{ID: "person", Name: "Alex"}}
	if err := s.SaveCompletionLink(ctx, link); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveCompletionLink(ctx, link); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(file)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	links, err := s.GetCompletionLinksForList(ctx, "Chores")
	if err != nil || len(links) != 1 || links[0].CompletedHAUID != "ha-archive" || links[0].Assignment.ID != "person" {
		t.Fatalf("Links = %#v / %v", links, err)
	}
	restored, err := s.GetItemByHAUID(ctx, "ha-master")
	if err != nil || restored.RemindersUID != "master" {
		t.Fatal("Original linkage changed")
	}
}
