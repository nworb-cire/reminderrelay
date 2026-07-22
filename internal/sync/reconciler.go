package sync

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/njoerd114/reminderrelay/internal/model"
	"github.com/njoerd114/reminderrelay/internal/state"
)

// action describes a single mutation the reconciler wants to perform.
type action int

const (
	actionNone          action = iota
	actionCreateInHA           // item exists in Reminders only → push to HA
	actionCreateInRem          // item exists in HA only → push to Reminders
	actionUpdateHA             // Reminders is the winner → push to HA
	actionUpdateRem            // HA is the winner → push to Reminders
	actionDeleteFromHA         // item deleted from Reminders → remove from HA
	actionDeleteFromRem        // item deleted from HA → remove from Reminders
)

// Stats tracks the number of mutations performed in a single reconcile pass.
type Stats struct {
	Created   int
	Updated   int
	Deleted   int
	Conflicts int
	Errors    int
}

// Reconciler performs a single bidirectional sync pass across all configured
// list mappings. It is stateless between calls — all persistent state lives
// in the [StateStore].
type Reconciler struct {
	rem   RemindersSource
	ha    HASource
	store StateStore
	log   *slog.Logger
}

// NewReconciler creates a Reconciler wired to the given adapters and state store.
func NewReconciler(rem RemindersSource, ha HASource, store StateStore, logger *slog.Logger) *Reconciler {
	return &Reconciler{rem: rem, ha: ha, store: store, log: logger}
}

