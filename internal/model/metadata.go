package model

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/BRO3886/go-eventkit"
)

const (
	metadataStart = "--- ReminderRelay metadata ---"
	metadataEnd   = "--- End ReminderRelay metadata ---"
)

type descriptionMetadata struct {
	Version      int                       `json:"version"`
	CanonicalUID string                    `json:"icloud_uid,omitempty"`
	Tags         []string                  `json:"tags,omitempty"`
	Assignment   *Assignment               `json:"assignment,omitempty"`
	Recurrence   []eventkit.RecurrenceRule `json:"recurrence,omitempty"`
}

// EncodeHADescription combines the user-authored reminder notes with a
// deterministic metadata block for fields Home Assistant todo items do not
// support natively. The block is intentionally readable and valid JSON so it
// can also be edited from Home Assistant.
func EncodeHADescription(item *Item) string {
	base := EncodePriorityPrefix(item.Priority, strings.TrimSpace(item.Description))
	meta := descriptionMetadata{
		Version:      1,
		CanonicalUID: item.CanonicalUID,
		Tags:         append([]string(nil), item.Tags...),
		Assignment:   item.Assignment,
		Recurrence:   append([]eventkit.RecurrenceRule(nil), item.RecurrenceRules...),
	}
	if meta.CanonicalUID == "" && len(meta.Tags) == 0 && meta.Assignment == nil && len(meta.Recurrence) == 0 {
		return base
	}

	payload, err := json.Marshal(meta)
	if err != nil {
		return base
	}
	block := fmt.Sprintf("%s\n%s\n%s", metadataStart, payload, metadataEnd)
	if base == "" {
		return block
	}
	return base + "\n\n" + block
}

// DecodeHADescription extracts priority and ReminderRelay metadata from a Home
// Assistant todo description. Malformed or manually removed metadata is
// treated as absent; the original description remains usable.
func DecodeHADescription(description string) (Priority, string, string, []string, *Assignment, []eventkit.RecurrenceRule) {
	clean := strings.TrimSpace(description)
	var meta descriptionMetadata

	start := strings.LastIndex(clean, metadataStart)
	if start >= 0 {
		rest := clean[start+len(metadataStart):]
		end := strings.Index(rest, metadataEnd)
		if end >= 0 {
			payload := strings.TrimSpace(rest[:end])
			if json.Unmarshal([]byte(payload), &meta) == nil && meta.Version == 1 {
				clean = strings.TrimSpace(clean[:start] + rest[end+len(metadataEnd):])
			}
		}
	}

	priority, notes := DecodePriorityPrefix(clean)
	return priority, strings.TrimSpace(notes), meta.CanonicalUID, meta.Tags, meta.Assignment, meta.Recurrence
}
