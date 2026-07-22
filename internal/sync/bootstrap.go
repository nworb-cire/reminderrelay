package sync

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/nworb-cire/reminderrelay/internal/model"
	"github.com/nworb-cire/reminderrelay/internal/state"
)

// ErrBootstrapCancelled prevents the daemon from reconciling against an empty
// state database after the user declines the first-run write summary.
var ErrBootstrapCancelled = errors.New("first-run bootstrap was not confirmed")

// Bootstrap performs the first-run linkage of existing items between Apple
// Reminders and Home Assistant. It matches items by title, prints a summary,
// and (with user confirmation) writes the state DB entries and pushes
// unmatched items from Reminders to HA.
type Bootstrap struct {
	rem    RemindersSource
	ha     HASource
	store  StateStore
	log    *slog.Logger
	reader io.Reader // for confirmation prompt (os.Stdin in production)
	writer io.Writer // for summary output (os.Stdout in production)
}

// NewBootstrap creates a Bootstrap wired to the given adapters and state store.
// reader and writer control the confirmation prompt I/O.
func NewBootstrap(rem RemindersSource, ha HASource, store StateStore, logger *slog.Logger, reader io.Reader, writer io.Writer) *Bootstrap {
	return &Bootstrap{
		rem:    rem,
		ha:     ha,
		store:  store,
		log:    logger,
		reader: reader,
		writer: writer,
	}
}

// matchResult holds the result of title-matching for a single list mapping.
type matchResult struct {
	listName string
	entityID string

	// Matched pairs: Reminders item + HA item that share a title.
	matched []matchedPair

	// Unmatched items that exist only on one side.
	remOnly []*model.Item
	haOnly  []*model.Item
}

type matchedPair struct {
	rem *model.Item
	ha  *model.Item
}

// Run checks whether the state DB is empty and, if so, performs the first-run
// bootstrap. Returns true if bootstrap was executed, false if skipped.
func (b *Bootstrap) Run(ctx context.Context, listMappings map[string]string) (bool, error) {
	empty, err := b.store.IsEmpty(ctx)
	if err != nil {
		return false, fmt.Errorf("checking state DB: %w", err)
	}
	if !empty {
		b.log.Debug("state DB is not empty, skipping bootstrap")
		return false, nil
	}

	b.log.Info("empty state DB detected, starting first-run bootstrap")

	listNames := make([]string, 0, len(listMappings))
	for name := range listMappings {
		listNames = append(listNames, name)
	}

	// Fetch all Reminders items.
	remItems, err := b.rem.FetchAll(ctx, listNames)
	if err != nil {
		return false, fmt.Errorf("fetching reminders for bootstrap: %w", err)
	}

	// Group Reminders items by list.
	remByList := make(map[string][]*model.Item)
	for _, item := range remItems {
		remByList[item.ListName] = append(remByList[item.ListName], item)
	}

	// Match each list.
	var results []matchResult
	for listName, entityID := range listMappings {
		haItems, err := b.ha.GetItems(ctx, entityID)
		if err != nil {
			return false, fmt.Errorf("fetching HA items for %s: %w", entityID, err)
		}

		result := matchByTitle(listName, entityID, remByList[listName], haItems)
		results = append(results, result)
	}

	// Print summary.
	b.printSummary(results)

	// Ask for confirmation.
	if !b.confirm() {
		b.log.Info("bootstrap cancelled by user")
		return false, ErrBootstrapCancelled
	}

	// Execute: write matched pairs to state DB, push unmatched Reminders → HA.
	if err := b.execute(ctx, results); err != nil {
		return false, fmt.Errorf("executing bootstrap: %w", err)
	}

	b.log.Info("bootstrap complete")
	return true, nil
}

// matchByTitle first honors an existing canonical iCloud UID embedded in HA,
// then pairs remaining items by title (case-insensitive). Each item can be
// consumed only once, which makes duplicate titles safe during bootstrap.
func matchByTitle(listName, entityID string, remItems []*model.Item, haItems []model.Item) matchResult {
	result := matchResult{
		listName: listName,
		entityID: entityID,
	}

	// Build HA canonical UID/title indexes.
	haByCanonicalUID := make(map[string]int, len(haItems))
	haByTitle := make(map[string][]int, len(haItems))
	for i := range haItems {
		haItems[i].ListName = listName
		if haItems[i].CanonicalUID != "" {
			haByCanonicalUID[haItems[i].CanonicalUID] = i
		}
		key := strings.ToLower(haItems[i].Title)
		haByTitle[key] = append(haByTitle[key], i)
	}

	usedHA := make(map[int]bool, len(haItems))

	for _, rem := range remItems {
		if idx, ok := haByCanonicalUID[rem.UID]; ok && !usedHA[idx] {
			result.matched = append(result.matched, matchedPair{rem: rem, ha: &haItems[idx]})
			usedHA[idx] = true
			continue
		}

		matched := false
		for _, idx := range haByTitle[strings.ToLower(rem.Title)] {
			if usedHA[idx] {
				continue
			}
			result.matched = append(result.matched, matchedPair{rem: rem, ha: &haItems[idx]})
			usedHA[idx] = true
			matched = true
			break
		}
		if !matched {
			result.remOnly = append(result.remOnly, rem)
		}
	}

	for i := range haItems {
		if !usedHA[i] {
			result.haOnly = append(result.haOnly, &haItems[i])
		}
	}

	return result
}

