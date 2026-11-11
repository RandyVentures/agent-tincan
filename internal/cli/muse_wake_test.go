package cli

import (
	"context"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mvanhorn/agent-tincan/internal/client"
	"github.com/mvanhorn/agent-tincan/internal/envelope"
	"github.com/mvanhorn/agent-tincan/internal/policy"
	"github.com/mvanhorn/agent-tincan/internal/relay"
	"github.com/mvanhorn/agent-tincan/internal/testrelay"
)

// A fresh-shell restart must recover queued work after 407 without rejoining
// or asking the sender to send it again. Rearming after a reply takes only
// the next request, and never persists either shell's proxy password.
func TestMuseWaitFreshShellRecoversAfter407(t *testing.T) {
	m := testrelay.New(t, relay.Config{})
	proxy := forwardProxy(t, "fresh-fixture-password")
	pu, err := url.Parse(proxy.URL)
	if err != nil {
		t.Fatal(err)
	}
	useConfig(t, client.Config{Relay: m.URL("muse"), Agent: "muse", Proxy: proxy.URL, ProxyCredentialsFromEnv: true})
	for _, key := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy", "TINCAN_SESSION", "TINCAN_PROJECT"} {
		t.Setenv(key, "")
	}
	// As on Muse, credentials come from the same host at the public proxy's
	// port, while the saved proxy keeps the tailnet route's port.
	t.Setenv("HTTPS_PROXY", "http://fixture:expired-fixture-password@"+pu.Hostname()+":3128")
	sender := m.Client(t, "instinct")
	first, err := sender.Send(t.Context(), "muse", "first transport check", envelope.KindAsk, "", false)
	if err != nil {
		t.Fatal(err)
	}
	out, err := run(t, waitCmd(), "--timeout", "5s")
	if !client.IsProxyAuth(err) || strings.Contains(out+err.Error(), "expired-fixture-password") {
		t.Fatalf("expired wait: output %q, error %v", out, err)
	}
	res, err := sender.Get(t.Context(), first.ID, 0)
	if err != nil || res.Status != envelope.StatusQueued {
		t.Fatalf("407 lost or claimed work: %+v, %v", res, err)
	}
	// Starting another command in a fresh shell supplies new credentials.
	t.Setenv("HTTPS_PROXY", "http://fixture:fresh-fixture-password@"+pu.Hostname()+":3128")
	out, err = run(t, waitCmd(), "--timeout", "5s")
	if err != nil || !strings.Contains(out, first.ID) {
		t.Fatalf("fresh wait: output %q, error %v", out, err)
	}
	if _, err := run(t, replyCmd(), first.ID, "TINCAN_MUSE_OK"); err != nil {
		t.Fatal(err)
	}
	res, err = sender.Get(t.Context(), first.ID, 0)
	if err != nil || res.Status != envelope.StatusAnswered || res.Reply == nil || res.Reply.Body != "TINCAN_MUSE_OK" {
		t.Fatalf("reply: %+v, %v", res, err)
	}
	second, err := sender.Send(t.Context(), "muse", "second transport check", envelope.KindAsk, "", false)
	if err != nil {
		t.Fatal(err)
	}
	out, err = run(t, waitCmd(), "--timeout", "5s")
	if err != nil || !strings.Contains(out, second.ID) || strings.Contains(out, first.ID) {
		t.Fatalf("rearmed wait: output %q, error %v", out, err)
	}
	raw, err := os.ReadFile(client.ConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	for _, password := range []string{"expired-fixture-password", "fresh-fixture-password"} {
		if strings.Contains(string(raw)+out, password) {
			t.Fatal("wait saved or printed a proxy password")
		}
	}
}

// An owner taking longer than the pilot wait must not expose held work or
// lose it. Approval after timeout queues the original ask for a new wait.
func TestMuseWaitCanRestartAfterApprovalOutlastsTimeout(t *testing.T) {
	m := testrelay.New(t, relay.Config{})
	path := filepath.Join(t.TempDir(), "approval.json")
	if err := os.WriteFile(path, []byte(`{"gate":{"muse":{"from":"*"}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	approval, err := policy.LoadApproval(path)
	if err != nil {
		t.Fatal(err)
	}
	m.Server.SetPreparer(policy.New(m.Store, policy.Config{Approval: approval}))
	useConfig(t, client.Config{Relay: m.URL("muse"), Agent: "muse"})
	sender := m.Client(t, "instinct")
	req, err := sender.Send(t.Context(), "muse", "held transport check", envelope.KindAsk, "", false)
	if err != nil || req.Status != envelope.StatusHeld {
		t.Fatalf("send: %+v, %v", req, err)
	}
	out, err := run(t, waitCmd(), "--timeout", "100ms")
	if !errors.Is(err, context.DeadlineExceeded) || strings.Contains(out, req.ID) || strings.Contains(out, "held transport check") {
		t.Fatalf("held wait: output %q, error %v", out, err)
	}
	res, err := sender.Get(t.Context(), req.ID, 0)
	if err != nil || res.Status != envelope.StatusHeld {
		t.Fatalf("timeout changed hold: %+v, %v", res, err)
	}
	if _, err := run(t, approvalCmd("approve"), req.ID, "--relay", m.URL("admin")); err != nil {
		t.Fatal(err)
	}
	out, err = run(t, waitCmd(), "--timeout", "5s")
	if err != nil || !strings.Contains(out, req.ID) || !strings.Contains(out, "held transport check") {
		t.Fatalf("restart after approval: output %q, error %v", out, err)
	}
	res, err = sender.Get(t.Context(), req.ID, 0)
	if err != nil || res.Status != envelope.StatusClaimed {
		t.Fatalf("restarted wait did not claim original ask: %+v, %v", res, err)
	}
	if held, err := m.Store.Held(t.Context()); err != nil || len(held) != 0 {
		t.Fatalf("held after approval: %+v, %v", held, err)
	}
}
