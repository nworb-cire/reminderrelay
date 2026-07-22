// Package reminders wraps the go-eventkit reminders library and converts
// between native EventKit types and the shared [model.Item] representation.
//
// The adapter exposes only the operations needed by the sync engine. It
// accepts context.Context on every method for API consistency with the
// architectural invariants, even though the underlying cgo calls are
// non-cancellable (sub-200ms latency).
package reminders

import (
	"context"
	"fmt"
	"log/slog"

	ekreminders "github.com/BRO3886/go-eventkit/reminders"

	"github.com/njoerd114/reminderrelay/internal/model"
)

// EventKitClient is the subset of [ekreminders.Client] methods used by the
// adapter. Defining it as an interface allows mock injection in tests.
type EventKitClient interface {
	Lists() ([]ekreminders.List, error)
	Reminders(opts ...ekreminders.ListOption) ([]ekreminders.Reminder, error)
	CreateReminder(input ekreminders.CreateReminderInput) (*ekreminders.Reminder, error)
	UpdateReminder(id string, input ekreminders.UpdateReminderInput) (*ekreminders.Reminder, error)
	DeleteReminder(id string) error
	CompleteReminder(id string) (*ekreminders.Reminder, error)
	UncompleteReminder(id string) (*ekreminders.Reminder, error)
	WatchChanges(ctx context.Context) (<-chan struct{}, error)
}

// Adapter provides sync-engine–oriented operations on Apple Reminders via
// EventKit. Create one with [NewAdapter] or [NewAdapterWithClient].
type Adapter struct {
	client       EventKitClient
	iCloudSource string
	log          *slog.Logger
}

// NewAdapter creates an Adapter backed by a real EventKit client.
// This triggers the macOS TCC permissions prompt on first use.
func NewAdapter(logger *slog.Logger) (*Adapter, error) {
	c, err := ekreminders.New()
	if err != nil {
		return nil, fmt.Errorf("initialising reminders client: %w", err)
	}
	return &Adapter{client: c, iCloudSource: "iCloud", log: logger}, nil
}

// NewAdapterWithClient creates an Adapter with a caller-supplied client.
// Intended for testing with a mock [EventKitClient].
func NewAdapterWithClient(client EventKitClient, logger *slog.Logger) *Adapter {
	return &Adapter{client: client, iCloudSource: "iCloud", log: logger}
}

// FetchAll returns all reminders (completed and incomplete) across the given
// list names, converted to [model.Item].
func (a *Adapter) FetchAll(ctx context.Context, listNames []string) ([]*model.Item, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("fetch all reminders: %w", err)
	}

	listByID, err := a.resolveICloudLists(listNames)
	if err != nil {
		return nil, err
	}
	rems, err := a.client.Reminders()
	if err != nil {
		return nil, fmt.Errorf("fetching iCloud reminders: %w", err)
	}

	items := make([]*model.Item, 0, len(rems))
	for i := range rems {
		name, tracked := listByID[rems[i].ListID]
		if !tracked {
			continue
		}
		item := reminderToItem(&rems[i], name)
		assignment, assignmentErr := readAssignment(rems[i].ID)
		if assignmentErr != nil {
			a.log.Warn("could not read reminder assignment", "uid", rems[i].ID, "error", assignmentErr)
		} else {
			item.Assignment = assignment
		}
		items = append(items, item)
	}
	return items, nil
}

// resolveICloudLists ensures every configured list resolves to exactly one
// iCloud list. Refusing ambiguous or non-iCloud names prevents an identically
// named local/Exchange list from ever becoming the source of truth.
func (a *Adapter) resolveICloudLists(listNames []string) (map[string]string, error) {
	lists, err := a.client.Lists()
	if err != nil {
		return nil, fmt.Errorf("listing reminder lists: %w", err)
	}
	requested := make(map[string]struct{}, len(listNames))
	for _, name := range listNames {
		requested[name] = struct{}{}
	}
	byID := make(map[string]string, len(listNames))
	for name := range requested {
		var iCloudMatches []ekreminders.List
		var otherMatches []ekreminders.List
		for _, list := range lists {
			if list.Title != name {
				continue
			}
			if list.Source == a.iCloudSource {
				iCloudMatches = append(iCloudMatches, list)
			} else {
				otherMatches = append(otherMatches, list)
			}
		}
		if len(iCloudMatches) == 0 {
			return nil, fmt.Errorf("configured list %q was not found in iCloud", name)
		}
		if len(iCloudMatches) > 1 || len(otherMatches) > 0 {
			return nil, fmt.Errorf("configured list %q is ambiguous across reminder accounts", name)
		}
		if iCloudMatches[0].ReadOnly {
			return nil, fmt.Errorf("configured iCloud list %q is read-only", name)
		}
		byID[iCloudMatches[0].ID] = name
	}
	return byID, nil
}

