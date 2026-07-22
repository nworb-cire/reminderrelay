package model

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/BRO3886/go-eventkit"
	"gopkg.in/yaml.v3"
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

type yamlDescription struct {
	Assignee string   `yaml:"Assignee,omitempty"`
	Tags     []string `yaml:"Tags,omitempty"`
	Priority string   `yaml:"Priority,omitempty"`
	Notes    string   `yaml:"Notes,omitempty"`
}

// EncodeHADescription returns a compact, human-readable YAML projection. It
// deliberately excludes internal identifiers and recurrence implementation
// details while exposing metadata useful in Home Assistant.
func EncodeHADescription(item *Item) string {
	projection := yamlDescription{
		Assignee: item.AssigneeLabel(),
		Tags:     append([]string(nil), item.Tags...),
		Notes:    strings.TrimSpace(item.Description),
	}
	if item.Priority != PriorityNone {
		projection.Priority = item.Priority.String()
	}
	if projection.Assignee == "" && len(projection.Tags) == 0 && projection.Priority == "" && projection.Notes == "" {
		return ""
	}
	var output bytes.Buffer
	encoder := yaml.NewEncoder(&output)
	encoder.SetIndent(2)
	if err := encoder.Encode(projection); err != nil {
		return strings.TrimSpace(item.Description)
	}
	return strings.TrimSpace(output.String())
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

	if !legacyMetadata {
		var projection yamlDescription
		if yaml.Unmarshal([]byte(clean), &projection) == nil &&
			(projection.Assignee != "" || len(projection.Tags) > 0 || projection.Priority != "" || projection.Notes != "") {
			priority := priorityFromLabel(projection.Priority)
			var assignment *Assignment
			if projection.Assignee != "" {
				assignment = &Assignment{Name: projection.Assignee}
			}
			return priority, strings.TrimSpace(projection.Notes), "", projection.Tags, assignment, nil, false
		}
	}

	priority, notes := DecodePriorityPrefix(clean)
	return priority, strings.TrimSpace(notes), meta.CanonicalUID, meta.Tags, meta.Assignment, meta.Recurrence, legacyMetadata
}

func priorityFromLabel(label string) Priority {
	switch {
	case strings.EqualFold(label, PriorityHigh.String()):
		return PriorityHigh
	case strings.EqualFold(label, PriorityMedium.String()):
		return PriorityMedium
	case strings.EqualFold(label, PriorityLow.String()):
		return PriorityLow
	default:
		return PriorityNone
	}
}
