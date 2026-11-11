package ownerweb

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/mvanhorn/agent-tincan/internal/envelope"
	_ "modernc.org/sqlite"
)

func ledgerDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	return db
}

func TestLedgerUncertainAndPendingSurviveRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.db")
	db := ledgerDB(t, path)
	l, err := NewLedger(db)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"uncertain-key", "interrupted-key"} {
		fresh, _, err := l.claim(t.Context(), key, "muse", "private request text")
		if err != nil || !fresh {
			t.Fatalf("claim %s: %v", key, err)
		}
	}
	if err := l.finish(t.Context(), "uncertain-key", envelope.SendResponse{}, context.DeadlineExceeded); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewLedger(ledgerDB(t, path))
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"uncertain-key", "interrupted-key"} {
		fresh, _, err := reopened.claim(t.Context(), key, "muse", "private request text")
		if fresh || err == nil {
			t.Fatalf("restart allowed resend of %s", key)
		}
	}
}

func TestLedgerConcurrentSubmitHasOneSender(t *testing.T) {
	l, err := NewLedger(ledgerDB(t, ":memory:"))
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			fresh, _, _ := l.claim(t.Context(), "one-submission", "muse", "same request")
			if fresh {
				calls.Add(1)
			}
		})
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("concurrent senders = %d", calls.Load())
	}
}

func TestLedgerSuccessfulResultSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.db")
	db := ledgerDB(t, path)
	l, err := NewLedger(db)
	if err != nil {
		t.Fatal(err)
	}
	fresh, _, err := l.claim(t.Context(), "key", "muse", "request")
	if err != nil || !fresh {
		t.Fatal(err)
	}
	result := envelope.SendResponse{Request: envelope.Request{ID: "original-id", To: "muse", Body: "request", Status: envelope.StatusHeld}}
	if err := l.finish(t.Context(), "key", result, nil); err != nil {
		t.Fatal(err)
	}
	db.Close()
	l, err = NewLedger(ledgerDB(t, path))
	if err != nil {
		t.Fatal(err)
	}
	fresh, got, err := l.claim(t.Context(), "key", "muse", "request")
	if err != nil || fresh || got.ID != "original-id" {
		t.Fatalf("duplicate = %v %+v %v", fresh, got, err)
	}
	if _, _, err := l.claim(t.Context(), "key", "muse", "changed"); err == nil {
		t.Fatal("key accepted different body")
	}
	if _, err := NewLedger(nil); err == nil {
		t.Fatal("nil ledger accepted")
	}
}
