package model

import (
	"encoding/json"
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

// EncodeHADescription returns only user-facing fields. Native iCloud metadata
// is deliberately excluded from Home Assistant todo descriptions.
func EncodeHADescription(item *Item) string {
	return EncodePriorityPrefix(item.Priority, strings.TrimSpace(item.Description))
}

// DecodeHADescription extracts priority and ReminderRelay metadata from a Home
// Assistant todo description. Malformed or manually removed metadata is
// treated as absent; the original description remains usable.
func DecodeHADescription(description string) (Priority, string, string, []string, *Assignment, []eventkit.RecurrenceRule, bool) {
	clean := strings.TrimSpace(description)
	var meta descriptionMetadata
	legacyMetadata := false

	start := strings.LastIndex(clean, metadataStart)
	if start >= 0 {
		rest := clean[start+len(metadataStart):]
		end := strings.Index(rest, metadataEnd)
		if end >= 0 {
			payload := strings.TrimSpace(rest[:end])
			if json.Unmarshal([]byte(payload), &meta) == nil && meta.Version == 1 {
				clean = strings.TrimSpace(clean[:start] + rest[end+len(metadataEnd):])
				legacyMetadata = true
			}
		}
	}

	priority, notes := DecodePriorityPrefix(clean)
	return priority, strings.TrimSpace(notes), meta.CanonicalUID, meta.Tags, meta.Assignment, meta.Recurrence, legacyMetadata
}
