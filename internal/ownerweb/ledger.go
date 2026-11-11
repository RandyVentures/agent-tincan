package ownerweb

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/mvanhorn/agent-tincan/internal/envelope"
)

// Submission keys survive service restarts. A pending or uncertain send is
// never automatically repeated: the relay may have accepted it before a
// connection failed. This adds no retries to the existing send protocol.
type Ledger struct{ db *sql.DB }

func NewLedger(db *sql.DB) (*Ledger, error) {
	if db == nil {
		return nil, errors.New("owner web requires a persistent submission ledger")
	}
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS owner_web_submissions (
 key TEXT PRIMARY KEY, body_hash TEXT NOT NULL, state TEXT NOT NULL,
 result_json TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL)`)
	return &Ledger{db: db}, err
}

func digest(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }

func (l *Ledger) claim(ctx context.Context, key, target, body string) (fresh bool, previous envelope.SendResponse, err error) {
	hash := digest(target + "\x00" + body)
	res, err := l.db.ExecContext(ctx, `INSERT INTO owner_web_submissions(key,body_hash,state,created_at) VALUES(?,?,'pending',?) ON CONFLICT(key) DO NOTHING`, key, hash, time.Now().UnixMilli())
	if err != nil {
		return false, previous, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, previous, err
	}
	if n == 1 {
		return true, previous, nil
	}
	var stored, state, raw string
	if err := l.db.QueryRowContext(ctx, `SELECT body_hash,state,result_json FROM owner_web_submissions WHERE key=?`, key).Scan(&stored, &state, &raw); err != nil {
		return false, previous, err
	}
	if stored != hash {
		return false, previous, errors.New("submission key was already used for different text")
	}
	if state != "sent" {
		return false, previous, errors.New("delivery is uncertain; check your request history before starting a new request")
	}
	err = json.Unmarshal([]byte(raw), &previous)
	return false, previous, err
}

func (l *Ledger) finish(ctx context.Context, key string, result envelope.SendResponse, sendErr error) error {
	state, raw := "uncertain", ""
	if sendErr == nil {
		state = "sent"
		data, err := json.Marshal(result)
		if err != nil {
			return err
		}
		raw = string(data)
	}
	_, err := l.db.ExecContext(ctx, `UPDATE owner_web_submissions SET state=?,result_json=? WHERE key=?`, state, raw, key)
	return err
}