// Create creates a new reminder from a [model.Item] and returns the
// UID assigned by EventKit.
func (a *Adapter) Create(ctx context.Context, item *model.Item) (*model.Item, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("create reminder: %w", err)
	}
	if _, err := a.resolveICloudLists([]string{item.ListName}); err != nil {
		return nil, err
	}

	input := itemToCreateInput(item)
	a.log.Debug("creating reminder", "title", item.Title, "list", item.ListName)

	rem, err := a.client.CreateReminder(input)
	if err != nil {
		return nil, fmt.Errorf("creating reminder %q in list %q: %w", item.Title, item.ListName, err)
	}
	rollback := func(cause error) (*model.Item, error) {
		if deleteErr := a.client.DeleteReminder(rem.ID); deleteErr != nil {
			return nil, fmt.Errorf("%v (also failed to roll back reminder %q: %w)", cause, rem.ID, deleteErr)
		}
		return nil, cause
	}

	if item.Assignment != nil {
		if _, err := writeAssignment(rem.ID, item.Assignment); err != nil {
			return rollback(fmt.Errorf("assigning new reminder %q: %w", rem.ID, err))
		}
	}

	// If the item should be completed, mark it now — CreateReminder always
	// creates an incomplete reminder.
	if item.Completed {
		rem, err = a.client.CompleteReminder(rem.ID)
		if err != nil {
			return rollback(fmt.Errorf("marking new reminder %q as completed: %w", rem.ID, err))
		}
	}
	canonical := reminderToItem(rem, item.ListName)
	canonical.Assignment = item.Assignment
	return canonical, nil
}

// Update applies the fields from a [model.Item] to an existing reminder.
func (a *Adapter) Update(ctx context.Context, uid string, item *model.Item) (*model.Item, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("update reminder: %w", err)
	}

	a.log.Debug("updating reminder", "uid", uid, "title", item.Title)

	// Fetch current state to decide if completion status changed.
	input := itemToUpdateInput(item)
	updated, err := a.client.UpdateReminder(uid, input)
	if err != nil {
		return nil, fmt.Errorf("updating reminder %q: %w", uid, err)
	}
	assignment, err := a.updateAssignment(uid, item.Assignment)
	if err != nil {
		return nil, fmt.Errorf("updating assignment for reminder %q: %w", uid, err)
	}

	// Handle completion status change through the dedicated API so that
	// CompletionDate is set/cleared properly.
	if item.Completed && !updated.Completed {
		updated, err = a.client.CompleteReminder(uid)
		if err != nil {
			return nil, fmt.Errorf("completing reminder %q: %w", uid, err)
		}
	} else if !item.Completed && updated.Completed {
		updated, err = a.client.UncompleteReminder(uid)
		if err != nil {
			return nil, fmt.Errorf("uncompleting reminder %q: %w", uid, err)
		}
	}
	canonical := reminderToItem(updated, item.ListName)
	canonical.Assignment = assignment
	return canonical, nil
}

// updateAssignment avoids invoking the private ReminderKit write path for the
// overwhelmingly common unassigned case. That keeps ordinary reminder edits
// working even when assignment metadata is unavailable on a particular macOS
// release, while still surfacing errors when an assignment change was asked
// for explicitly.
func (a *Adapter) updateAssignment(uid string, desired *model.Assignment) (*model.Assignment, error) {
	current, err := readAssignment(uid)
	if err != nil {
		if desired == nil {
			a.log.Warn("could not inspect reminder assignment; leaving it unchanged", "uid", uid, "error", err)
			return nil, nil
		}
		return writeAssignment(uid, desired)
	}
	if assignmentsEqual(current, desired) {
		return current, nil
	}
	return writeAssignment(uid, desired)
}

func assignmentsEqual(a, b *model.Assignment) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.ID == b.ID && a.Name == b.Name && a.Address == b.Address
}

// Delete permanently removes a reminder by UID.
func (a *Adapter) Delete(ctx context.Context, uid string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("delete reminder: %w", err)
	}

	a.log.Debug("deleting reminder", "uid", uid)
	if err := a.client.DeleteReminder(uid); err != nil {
		return fmt.Errorf("deleting reminder %q: %w", uid, err)
	}
	return nil
}

// WatchChanges subscribes to EventKit database changes, including iCloud push
// updates and writes from Reminders.app. The signal contains no diff; callers
// must refetch canonical state.
func (a *Adapter) WatchChanges(ctx context.Context) (<-chan struct{}, error) {
	return a.client.WatchChanges(ctx)
}
