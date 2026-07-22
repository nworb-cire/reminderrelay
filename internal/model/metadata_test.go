package model

import (
	"strings"
	"testing"

	"github.com/BRO3886/go-eventkit"
)

func TestHADescriptionMetadataRoundTrip(t *testing.T) {
	item := &Item{
		CanonicalUID: "icloud-123",
		Description:  "Bring the blue bin",
		Priority:     PriorityHigh,
		Tags:         []string{"outside", "weekly"},
		Assignment:   &Assignment{ID: "sharee://1", Name: "Madi", Address: "madi@example.com"},
		RecurrenceRules: []eventkit.RecurrenceRule{
			eventkit.Weekly(1, eventkit.Monday),
		},
	}

	description := EncodeHADescription(item)
	priority, notes, canonicalUID, tags, assignment, recurrence := DecodeHADescription(description)
	if priority != PriorityHigh || notes != item.Description {
		t.Fatalf("decoded priority/notes = %v/%q", priority, notes)
	}
	if canonicalUID != "icloud-123" {
		t.Fatalf("decoded canonical UID = %q", canonicalUID)
	}
	if len(tags) != 2 || tags[0] != "outside" || tags[1] != "weekly" {
		t.Fatalf("decoded tags = %#v", tags)
	}
	if assignment == nil || assignment.ID != "sharee://1" || assignment.Name != "Madi" {
		t.Fatalf("decoded assignment = %#v", assignment)
	}
	if len(recurrence) != 1 || recurrence[0].Frequency != eventkit.FrequencyWeekly {
		t.Fatalf("decoded recurrence = %#v", recurrence)
	}
}

func TestDecodeHADescriptionMalformedMetadataPreservesText(t *testing.T) {
	description := "notes\n\n" + metadataStart + "\nnot-json\n" + metadataEnd
	_, notes, canonicalUID, tags, assignment, recurrence := DecodeHADescription(description)
	if !strings.Contains(notes, "not-json") {
		t.Fatalf("malformed block was discarded: %q", notes)
	}
	if canonicalUID != "" || len(tags) != 0 || assignment != nil || len(recurrence) != 0 {
		t.Fatal("malformed block unexpectedly produced metadata")
	}
}