// Run performs a full bidirectional sync for all list mappings. It returns
// aggregate statistics and the first error encountered (sync continues past
// individual item errors to maximise progress).
func (r *Reconciler) Run(ctx context.Context, listMappings map[string]string) (Stats, error) {
	var stats Stats
	var firstErr error

	listNames := make([]string, 0, len(listMappings))
	for name := range listMappings {
		listNames = append(listNames, name)
	}

	// 1. Fetch all Reminders items across configured lists.
	remItems, err := r.rem.FetchAll(ctx, listNames)
	if err != nil {
		return stats, fmt.Errorf("fetching reminders: %w", err)
	}

	// Index Reminders items by UID for fast lookup.
	remByUID := make(map[string]*model.Item, len(remItems))
	for _, item := range remItems {
		remByUID[item.UID] = item
	}

	// 2. Process each list mapping independently.
	for listName, entityID := range listMappings {
		ls, err := r.reconcileList(ctx, listName, entityID, remByUID)
		stats.Created += ls.Created
		stats.Updated += ls.Updated
		stats.Deleted += ls.Deleted
		stats.Conflicts += ls.Conflicts
		stats.Errors += ls.Errors
		if err != nil && firstErr == nil {
			firstErr = err
		}
		if err := r.publishListSummary(ctx, listName, entityID); err != nil {
			stats.Errors++
			r.log.Error("publishing list summary failed", "list", listName, "error", err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}

	r.log.Info("reconcile complete",
		"created", stats.Created,
		"updated", stats.Updated,
		"deleted", stats.Deleted,
		"conflicts", stats.Conflicts,
		"errors", stats.Errors,
	)

	return stats, firstErr
}

// ReconcileEntity performs reconciliation for a single HA entity. Called by
// the WebSocket listener when a state_changed event is received.
func (r *Reconciler) ReconcileEntity(ctx context.Context, listName, entityID string) (Stats, error) {
	// We need the Reminders items for just this list.
	remItems, err := r.rem.FetchAll(ctx, []string{listName})
	if err != nil {
		return Stats{}, fmt.Errorf("fetching reminders for %q: %w", listName, err)
	}

	remByUID := make(map[string]*model.Item, len(remItems))
	for _, item := range remItems {
		remByUID[item.UID] = item
	}

	stats, reconcileErr := r.reconcileList(ctx, listName, entityID, remByUID)
	if err := r.publishListSummary(ctx, listName, entityID); err != nil {
		stats.Errors++
		if reconcileErr == nil {
			reconcileErr = err
		}
	}
	return stats, reconcileErr
}

// reconcileList performs bidirectional sync for a single list ↔ entity pair.
func (r *Reconciler) reconcileList(ctx context.Context, listName, entityID string, remByUID map[string]*model.Item) (Stats, error) {
	var stats Stats
	var firstErr error

	r.log.Debug("reconciling list", "list", listName, "entity", entityID)

	// Fetch HA items for this entity.
	haItems, err := r.ha.GetItems(ctx, entityID)
	if err != nil {
		return stats, fmt.Errorf("fetching HA items for %s: %w", entityID, err)
	}

	// Index HA items by UID.
	haByUID := make(map[string]*model.Item, len(haItems))
	for i := range haItems {
		haItems[i].ListName = listName
		haByUID[haItems[i].UID] = &haItems[i]
	}

	// Fetch all tracked state items for this list.
	stateItems, err := r.store.GetAllItemsForList(ctx, listName)
	if err != nil {
		return stats, fmt.Errorf("fetching state items for %q: %w", listName, err)
	}

	// Build a set of state RemindersUIDs and HAUIDs we've processed,
	// so we can detect new items after processing tracked ones.
	processedRemUIDs := make(map[string]bool, len(stateItems))
	processedHAUIDs := make(map[string]bool, len(stateItems))

	// 1. Process items we're already tracking.
	for _, si := range stateItems {
		remItem := remByUID[si.RemindersUID]
		haItem := haByUID[si.HAUID]

		if si.RemindersUID != "" {
			processedRemUIDs[si.RemindersUID] = true
		}
		if si.HAUID != "" {
			processedHAUIDs[si.HAUID] = true
		}

		act := r.decide(si, remItem, haItem)
		oldHash := si.LastSyncHash // capture before execute modifies si
		if err := r.execute(ctx, act, si, remItem, haItem, entityID); err != nil {
			r.log.Error("sync action failed",
				"action", act,
				"title", si.Title,
				"error", err,
			)
			stats.Errors++
			if firstErr == nil {
				firstErr = err
			}
			continue
		}

		switch act {
		case actionCreateInHA, actionCreateInRem:
			stats.Created++
		case actionUpdateHA, actionUpdateRem:
			stats.Updated++
			// Check if this was a conflict (both sides changed).
			if remItem != nil && haItem != nil {
				remHash := remItem.ProjectionHash()
				haHash := haItem.ProjectionHash()
				if remHash != oldHash && haHash != oldHash {
					stats.Conflicts++
				}
			}
		case actionDeleteFromHA, actionDeleteFromRem:
			stats.Deleted++
		}
	}

	// 2. Detect new Reminders items not in state DB → create in HA.
	for uid, remItem := range remByUID {
		if remItem.ListName != listName {
			continue
		}
		if processedRemUIDs[uid] {
			continue
		}

		r.log.Info("new reminder detected", "title", remItem.Title, "uid", uid)
		if err := r.createInHA(ctx, remItem, entityID); err != nil {
			r.log.Error("failed to create in HA", "title", remItem.Title, "error", err)
			stats.Errors++
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		stats.Created++
	}

	// 3. Detect new HA items not in state DB → create in Reminders.
	for uid, haItem := range haByUID {
		if processedHAUIDs[uid] {
			continue
		}

		r.log.Info("new HA item detected", "title", haItem.Title, "uid", uid)
		if err := r.createInReminders(ctx, haItem, entityID); err != nil {
			r.log.Error("failed to create in Reminders", "title", haItem.Title, "error", err)
			stats.Errors++
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		stats.Created++
	}

	return stats, firstErr
}

// decide determines what action to take for a tracked item based on hash
// and timestamp comparison.
func (r *Reconciler) decide(si *state.Item, remItem, haItem *model.Item) action {
	remExists := remItem != nil
	haExists := haItem != nil

	// Both deleted → just clean up state (handled as deleteFromHA path).
	if !remExists && !haExists {
		return actionDeleteFromHA // will clean state DB only
	}

	// Deleted from Reminders, still in HA → delete from HA.
	if !remExists && haExists {
		return actionDeleteFromHA
	}

	// Deleted from HA, still in Reminders → delete from Reminders.
	if remExists && !haExists {
		return actionDeleteFromRem
	}

	// Both exist — check for changes via content hash.
	if haItem.LegacyMetadata {
		return actionUpdateHA
	}
	remHash := remItem.ProjectionHash()
	haHash := haItem.ProjectionHash()
	remChanged := remHash != si.LastSyncHash
	haChanged := haHash != si.LastSyncHash

	// Neither changed → no-op.
	if !remChanged && !haChanged {
		return actionNone
	}

	// Only one side changed → propagate.
	if remChanged && !haChanged {
		return actionUpdateHA
	}
	if !remChanged && haChanged {
		return actionUpdateRem
	}

	// Both changed → iCloud wins. Home Assistant is a writable projection, but
	// Apple Reminders is always the canonical state and conflict authority.
	r.log.Info("conflict detected",
		"title", si.Title,
		"reminders_modified", remItem.ModifiedAt,
		"ha_modified", haItem.ModifiedAt,
	)

	return actionUpdateHA
}

// execute dispatches the decided action to the appropriate adapter and
// updates the state DB.
func (r *Reconciler) execute(ctx context.Context, act action, si *state.Item, remItem, haItem *model.Item, entityID string) error {
	now := time.Now().UTC()

	switch act {
	case actionNone:
		return nil

	case actionCreateInHA:
		// Reminders item exists but HA counterpart was deleted → shouldn't normally
		// happen for tracked items; treat as delete from Reminders.
		// Actually this case is: item tracked, Reminders still exists, HA gone →
		// we chose actionDeleteFromRem above. So fall through here is unexpected.
		// This branch handles the edge case defensively.
		return r.store.DeleteItem(ctx, si.ID)

	case actionCreateInRem:
		// Same defensive logic as above.
		return r.store.DeleteItem(ctx, si.ID)

	case actionDeleteFromHA:
		if haItem != nil {
			if err := r.ha.RemoveItem(ctx, entityID, haItem.UID); err != nil {
				return fmt.Errorf("deleting %q from HA: %w", si.Title, err)
			}
		}
		return r.store.DeleteItem(ctx, si.ID)

	case actionDeleteFromRem:
		if remItem != nil {
			if err := r.rem.Delete(ctx, remItem.UID); err != nil {
				return fmt.Errorf("deleting %q from Reminders: %w", si.Title, err)
			}
		}
		return r.store.DeleteItem(ctx, si.ID)

	case actionUpdateHA:
		// Use the HA item's current title to identify it (may differ from
		// state DB title if both sides changed).
		currentHAIdentifier := si.HAUID
		if haItem != nil {
			currentHAIdentifier = haItem.UID
		}
		if err := r.ha.UpdateItem(ctx, entityID, currentHAIdentifier, remItem); err != nil {
			return fmt.Errorf("updating %q in HA: %w", remItem.Title, err)
		}
		si.Title = remItem.Title
		si.LastSyncHash = remItem.ProjectionHash()
		si.RemindersModified = remItem.ModifiedAt
		si.LastSyncedAt = now
		return r.store.UpsertItem(ctx, si)

	case actionUpdateRem:
		command := mergeHAProjection(remItem, haItem)
		canonical, err := r.rem.Update(ctx, si.RemindersUID, command)
		if err != nil {
			return fmt.Errorf("updating %q in Reminders: %w", haItem.Title, err)
		}
		// Echo the committed iCloud representation back to HA. This is important
		// for recurrence (completion may materialize another occurrence) and for
		// metadata normalized by ReminderKit.
		if err := r.ha.UpdateItem(ctx, entityID, haItem.UID, canonical); err != nil {
			return fmt.Errorf("refreshing %q from canonical iCloud state: %w", canonical.Title, err)
		}
		si.Title = canonical.Title
		si.LastSyncHash = canonical.ProjectionHash()
		si.RemindersModified = canonical.ModifiedAt
		si.HAModified = haItem.ModifiedAt
		si.LastSyncedAt = now
		return r.store.UpsertItem(ctx, si)
	}

	return nil
}

// createInHA pushes a new Reminders item to HA and writes the state DB entry.
func (r *Reconciler) createInHA(ctx context.Context, remItem *model.Item, entityID string) error {
	haUID, err := addToHAAndResolveUID(ctx, r.ha, entityID, remItem)
	if err != nil {
		return fmt.Errorf("adding %q to HA: %w", remItem.Title, err)
	}

	now := time.Now().UTC()
	si := &state.Item{
		RemindersUID:      remItem.UID,
		HAUID:             haUID,
		ListName:          remItem.ListName,
		Title:             remItem.Title,
		LastSyncHash:      remItem.ProjectionHash(),
		RemindersModified: remItem.ModifiedAt,
		LastSyncedAt:      now,
	}
	return r.store.UpsertItem(ctx, si)
}

// createInReminders pushes a new HA item to Reminders and writes the state DB entry.
func (r *Reconciler) createInReminders(ctx context.Context, haItem *model.Item, entityID string) error {
	canonical, err := r.rem.Create(ctx, haItem)
	if err != nil {
		return fmt.Errorf("creating %q in Reminders: %w", haItem.Title, err)
	}
	if err := r.ha.UpdateItem(ctx, entityID, haItem.UID, canonical); err != nil {
		return fmt.Errorf("refreshing new item %q from canonical iCloud state: %w", canonical.Title, err)
	}

	now := time.Now().UTC()
	si := &state.Item{
		RemindersUID:      canonical.UID,
		HAUID:             haItem.UID,
		ListName:          haItem.ListName,
		Title:             canonical.Title,
		LastSyncHash:      canonical.ProjectionHash(),
		RemindersModified: canonical.ModifiedAt,
		HAModified:        haItem.ModifiedAt,
		LastSyncedAt:      now,
	}
	return r.store.UpsertItem(ctx, si)
}

// mergeHAProjection applies only fields Home Assistant represents onto the
// latest canonical iCloud item. Tags, assignment, recurrence, and all other
// native metadata remain untouched.
func mergeHAProjection(canonical, projection *model.Item) *model.Item {
	merged := *canonical
	merged.Title = projection.Title
	merged.Description = projection.Description
	merged.DueDate = projection.DueDate
	merged.Priority = projection.Priority
	merged.Completed = projection.Completed
	return &merged
}

func (r *Reconciler) publishListSummary(ctx context.Context, listName, entityID string) error {
	items, err := r.rem.FetchAll(ctx, []string{listName})
	if err != nil {
		return fmt.Errorf("fetching metadata summary for %q: %w", listName, err)
	}
	summary := model.ListSummary{
		ListName:        listName,
		TodoEntityID:    entityID,
		ByAssignee:      make(map[string]int),
		ByTag:           make(map[string]int),
		TasksByAssignee: make(map[string][]model.SummaryTask),
		UpdatedAt:       time.Now().UTC(),
	}
	groups := make(map[string]*model.AssigneeSummary)
	for _, item := range items {
		if item.ListName != listName || item.Completed {
			continue
		}
		assigneeName := "Unassigned"
		assigneeKey := "__unassigned__"
		if item.Assignment != nil {
			switch {
			case item.Assignment.Name != "":
				assigneeName = item.Assignment.Name
			case item.Assignment.Address != "":
				assigneeName = item.Assignment.Address
			case item.Assignment.ID != "":
				assigneeName = item.Assignment.ID
			}
			assigneeKey = item.Assignment.ID
			if assigneeKey == "" {
				assigneeKey = item.Assignment.Address
			}
			if assigneeKey == "" {
				assigneeKey = item.Assignment.Name
			}
		}
		task := model.SummaryTask{
			UID:        item.UID,
			Title:      item.Title,
			DueAt:      item.DueDate,
			Tags:       append([]string(nil), item.Tags...),
			Assignment: item.Assignment,
		}
		summary.Remaining++
		summary.ByAssignee[assigneeName]++
		summary.TasksByAssignee[assigneeName] = append(summary.TasksByAssignee[assigneeName], task)
		for _, tag := range item.Tags {
			summary.ByTag[tag]++
		}
		group := groups[assigneeKey]
		if group == nil {
			group = &model.AssigneeSummary{Assignment: item.Assignment, Name: assigneeName}
			groups[assigneeKey] = group
		}
		group.Remaining++
		group.Tasks = append(group.Tasks, task)
	}
	for _, group := range groups {
		summary.Assignees = append(summary.Assignees, *group)
	}
	sort.Slice(summary.Assignees, func(i, j int) bool {
		return strings.ToLower(summary.Assignees[i].Name) < strings.ToLower(summary.Assignees[j].Name)
	})
	return r.ha.PublishListSummary(ctx, summary)
}
