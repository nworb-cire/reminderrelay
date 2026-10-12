package state

import (
	"context"
	"encoding/json"
	"github.com/nworb-cire/reminderrelay/internal/model"
)

// CompletionLink records Apple's rollover from a recurring master to the
// separately archived completed occurrence. It never identifies tasks by title.
type CompletionLink struct {
	ListName              string            `json:"-"`
	OriginHAUID           string            `json:"origin_ha_uid"`
	OriginDue             string            `json:"origin_due"`
	CompletedHAUID        string            `json:"completed_ha_uid"`
	CompletedRemindersUID string            `json:"-"`
	Assignment            *model.Assignment `json:"assignment,omitempty"`
}

func (s *Store) SaveCompletionLink(ctx context.Context, link CompletionLink) error {
	assignment, _ := json.Marshal(link.Assignment)
	_, err := s.db.ExecContext(ctx, `INSERT INTO completion_links(list_name,origin_ha_uid,origin_due,completed_ha_uid,completed_reminders_uid,assignment) VALUES(?,?,?,?,?,?) ON CONFLICT(origin_ha_uid,origin_due) DO UPDATE SET completed_ha_uid=excluded.completed_ha_uid,completed_reminders_uid=excluded.completed_reminders_uid,assignment=excluded.assignment`, link.ListName, link.OriginHAUID, link.OriginDue, link.CompletedHAUID, link.CompletedRemindersUID, string(assignment))
	return err
}
func (s *Store) GetCompletionLinksForList(ctx context.Context, list string) ([]CompletionLink, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT list_name,origin_ha_uid,origin_due,completed_ha_uid,completed_reminders_uid,assignment FROM completion_links WHERE list_name=?`, list)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var links []CompletionLink
	for rows.Next() {
		var link CompletionLink
		var raw string
		if err := rows.Scan(&link.ListName, &link.OriginHAUID, &link.OriginDue, &link.CompletedHAUID, &link.CompletedRemindersUID, &raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(raw), &link.Assignment); err != nil {
			return nil, err
		}
		links = append(links, link)
	}
	return links, rows.Err()
}
