//go:build ownerwebpreview

package ownerweb_test

import (
	"context"
	"encoding/json"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/mvanhorn/agent-tincan/internal/envelope"
)

// Explicit development fixture: production authentication and the real local
// relay, with a scripted test agent. This never connects to external Muse.
func TestOwnerWebInteractivePreview(t *testing.T) {
	if os.Getenv("TINCAN_OWNER_WEB_PREVIEW") != "1" {
		t.Skip("manual local integration preview only")
	}
	e := webSetup(t)
	path := os.Getenv("TINCAN_OWNER_WEB_PREVIEW_INFO")
	if path == "" {
		t.Fatal("set a task-local preview info path")
	}
	info, _ := json.MarshalIndent(map[string]any{"url": e.ts.URL, "password": testPassword, "fixture_only": true, "real_local_relay": true, "external_muse_connected": false}, "", "  ")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(append(info, '\n')); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 45*time.Minute)
	defer cancel()
	var wg sync.WaitGroup
	wg.Go(func() {
		ticker := time.NewTicker(300 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				in, err := e.m.Client(t, "muse").Poll(ctx, 0)
				if err != nil {
					continue
				}
				for _, req := range in.Requests {
					if _, err := e.m.Client(t, "muse").Claim(ctx, req.ID); err != nil {
						continue
					}
					e.m.Client(t, "muse").Reply(ctx, req.ID, "LOCAL RELAY TEST: Your request passed authentication, the approval gate, delivery and reply storage. This answer is from a scripted test agent; external Muse is not connected.", envelope.StatusAnswered)
				}
			}
		}
	})
	t.Logf("Local integration preview ready at %s; external Muse is NOT connected", e.ts.URL)
	<-ctx.Done()
	cancel()
	wg.Wait()
}
