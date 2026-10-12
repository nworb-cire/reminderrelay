package homeassistant

import (
	"github.com/BRO3886/go-eventkit"
	"github.com/nworb-cire/reminderrelay/internal/model"
	"testing"
)

func TestBeaconTypedProjectionAndExplicitClearing(t *testing.T) {
	got := haItemToModelItem(haTodoItem{UID: "ha-1", Summary: "Bins", Description: "Assignee: Alex\nTags: [outside]\nFrequency: daily\nNotes: Keep this note"})
	if !got.AssignmentEditable || !got.TagsEditable || !got.FrequencyEditable || got.Assignment.Name != "Alex" || got.RecurrenceRules[0].Frequency != eventkit.FrequencyDaily {
		t.Fatalf("Projection not recognized: %#v", got)
	}
	if got.Description != "Keep this note" {
		t.Fatal("Notes lost")
	}
	cleared := haItemToModelItem(haTodoItem{Description: "Assignee: \"\"\nTags: []\nFrequency: once"})
	if !cleared.AssignmentEditable || !cleared.TagsEditable || !cleared.FrequencyEditable || cleared.Assignment != nil || len(cleared.RecurrenceRules) != 0 || cleared.Description != "" {
		t.Fatalf("Clear projection not recognized: %#v", cleared)
	}
	plain := haItemToModelItem(haTodoItem{Description: "Remember to do this"})
	if plain.AssignmentEditable || plain.TagsEditable || plain.FrequencyEditable || plain.Description != "Remember to do this" {
		t.Fatal("Plain notes treated as metadata")
	}
	complex := &model.Item{RecurrenceRules: []eventkit.RecurrenceRule{eventkit.Daily(2)}}
	projected := haItemToModelItem(haTodoItem{Description: model.EncodeHADescription(complex)})
	if projected.FrequencyEditable || projected.ProjectionHash() != complex.ProjectionHash() {
		t.Fatal("Custom recurrence cannot round-trip without an edit")
	}
}