// printSummary writes a human-readable summary of the match results.
func (b *Bootstrap) printSummary(results []matchResult) {
	totalMatched := 0
	totalRemOnly := 0
	totalHAOnly := 0

	for _, r := range results {
		totalMatched += len(r.matched)
		totalRemOnly += len(r.remOnly)
		totalHAOnly += len(r.haOnly)
	}

	_, _ = fmt.Fprintf(b.writer, "\n--- First-Run Bootstrap Summary ---\n\n")

	for _, r := range results {
		_, _ = fmt.Fprintf(b.writer, "List %q ↔ %s:\n", r.listName, r.entityID)
		_, _ = fmt.Fprintf(b.writer, "  Matched by title: %d\n", len(r.matched))
		for _, m := range r.matched {
			_, _ = fmt.Fprintf(b.writer, "    ✓ %s\n", m.rem.Title)
		}
		if len(r.remOnly) > 0 {
			_, _ = fmt.Fprintf(b.writer, "  Only in Reminders (will push to HA): %d\n", len(r.remOnly))
			for _, item := range r.remOnly {
				_, _ = fmt.Fprintf(b.writer, "    → %s\n", item.Title)
			}
		}
		if len(r.haOnly) > 0 {
			_, _ = fmt.Fprintf(b.writer, "  Only in HA (will push to Reminders): %d\n", len(r.haOnly))
			for _, item := range r.haOnly {
				_, _ = fmt.Fprintf(b.writer, "    ← %s\n", item.Title)
			}
		}
		_, _ = fmt.Fprintln(b.writer)
	}

	_, _ = fmt.Fprintf(b.writer, "Total: %d matched, %d Reminders→HA, %d HA→Reminders\n",
		totalMatched, totalRemOnly, totalHAOnly)
}

// confirm reads a y/n response from the reader.
func (b *Bootstrap) confirm() bool {
	_, _ = fmt.Fprintf(b.writer, "Proceed with sync? [y/N] ")
	scanner := bufio.NewScanner(b.reader)
	if scanner.Scan() {
		answer := strings.TrimSpace(strings.ToLower(scanner.Text()))
		return answer == "y" || answer == "yes"
	}
	return false
}

// execute writes all matched pairs to the state DB and pushes unmatched items.
func (b *Bootstrap) execute(ctx context.Context, results []matchResult) error {
	now := time.Now().UTC()

	for _, r := range results {
		// Write matched pairs.
		for _, m := range r.matched {
			// Matching is identity discovery only. iCloud content is canonical,
			// so refresh the HA projection before recording the baseline hash.
			if err := b.ha.UpdateItem(ctx, r.entityID, m.ha.UID, m.rem); err != nil {
				return fmt.Errorf("refreshing matched item %q from iCloud: %w", m.rem.Title, err)
			}
			si := &state.Item{
				RemindersUID:      m.rem.UID,
				HAUID:             m.ha.UID,
				ListName:          r.listName,
				Title:             m.rem.Title,
				LastSyncHash:      m.rem.ProjectionHash(),
				RemindersModified: m.rem.ModifiedAt,
				HAModified:        m.ha.ModifiedAt,
				LastSyncedAt:      now,
			}
			if err := b.store.UpsertItem(ctx, si); err != nil {
				return fmt.Errorf("writing matched pair %q: %w", m.rem.Title, err)
			}
			b.log.Debug("linked matched pair", "title", m.rem.Title)
		}

		// Push Reminders-only items to HA.
		for _, item := range r.remOnly {
			haUID, err := addToHAAndResolveUID(ctx, b.ha, r.entityID, item)
			if err != nil {
				return fmt.Errorf("pushing %q to HA: %w", item.Title, err)
			}

			si := &state.Item{
				RemindersUID:      item.UID,
				HAUID:             haUID,
				ListName:          r.listName,
				Title:             item.Title,
				LastSyncHash:      item.ProjectionHash(),
				RemindersModified: item.ModifiedAt,
				LastSyncedAt:      now,
			}
			if err := b.store.UpsertItem(ctx, si); err != nil {
				return fmt.Errorf("writing state for %q: %w", item.Title, err)
			}
			b.log.Info("pushed to HA", "title", item.Title)
		}

		// Push HA-only items to Reminders.
		for _, item := range r.haOnly {
			canonical, err := b.rem.Create(ctx, item)
			if err != nil {
				return fmt.Errorf("pushing %q to Reminders: %w", item.Title, err)
			}
			if err := b.ha.UpdateItem(ctx, r.entityID, item.UID, canonical); err != nil {
				return fmt.Errorf("refreshing %q from iCloud: %w", item.Title, err)
			}

			si := &state.Item{
				RemindersUID:      canonical.UID,
				HAUID:             item.UID,
				ListName:          r.listName,
				Title:             canonical.Title,
				LastSyncHash:      canonical.ProjectionHash(),
				RemindersModified: canonical.ModifiedAt,
				HAModified:        item.ModifiedAt,
				LastSyncedAt:      now,
			}
			if err := b.store.UpsertItem(ctx, si); err != nil {
				return fmt.Errorf("writing state for %q: %w", item.Title, err)
			}
			b.log.Info("pushed to Reminders", "title", item.Title)
		}
	}

	return nil
}
