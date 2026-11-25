package store

import (
	"context"
	"strings"
	"time"
)

// OwnerDelivery describes one owner's asks to one agent. Only timestamps and
// fixed diagnostic codes leave the store; message and reply bodies stay private.
type OwnerDelivery struct {
	LastReply   time.Time `json:"last_reply,omitzero"`
	LastProblem time.Time `json:"last_problem,omitzero"`
	Problem     string    `json:"problem,omitempty"`
}

func (s *Store) OwnerDelivery(ctx context.Context, owner, target string) (OwnerDelivery, error) {
	const query = `
SELECT * FROM (
 SELECT q.status, r.created_at, '' FROM requests q JOIN replies r ON r.request_id=q.id
 WHERE q.from_agent=?1 AND q.to_agent=?2 AND q.kind='ask' AND q.status='answered'
 AND r.from_agent=q.to_agent AND r.status='answered' ORDER BY q.updated_at DESC,q.id DESC LIMIT 1
)
UNION ALL SELECT * FROM (
 SELECT q.status, q.updated_at, COALESCE(substr(r.body,1,512),'') FROM requests q
 LEFT JOIN replies r ON r.request_id=q.id AND r.from_agent=q.to_agent
 WHERE q.from_agent=?1 AND q.to_agent=?2 AND q.kind='ask' AND q.status='failed'
 ORDER BY q.updated_at DESC,q.id DESC LIMIT 1
)
UNION ALL SELECT * FROM (
 SELECT status, updated_at, '' FROM requests WHERE from_agent=?1 AND to_agent=?2 AND kind='ask' AND status='expired'
 ORDER BY updated_at DESC,id DESC LIMIT 1
)`
	rows, err := s.db.QueryContext(ctx, query, owner, target)
	if err != nil {
		return OwnerDelivery{}, err
	}
	defer rows.Close()
	var d OwnerDelivery
	for rows.Next() {
		var status, body string
		var ms int64
		if err := rows.Scan(&status, &ms, &body); err != nil {
			return OwnerDelivery{}, err
		}
		at := time.UnixMilli(ms).UTC()
		if status == "answered" {
			d.LastReply = at
			continue
		}
		if d.LastProblem.Before(at) {
			d.LastProblem = at
			d.Problem = status
			if status == "failed" && strings.HasPrefix(body, "Sorry, source unavailable:") {
				switch {
				case strings.Contains(body, "not logged in"):
					d.Problem = "signed_out"
				case strings.Contains(body, "extension is not connected"):
					d.Problem = "extension"
				case strings.Contains(body, "send button stayed disabled"), strings.Contains(body, "message box did not become editable"):
					d.Problem = "editor"
				}
			}
		}
	}
	return d, rows.Err()
}

// OwnerDirectIDs excludes shared posts before limiting the direct inbox.
func (s *Store) OwnerDirectIDs(ctx context.Context, owner string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM requests r WHERE from_agent=? AND (hop=1 OR parent_id='') AND kind!='ping'
	AND (group_id='' OR NOT EXISTS (SELECT 1 FROM owner_web_group_posts p WHERE p.owner=r.from_agent AND p.post_id=r.group_id)) ORDER BY created_at DESC,id DESC LIMIT 20`, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
