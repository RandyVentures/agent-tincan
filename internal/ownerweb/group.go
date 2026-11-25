package ownerweb

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/mvanhorn/agent-tincan/internal/client"
)

const groupSchema = `CREATE TABLE IF NOT EXISTS owner_web_group_posts (
 owner TEXT NOT NULL, post_id TEXT NOT NULL, audience TEXT NOT NULL, body TEXT NOT NULL,
 created_at INTEGER NOT NULL, PRIMARY KEY(owner,post_id)
);
CREATE INDEX IF NOT EXISTS owner_web_group_recent ON owner_web_group_posts(owner,created_at DESC)`

type groupPost struct {
	ID        string          `json:"id"`
	Audience  string          `json:"audience"`
	Body      string          `json:"body"`
	CreatedAt time.Time       `json:"created_at"`
	Requests  []client.Result `json:"requests"`
}

func (s *Server) groupEnabled() bool {
	return slices.Contains(s.cfg.Targets, "muse") && slices.Contains(s.cfg.Targets, "dot-web")
}

func groupTargets(audience string) []string {
	switch audience {
	case "both":
		return []string{"muse", "dot-web"}
	case "muse", "dot-web":
		return []string{audience}
	default:
		return nil
	}
}

// Reserve the whole post before dispatch. A replay or crash can leave a recipient
// unconfirmed, but can never turn a refresh into a second delivery.
func (s *Server) sendGroup(w http.ResponseWriter, r *http.Request, ctx context.Context) {
	if !s.groupEnabled() {
		http.NotFound(w, r)
		return
	}
	var in struct {
		Audience string `json:"audience"`
		Body     string `json:"body"`
		Key      string `json:"submission_key"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	targets := groupTargets(in.Audience)
	in.Body = strings.TrimSpace(in.Body)
	if len(targets) == 0 || in.Body == "" || len(in.Body) > 16384 || !submissionID.MatchString(in.Key) {
		problem(w, 400, "Choose the recipients and write a message up to 16 KiB.")
		return
	}
	id := "owner-group-" + digest(in.Key)[:40]
	res, err := s.ledger.db.ExecContext(ctx, `INSERT INTO owner_web_group_posts(owner,post_id,audience,body,created_at)
 VALUES(?,?,?,?,?) ON CONFLICT(owner,post_id) DO NOTHING`, s.cfg.Agent, id, in.Audience, in.Body, time.Now().UnixMilli())
	if err != nil {
		s.backendError(w, err)
		return
	}
	n, err := res.RowsAffected()
	if err != nil {
		s.backendError(w, err)
		return
	}
	if n == 0 {
		var same bool
		err = s.ledger.db.QueryRowContext(ctx, `SELECT audience=? AND body=? FROM owner_web_group_posts WHERE owner=? AND post_id=?`, in.Audience, in.Body, s.cfg.Agent, id).Scan(&same)
		if err != nil {
			s.backendError(w, err)
			return
		}
		if !same {
			problem(w, 409, "That post was already submitted with different text or recipients.")
			return
		}
		output(w, 200, map[string]string{"id": id})
		return
	}
	// Once accepted, finish both attempts even if the user closes their tab.
	dispatch, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	var unconfirmed []string
	for _, name := range targets {
		if _, err := s.backend.SendGrouped(dispatch, name, in.Body, id); err != nil {
			unconfirmed = append(unconfirmed, name)
		}
	}
	output(w, 201, map[string]any{"id": id, "unconfirmed": unconfirmed})
}

func (s *Server) groupPosts(ctx context.Context) ([]groupPost, error) {
	// Membership comes from persisted owner-scoped metadata, never user text.
	rows, err := s.ledger.db.QueryContext(ctx, `WITH recent AS (
 SELECT * FROM owner_web_group_posts WHERE owner=? ORDER BY created_at DESC,rowid DESC LIMIT 20
) SELECT p.post_id,p.audience,p.body,p.created_at,COALESCE(r.id,'')
 FROM recent p LEFT JOIN requests r ON r.from_agent=p.owner AND r.group_id=p.post_id
 AND ((p.audience='both' AND r.to_agent IN ('muse','dot-web')) OR p.audience=r.to_agent)
 ORDER BY p.created_at,p.post_id,r.created_at,r.id`, s.cfg.Agent)
	if err != nil {
		return nil, err
	}
	// Close the cursor before fetching full requests. SQLite may use one connection.
	type member struct {
		post int
		id   string
	}
	var members []member
	posts := []groupPost{}
	defer rows.Close()
	for rows.Next() {
		var p groupPost
		var ms int64
		var id string
		if err := rows.Scan(&p.ID, &p.Audience, &p.Body, &ms, &id); err != nil {
			return nil, err
		}
		if len(posts) == 0 || posts[len(posts)-1].ID != p.ID {
			p.CreatedAt = time.UnixMilli(ms).UTC()
			posts = append(posts, p)
		}
		if id != "" {
			members = append(members, member{len(posts) - 1, id})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for _, m := range members {
		item, err := s.backend.Get(ctx, m.id)
		if err != nil {
			return nil, err
		}
		posts[m.post].Requests = append(posts[m.post].Requests, item)
	}
	return posts, nil
}
