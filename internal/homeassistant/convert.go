package homeassistant

import (
	"time"

	"github.com/njoerd114/reminderrelay/internal/model"
)

// HA todo service constants.
const (
	domainTodo        = "todo"
	serviceGetItems   = "get_items"
	serviceAddItem    = "add_item"
	serviceUpdateItem = "update_item"
	serviceRemoveItem = "remove_item"

	statusNeedsAction = "needs_action"
	statusCompleted   = "completed"

	dateLayout = "2006-01-02"
)

// haTodoItem is the JSON structure for a single item returned by the HA
// todo.get_items service.
type haTodoItem struct {
	UID         string `json:"uid"`
	Summary     string `json:"summary"`
	Status      string `json:"status"` // "needs_action" or "completed"
	Description string `json:"description,omitempty"`
	Due         string `json:"due,omitempty"` // "YYYY-MM-DD" or RFC 3339
}

// haItemsResponse wraps the items array inside the service response for a
// single entity.
type haItemsResponse struct {
	Items []haTodoItem `json:"items"`
}

// haItemToModelItem converts an HA todo item to a [model.Item]. The priority
// prefix (e.g. "[High] ") is stripped from the description and decoded into
// the Priority field.
func haItemToModelItem(h haTodoItem) model.Item {
	priority, description, canonicalUID, tags, assignment, recurrence, legacyMetadata := model.DecodeHADescription(h.Description)

	item := model.Item{
		UID:             h.UID,
		CanonicalUID:    canonicalUID,
		LegacyMetadata:  legacyMetadata,
		Title:           h.Summary,
		Description:     description,
		Priority:        priority,
		Tags:            tags,
		Assignment:      assignment,
		RecurrenceRules: recurrence,
		Completed:       h.Status == statusCompleted,
	}

	if h.Due != "" {
		if t, err := parseDue(h.Due); err == nil {
			item.DueDate = &t
		}
	}

	return item
}

// buildAddItemData returns the service-call payload for todo.add_item.
func buildAddItemData(entityID string, item *model.Item) map[string]interface{} {
	data := map[string]interface{}{
		"entity_id": entityID,
		"item":      item.Title,
	}

	desc := model.EncodeHADescription(item)
	if desc != "" {
		data["description"] = desc
	}

	addDue(data, item.DueDate, false)

	return data
}

// buildUpdateItemData returns the service-call payload for todo.update_item.
// identifier is the stable HA item UID used to identify the item.
func buildUpdateItemData(entityID, identifier string, item *model.Item) map[string]interface{} {
	data := map[string]interface{}{
		"entity_id": entityID,
		"item":      identifier,
	}

	data["rename"] = item.Title

	data["description"] = model.EncodeHADescription(item)

	addDue(data, item.DueDate, true)

	if item.Completed {
		data["status"] = statusCompleted
	} else {
		data["status"] = statusNeedsAction
	}

	return data
}

// buildRemoveItemData returns the service-call payload for todo.remove_item.
func buildRemoveItemData(entityID, identifier string) map[string]interface{} {
	return map[string]interface{}{
		"entity_id": entityID,
		"item":      identifier,
	}
}

// buildGetItemsData returns the service-call payload for todo.get_items.
func buildGetItemsData(entityID string) map[string]interface{} {
	return map[string]interface{}{
		"entity_id": entityID,
	}
}

// parseDue parses an HA due-date string. It tries date-only format first
// ("2006-01-02"), then falls back to RFC 3339.
func parseDue(s string) (time.Time, error) {
	if t, err := time.Parse(dateLayout, s); err == nil {
		return t, nil
	}
	return time.Parse(time.RFC3339, s)
}

// formatDue formats a time value as a date-only string for HA.
func addDue(data map[string]interface{}, due *time.Time, clear bool) {
	if due == nil {
		if clear {
			data["due_date"] = nil
		}
		return
	}
	if due.Hour() == 0 && due.Minute() == 0 && due.Second() == 0 && due.Nanosecond() == 0 {
		data["due_date"] = formatDue(due)
		return
	}
	data["due_datetime"] = formatDue(due)
}

func formatDue(due *time.Time) string {
	if due.Hour() == 0 && due.Minute() == 0 && due.Second() == 0 && due.Nanosecond() == 0 {
		return due.Format(dateLayout)
	}
	return due.Format(time.RFC3339)
}
