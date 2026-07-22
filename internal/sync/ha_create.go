package sync

import (
	"context"
	"fmt"

	"github.com/nworb-cire/reminderrelay/internal/model"
)

// addToHAAndResolveUID creates an HA todo item and identifies the UID assigned
// by HA from the before/after set difference. Titles are not identifiers:
// recurring reminders legitimately produce multiple occurrences with the same
// title, and clean HA descriptions intentionally omit the iCloud UID.
func addToHAAndResolveUID(ctx context.Context, ha HASource, entityID string, item *model.Item) (string, error) {
	before, err := ha.GetItems(ctx, entityID)
	if err != nil {
		return "", fmt.Errorf("fetching items from %s before add: %w", entityID, err)
	}
	known := make(map[string]struct{}, len(before))
	for _, existing := range before {
		known[existing.UID] = struct{}{}
	}

	if err := ha.AddItem(ctx, entityID, item); err != nil {
		return "", err
	}

	after, err := ha.GetItems(ctx, entityID)
	if err != nil {
		return "", fmt.Errorf("refetching items from %s after add: %w", entityID, err)
	}
	candidates := make([]model.Item, 0, 1)
	for _, candidate := range after {
		if _, existed := known[candidate.UID]; !existed {
			candidates = append(candidates, candidate)
		}
	}
	if len(candidates) == 1 {
		if err := ha.UpdateItem(ctx, entityID, candidates[0].UID, item); err != nil {
			return "", fmt.Errorf("applying canonical fields to new HA item %q: %w", item.Title, err)
		}
		return candidates[0].UID, nil
	}
	for _, candidate := range candidates {
		if candidate.CanonicalUID == item.UID {
			if err := ha.UpdateItem(ctx, entityID, candidate.UID, item); err != nil {
				return "", fmt.Errorf("applying canonical fields to new HA item %q: %w", item.Title, err)
			}
			return candidate.UID, nil
		}
	}
	return "", fmt.Errorf("identifying newly added HA item %q: found %d new candidates", item.Title, len(candidates))
}
