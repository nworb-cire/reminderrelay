package sync

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/BRO3886/go-eventkit"
	"github.com/njoerd114/reminderrelay/internal/model"
	"github.com/njoerd114/reminderrelay/internal/state"
)

// --- Mock Reminders Source ---------------------------------------------------

type mockReminders struct {
	mu      sync.Mutex
	items   map[string]*model.Item // UID → Item
	nextUID int
}

func newMockReminders(items ...*model.Item) *mockReminders {
	m := &mockReminders{items: make(map[string]*model.Item), nextUID: len(items)}
	for _, item := range items {
		m.items[item.UID] = item
	}
	return m
}

func (m *mockReminders) FetchAll(_ context.Context, listNames []string) ([]*model.Item, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	nameSet := make(map[string]bool, len(listNames))
	for _, n := range listNames {
		nameSet[n] = true
	}

	var result []*model.Item
	for _, item := range m.items {
		if nameSet[item.ListName] {
			result = append(result, item)
		}
	}
	return result, nil
}

func (m *mockReminders) Create(_ context.Context, item *model.Item) (*model.Item, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.nextUID++
	uid := fmt.Sprintf("rem-%d", m.nextUID)
	cp := *item
	cp.UID = uid
	cp.CanonicalUID = uid
	m.items[uid] = &cp
	return &cp, nil
}

func advanceRecurringDate(due time.Time, rule eventkit.RecurrenceRule) time.Time {
	interval := rule.Interval
	if interval < 1 {
		interval = 1
	}
	switch rule.Frequency {
	case eventkit.FrequencyWeekly:
		return due.AddDate(0, 0, 7*interval)
	case eventkit.FrequencyMonthly:
		return due.AddDate(0, interval, 0)
	case eventkit.FrequencyYearly:
		return due.AddDate(interval, 0, 0)
	default:
		return due.AddDate(0, 0, interval)
	}
}

func (m *mockReminders) Update(_ context.Context, uid string, item *model.Item) (*model.Item, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	existing, ok := m.items[uid]
	if !ok {
		return nil, fmt.Errorf("reminder %q not found", uid)
	}
	cp := *item
	cp.UID = uid
	cp.CanonicalUID = uid
	m.items[uid] = &cp
	if item.Completed && !existing.Completed && len(existing.RecurrenceRules) > 0 {
		m.nextUID++
		next := cp
		next.UID = fmt.Sprintf("rem-%d", m.nextUID)
		next.CanonicalUID = next.UID
		next.Completed = false
		if next.DueDate != nil {
			due := advanceRecurringDate(*next.DueDate, next.RecurrenceRules[0])
			next.DueDate = &due
		}
		m.items[next.UID] = &next
	}
	return &cp, nil
}

func (m *mockReminders) Delete(_ context.Context, uid string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.items[uid]; !ok {
		return fmt.Errorf("reminder %q not found", uid)
	}
	delete(m.items, uid)
	return nil
}

func (m *mockReminders) get(uid string) *model.Item {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.items[uid]
}

func (m *mockReminders) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.items)
}

// --- Mock HA Source -----------------------------------------------------------

type mockHA struct {
	mu        sync.Mutex
	items     map[string][]model.Item // entityID → items
	nextUID   int
	summaries []model.ListSummary
}

func newMockHA() *mockHA {
	return &mockHA{items: make(map[string][]model.Item), nextUID: 100}
}

func (m *mockHA) addItems(entityID string, items ...model.Item) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.items[entityID] = append(m.items[entityID], items...)
}

func (m *mockHA) GetItems(_ context.Context, entityID string) ([]model.Item, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	items := m.items[entityID]
	// Return copies.
	result := make([]model.Item, len(items))
	copy(result, items)
	return result, nil
}

func (m *mockHA) AddItem(_ context.Context, entityID string, item *model.Item) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.nextUID++
	cp := *item
	cp.UID = fmt.Sprintf("ha-%d", m.nextUID)
	m.items[entityID] = append(m.items[entityID], cp)
	return nil
}

func (m *mockHA) UpdateItem(_ context.Context, entityID, identifier string, item *model.Item) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	items := m.items[entityID]
	for i, h := range items {
		if h.UID == identifier || h.Title == identifier {
			uid := items[i].UID
			items[i] = *item
			items[i].UID = uid
			return nil
		}
	}
	return fmt.Errorf("item %q not found in %s", identifier, entityID)
}

func (m *mockHA) RemoveItem(_ context.Context, entityID, identifier string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	items := m.items[entityID]
	for i, h := range items {
		if h.UID == identifier || h.Title == identifier {
			m.items[entityID] = append(items[:i], items[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("item %q not found in %s", identifier, entityID)
}

func (m *mockHA) PublishListSummary(_ context.Context, summary model.ListSummary) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.summaries = append(m.summaries, summary)
	return nil
}

func (m *mockHA) getItems(entityID string) []model.Item {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.items[entityID]
}

// --- Mock State Store --------------------------------------------------------

type mockStore struct {
	mu     sync.Mutex
	items  map[int64]*state.Item
	nextID int64
}

func newMockStore() *mockStore {
	return &mockStore{items: make(map[int64]*state.Item)}
}

func (m *mockStore) seed(items ...*state.Item) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, item := range items {
		m.nextID++
		item.ID = m.nextID
		m.items[item.ID] = item
	}
}

func (m *mockStore) GetItemByRemindersUID(_ context.Context, uid string) (*state.Item, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, item := range m.items {
		if item.RemindersUID == uid {
			cp := *item
			return &cp, nil
		}
	}
	return nil, nil
}

func (m *mockStore) GetItemByHAUID(_ context.Context, uid string) (*state.Item, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, item := range m.items {
		if item.HAUID == uid {
			cp := *item
			return &cp, nil
		}
	}
	return nil, nil
}

func (m *mockStore) GetAllItemsForList(_ context.Context, listName string) ([]*state.Item, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var result []*state.Item
	for _, item := range m.items {
		if item.ListName == listName {
			cp := *item
			result = append(result, &cp)
		}
	}
	return result, nil
}

func (m *mockStore) UpsertItem(_ context.Context, item *state.Item) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if item.ID == 0 {
		// Check for existing by RemindersUID.
		for _, existing := range m.items {
			if item.RemindersUID != "" && existing.RemindersUID == item.RemindersUID {
				item.ID = existing.ID
				*existing = *item
				return nil
			}
		}
		m.nextID++
		item.ID = m.nextID
	}
	cp := *item
	m.items[item.ID] = &cp
	return nil
}

func (m *mockStore) DeleteItem(_ context.Context, id int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.items, id)
	return nil
}

func (m *mockStore) IsEmpty(_ context.Context) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.items) == 0, nil
}

func (m *mockStore) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.items)
}
