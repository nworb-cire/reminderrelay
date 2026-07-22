package model

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/BRO3886/go-eventkit"
)

func TestHADescriptionOmitsNativeMetadata(t *testing.T) {
	item := &Item{
		CanonicalUID: "icloud-123",
		Description:  "Bring the blue bin",
		Priority:     PriorityHigh,
		Tags:         []string{"outside", "weekly"},
		Assignment:   &Assignment{ID: "sharee://1", Name: "Alex", Address: "alex@example.com"},
		RecurrenceRules: []eventkit.RecurrenceRule{
			eventkit.Weekly(1, eventkit.Monday),
		},
	}

	description := EncodeHADescription(item)
	if description != "[High] Bring the blue bin" {
		t.Fatalf("encoded description = %q", description)
	}
	priority, notes, canonicalUID, tags, assignment, recurrence, legacy := DecodeHADescription(description)
	if legacy {
		t.Fatal("new descriptions must not contain legacy metadata")
	}
	if priority != PriorityHigh || notes != item.Description {
		t.Fatalf("decoded priority/notes = %v/%q", priority, notes)
	}
	if canonicalUID != "" || len(tags) != 0 || assignment != nil || len(recurrence) != 0 {
		t.Fatal("new HA descriptions must not expose native iCloud metadata")
	}
}

func TestDecodeHADescriptionReadsLegacyMetadataForMigration(t *testing.T) {
	payload, err := json.Marshal(descriptionMetadata{
		Version:      1,
		CanonicalUID: "icloud-123",
		Tags:         []string{"outside"},
		Assignment:   &Assignment{Name: "Alex"},
		Recurrence:   []eventkit.RecurrenceRule{eventkit.Weekly(1, eventkit.Monday)},
	})
	if err != nil {
		t.Fatal(err)
	}
	description := "notes\n\n" + metadataStart + "\n" + string(payload) + "\n" + metadataEnd
	_, notes, canonicalUID, tags, assignment, recurrence, legacy := DecodeHADescription(description)
	if !legacy || notes != "notes" || canonicalUID != "icloud-123" {
		t.Fatalf("legacy decode = legacy:%t notes:%q uid:%q", legacy, notes, canonicalUID)
	}
	if len(tags) != 1 || tags[0] != "outside" || assignment == nil || assignment.Name != "Alex" || len(recurrence) != 1 {
		t.Fatalf("legacy metadata was not recovered: %#v %#v %#v", tags, assignment, recurrence)
	}
}

func TestDecodeHADescriptionMalformedMetadataPreservesText(t *testing.T) {
	description := "notes\n\n" + metadataStart + "\nnot-json\n" + metadataEnd
	_, notes, canonicalUID, tags, assignment, recurrence, legacy := DecodeHADescription(description)
	if legacy {
		t.Fatal("malformed metadata must not be treated as valid legacy metadata")
	}
	if !strings.Contains(notes, "not-json") {
		t.Fatalf("malformed block was discarded: %q", notes)
	}
	if canonicalUID != "" || len(tags) != 0 || assignment != nil || len(recurrence) != 0 {
		t.Fatal("malformed block unexpectedly produced metadata")
	}
}
