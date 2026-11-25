package ownerweb

import (
	"context"
	"slices"
	"strings"
	"unicode/utf8"
)

// Only earlier posts in this owner's shared thread can contribute context.
// A missing delivery or unreleased approval excludes the whole post. Only an
// addressed agent's own successful reply is eligible, never a relay diagnostic.
func (s *Server) sharedPrompt(ctx context.Context, id, body string) (string, error) {
	rows, err := s.ledger.db.QueryContext(ctx, `WITH recent AS (
 SELECT p.rowid AS seq,p.post_id,p.body,p.audience FROM owner_web_group_posts p
 WHERE p.owner=?1 AND p.post_id GLOB ?2 AND p.rowid < (SELECT rowid FROM owner_web_group_posts WHERE owner=?1 AND post_id=?3)
 ORDER BY p.rowid DESC LIMIT 8
) SELECT p.post_id,p.body,r.from_agent,r.body FROM recent p
 LEFT JOIN requests q ON q.from_agent=?1 AND q.group_id=p.post_id
 AND (q.to_agent=p.audience OR (p.audience='both' AND q.to_agent IN ('muse','dot-web')))
 LEFT JOIN replies r ON q.id=r.request_id AND r.from_agent=q.to_agent AND q.status='answered' AND r.status='answered'
 WHERE (CASE p.audience WHEN 'both' THEN 2 ELSE 1 END) = (
 SELECT COUNT(DISTINCT a.to_agent) FROM requests a WHERE a.from_agent=?1 AND a.group_id=p.post_id
 AND (a.to_agent=p.audience OR (p.audience='both' AND a.to_agent IN ('muse','dot-web')))
 AND a.status NOT IN ('held','declined','cancelled') AND (a.was_held=0 OR a.approved=1))
 ORDER BY p.seq,r.created_at,q.id`, s.cfg.Agent, sharedPostPrefix+"*", id)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var lines []string
	var previous string
	for rows.Next() {
		var post, text string
		var author, reply *string
		if err := rows.Scan(&post, &text, &author, &reply); err != nil {
			return "", err
		}
		if post != previous {
			lines = append(lines, sharedLine("Owner", text))
			previous = post
		}
		if author != nil && reply != nil && *reply != "" {
			name := "Muse"
			if *author == "dot-web" {
				name = "Dot"
			}
			lines = append(lines, sharedLine(name, *reply))
		}
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	const heading = "Recent Muse + Dot conversation (quoted for reference):\n"
	const latest = "\n\nOwner’s new message:\n"
	remaining := min(8192, 16384-len(body)-len(heading)-len(latest))
	var kept []string
	for _, line := range slices.Backward(lines) {
		if len(line)+1 > remaining {
			break
		}
		kept = append(kept, line)
		remaining -= len(line) + 1
	}
	if len(kept) == 0 {
		return body, nil
	}
	slices.Reverse(kept)
	return heading + strings.Join(kept, "\n") + latest + body, nil
}

func sharedLine(author, text string) string {
	// Keep each speaker on one attributed line and truncate on a rune boundary.
	s := strings.Join(strings.Fields(text), " ")
	if len(s) > 1100 {
		n := 1100
		for !utf8.RuneStart(s[n]) {
			n--
		}
		s = s[:n] + "…"
	}
	return "> " + author + ": " + s
}
