package ownerweb_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/mvanhorn/agent-tincan/internal/client"
	"github.com/mvanhorn/agent-tincan/internal/envelope"
	"github.com/mvanhorn/agent-tincan/internal/ownerweb"
	"github.com/mvanhorn/agent-tincan/internal/policy"
	"github.com/mvanhorn/agent-tincan/internal/relay"
	"github.com/mvanhorn/agent-tincan/internal/testrelay"
	"golang.org/x/crypto/bcrypt"
)

const testPassword = "fixture-only-owner-passphrase"

type webEnv struct {
	m       *testrelay.Mesh
	app     *ownerweb.Server
	ts      *httptest.Server
	http    *http.Client
	csrf    string
	backend *ownerweb.RelayBackend
	cfg     ownerweb.Config
}

func webSetup(t *testing.T) *webEnv {
	return webSetupWith(t, nil)
}

func webSetupWith(t *testing.T, wrap func(ownerweb.Backend) ownerweb.Backend) *webEnv {
	t.Helper()
	m := testrelay.New(t, relay.Config{})
	p := filepath.Join(t.TempDir(), "approval.json")
	if err := os.WriteFile(p, []byte(`{"gate":{"muse":{"from":"*"}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	a, err := policy.LoadApproval(p)
	if err != nil {
		t.Fatal(err)
	}
	m.Server.SetPreparer(policy.New(m.Store, policy.Config{Approval: a}))
	backend, err := m.Server.OwnerWebBackend(t.Context(), "owner-web", []string{"muse"})
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := ownerweb.NewLedger(m.Store.DB())
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewUnstartedServer(nil)
	if os.Getenv("TINCAN_OWNER_WEB_PREVIEW") == "1" && os.Getenv("TINCAN_OWNER_WEB_PREVIEW_ADDR") != "" {
		host, _, err := net.SplitHostPort(os.Getenv("TINCAN_OWNER_WEB_PREVIEW_ADDR"))
		if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
			t.Fatal("manual preview must bind only a literal loopback address")
		}
		ts.Listener.Close()
		listener, err := net.Listen("tcp", os.Getenv("TINCAN_OWNER_WEB_PREVIEW_ADDR"))
		if err != nil {
			t.Fatal(err)
		}
		ts.Listener = listener
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(testPassword), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	cfg := ownerweb.Config{Listen: "127.0.0.1:0", PublicURL: "http://" + ts.Listener.Addr().String(), Agent: "owner-web", Targets: []string{"muse"}, PasswordHash: string(hash), Dev: true}
	var scoped ownerweb.Backend = backend
	if wrap != nil {
		scoped = wrap(scoped)
	}
	app, err := ownerweb.New(cfg, scoped, ledger)
	if err != nil {
		ts.Close()
		t.Fatal(err)
	}
	ts.Config.Handler = app
	if os.Getenv("TINCAN_OWNER_WEB_PREVIEW") == "1" {
		ts.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Tincan-Test-Fixture", "1")
			app.ServeHTTP(w, r)
		})
	}
	ts.Start()
	t.Cleanup(ts.Close)
	jar, _ := cookiejar.New(nil)
	return &webEnv{m: m, app: app, ts: ts, http: &http.Client{Jar: jar}, backend: backend, cfg: cfg}
}

type acceptedThenDisconnected struct {
	ownerweb.Backend
	calls atomic.Int32
}

func (b *acceptedThenDisconnected) Send(ctx context.Context, to, body string) (envelope.SendResponse, error) {
	b.calls.Add(1)
	r, err := b.Backend.Send(ctx, to, body)
	if err != nil {
		return r, err
	}
	return r, context.DeadlineExceeded
}

func TestOwnerWebDoesNotResendAfterUncertainDelivery(t *testing.T) {
	var fault *acceptedThenDisconnected
	e := webSetupWith(t, func(b ownerweb.Backend) ownerweb.Backend { fault = &acceptedThenDisconnected{Backend: b}; return fault })
	e.login(t)
	in := map[string]string{"submission_key": "owner-web-uncertain-key-001", "target": "muse", "body": "Do not send this twice"}
	code, raw, _ := e.request(t, "POST", "/api/requests", in)
	if code != 502 || !strings.Contains(string(raw), `"uncertain":true`) {
		t.Fatalf("uncertain response = %d %s", code, raw)
	}
	code, _, _ = e.request(t, "POST", "/api/requests", in)
	if code != 409 {
		t.Fatalf("uncertain retry = %d", code)
	}
	if fault.calls.Load() != 1 {
		t.Fatalf("forwarded %d times", fault.calls.Load())
	}
	items, err := e.backend.List(t.Context())
	if err != nil || len(items) != 1 || items[0].Request.Body != in["body"] {
		t.Fatalf("accepted request missing from history: %+v %v", items, err)
	}
}

func (e *webEnv) request(t *testing.T, method, path string, body any) (int, []byte, *http.Response) {
	t.Helper()
	var data []byte
	if body != nil {
		var err error
		data, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	r, err := http.NewRequestWithContext(t.Context(), method, e.ts.URL+path, bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if method == "POST" {
		r.Header.Set("Origin", e.ts.URL)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Tincan-UI", "1")
		r.Header.Set("X-CSRF-Token", e.csrf)
	}
	resp, err := e.http.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, raw, resp
}

func (e *webEnv) login(t *testing.T) {
	t.Helper()
	code, raw, _ := e.request(t, "POST", "/api/login", map[string]string{"password": testPassword})
	if code != 200 {
		t.Fatalf("login %d: %s", code, raw)
	}
	var out struct {
		CSRF string `json:"csrf"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.CSRF == "" {
		t.Fatalf("login token missing: %v", err)
	}
	e.csrf = out.CSRF
}

func (e *webEnv) send(t *testing.T, key, body string) envelope.SendResponse {
	t.Helper()
	code, raw, _ := e.request(t, "POST", "/api/requests", map[string]string{"submission_key": key, "target": "muse", "body": body})
	if code != 201 {
		t.Fatalf("send %d: %s", code, raw)
	}
	var out envelope.SendResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestOwnerWebRealRelayRoundTripAndApprovalBoundary(t *testing.T) {
	e := webSetup(t)
	e.login(t)
	// Another sender's request must never enter this owner's workspace.
	other, err := e.m.Client(t, "instinct").Send(t.Context(), "muse", "another sender's private task", envelope.KindAsk, "", false)
	if err != nil {
		t.Fatal(err)
	}
	code, _, _ := e.request(t, "GET", "/api/requests/"+other.ID, nil)
	if code != 404 {
		t.Fatalf("other sender request = %d", code)
	}
	code, raw, _ := e.request(t, "GET", "/api/requests", nil)
	if code != 200 || strings.Contains(string(raw), "private task") {
		_, backendErr := e.backend.List(t.Context())
		t.Logf("test relay list error: %v", backendErr)
		t.Fatalf("list leaked other sender: %d %s", code, raw)
	}
	body := "Reply exactly OWNER_WEB_TEST_OK. " + strings.Repeat("Review all of this text. ", 20) + "End of the full request."
	sent := e.send(t, "owner-web-unique-submission-001", body)
	if sent.Status != envelope.StatusHeld {
		t.Fatalf("send did not preserve gate: %+v", sent)
	}
	in, err := e.m.Client(t, "muse").Poll(t.Context(), 0)
	if err != nil || len(in.Requests) != 0 {
		t.Fatalf("held work reached Muse: %+v %v", in, err)
	}
	// CSRF alone cannot approve without a fresh per-request review.
	code, _, _ = e.request(t, "POST", "/api/requests/"+sent.ID+"/decision", map[string]string{"decision": "approve", "review_token": "invented"})
	if code != 409 {
		t.Fatalf("approval without review = %d", code)
	}
	code, raw, _ = e.request(t, "POST", "/api/requests/"+sent.ID+"/review", map[string]string{})
	if code != 200 {
		t.Fatalf("review %d %s", code, raw)
	}
	var review struct {
		Request envelope.Request `json:"request"`
		Approve string           `json:"approve_token"`
		Deny    string           `json:"deny_token"`
	}
	if err := json.Unmarshal(raw, &review); err != nil {
		t.Fatal(err)
	}
	if review.Request.Body != body {
		t.Fatal("approval did not show the full request")
	}
	code, _, _ = e.request(t, "POST", "/api/requests/"+sent.ID+"/decision", map[string]string{"decision": "approve", "review_token": review.Approve})
	if code != 200 {
		t.Fatalf("approve = %d", code)
	}
	code, _, _ = e.request(t, "POST", "/api/requests/"+sent.ID+"/decision", map[string]string{"decision": "approve", "review_token": review.Approve})
	if code != 409 {
		t.Fatalf("approval replay = %d", code)
	}
	in, err = e.m.Client(t, "muse").Poll(t.Context(), 0)
	if err != nil || len(in.Requests) != 1 || in.Requests[0].ID != sent.ID {
		t.Fatalf("delivery = %+v %v", in, err)
	}
	if _, err := e.m.Client(t, "muse").Claim(t.Context(), sent.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.Client(t, "muse").Reply(t.Context(), sent.ID, "OWNER_WEB_TEST_OK", envelope.StatusAnswered); err != nil {
		t.Fatal(err)
	}
	code, raw, _ = e.request(t, "GET", "/api/requests/"+sent.ID, nil)
	if code != 200 {
		t.Fatalf("get = %d %s", code, raw)
	}
	var result client.Result
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if result.Status != envelope.StatusAnswered || result.Reply == nil || result.Reply.Body != "OWNER_WEB_TEST_OK" {
		t.Fatalf("result = %+v", result)
	}
	// A duplicate browser submit returns the original request, not a second ask.
	code, raw, _ = e.request(t, "POST", "/api/requests", map[string]string{"submission_key": "owner-web-unique-submission-001", "target": "muse", "body": body})
	if code != 200 || !strings.Contains(string(raw), sent.ID) {
		t.Fatalf("duplicate = %d %s", code, raw)
	}
	code, _, _ = e.request(t, "POST", "/api/requests", map[string]string{"submission_key": "owner-web-unique-submission-001", "target": "muse", "body": "different"})
	if code != 409 {
		t.Fatalf("changed duplicate = %d", code)
	}
	// The virtual phone cannot take incoming work or use admin capabilities.
	if _, err := e.m.Client(t, "instinct").Send(t.Context(), "owner-web", "not an agent worker", envelope.KindAsk, "", false); err == nil {
		t.Fatal("owner identity accepted incoming work")
	}
	if err := e.backend.Client.Raw(t.Context(), "GET", "/v1/admin/held", nil, new(any)); !client.IsStatus(err, 403) {
		t.Fatalf("owner agent is admin: %v", err)
	}
}

func TestOwnerWebAuthenticationAndScope(t *testing.T) {
	e := webSetup(t)
	for _, path := range []string{"/api/session", "/api/requests", "/api/targets", "/api/admin/invite"} {
		code, _, _ := e.request(t, "GET", path, nil)
		if code != 401 {
			t.Fatalf("anonymous %s = %d", path, code)
		}
	}
	code, raw, resp := e.request(t, "GET", "/", nil)
	if code != 200 || !strings.Contains(string(raw), "Sign in") {
		t.Fatalf("login page = %d", code)
	}
	if resp.Header.Get("Content-Security-Policy") == "" || resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatal("missing response protection")
	}
	e.login(t)
	code, _, _ = e.request(t, "POST", "/api/admin/invite", map[string]string{})
	if code != 404 {
		t.Fatalf("raw admin operation = %d", code)
	}
	code, _, _ = e.request(t, "POST", "/api/requests", map[string]string{"submission_key": "owner-web-scope-submission-001", "target": "instinct", "body": "no"})
	if code != 400 {
		t.Fatalf("non-allowlisted target = %d", code)
	}
	code, _, _ = e.request(t, "POST", "/api/requests", map[string]string{"submission_key": "owner-web-scope-submission-001", "target": "muse", "body": "no", "from": "instinct"})
	if code != 400 {
		t.Fatalf("caller-supplied identity = %d", code)
	}
	csrf := e.csrf
	e.csrf = ""
	code, _, _ = e.request(t, "POST", "/api/logout", map[string]string{})
	if code != 403 {
		t.Fatalf("missing CSRF = %d", code)
	}
	e.csrf = csrf
	code, _, _ = e.request(t, "POST", "/api/logout", map[string]string{})
	if code != 200 {
		t.Fatalf("logout = %d", code)
	}
	code, _, _ = e.request(t, "GET", "/api/session", nil)
	if code != 401 {
		t.Fatalf("logged out session = %d", code)
	}
}

func TestOwnerWebRejectsCrossOriginAndRebinding(t *testing.T) {
	e := webSetup(t)
	for _, tc := range []struct{ host, origin, contentType, fetchSite string }{
		{"attacker.invalid", e.ts.URL, "application/json", "same-origin"},
		{"", "https://attacker.invalid", "application/json", "cross-site"},
		{"", e.ts.URL, "application/x-www-form-urlencoded", "same-origin"},
		{"", "", "application/json", ""},
	} {
		r := httptest.NewRequest("POST", e.ts.URL+"/api/login", strings.NewReader(`{"password":"`+testPassword+`"}`))
		if tc.host != "" {
			r.Host = tc.host
		}
		r.Header.Set("Origin", tc.origin)
		r.Header.Set("Content-Type", tc.contentType)
		r.Header.Set("Sec-Fetch-Site", tc.fetchSite)
		r.Header.Set("X-Tincan-UI", "1")
		w := httptest.NewRecorder()
		e.app.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatalf("cross origin %+v = %d", tc, w.Code)
		}
	}
}

func TestOwnerWebReviewBoundToSessionActionAndRequest(t *testing.T) {
	e := webSetup(t)
	e.login(t)
	sent := e.send(t, "owner-web-review-submission-001", "review this exact text")
	code, raw, _ := e.request(t, "POST", "/api/requests/"+sent.ID+"/review", map[string]string{})
	if code != 200 {
		t.Fatal(string(raw))
	}
	var review map[string]any
	json.Unmarshal(raw, &review)
	jar, _ := cookiejar.New(nil)
	firstClient, firstCSRF := e.http, e.csrf
	e.http = &http.Client{Jar: jar}
	e.login(t)
	code, _, _ = e.request(t, "POST", "/api/requests/"+sent.ID+"/decision", map[string]string{"decision": "approve", "review_token": review["approve_token"].(string)})
	if code != 409 {
		t.Fatalf("cross-session review = %d", code)
	}
	e.http, e.csrf = firstClient, firstCSRF
	code, _, _ = e.request(t, "POST", "/api/requests/"+sent.ID+"/decision", map[string]string{"decision": "deny", "review_token": review["approve_token"].(string)})
	if code != 409 {
		t.Fatalf("wrong-action token = %d", code)
	}
	code, raw, _ = e.request(t, "POST", "/api/requests/"+sent.ID+"/review", map[string]string{})
	if code != 200 {
		t.Fatal(string(raw))
	}
	json.Unmarshal(raw, &review)
	code, _, _ = e.request(t, "POST", "/api/requests/"+sent.ID+"/decision", map[string]string{"decision": "deny", "review_token": review["deny_token"].(string)})
	if code != 200 {
		t.Fatalf("deny = %d", code)
	}
	res, err := e.backend.Get(context.Background(), sent.ID)
	if err != nil || res.Status != envelope.StatusDeclined {
		t.Fatalf("denied = %+v %v", res, err)
	}
}

func TestOwnerWebLoginRateLimit(t *testing.T) {
	e := webSetup(t)
	for i := range 6 {
		code, _, _ := e.request(t, "POST", "/api/login", map[string]string{"password": "wrong"})
		want := 401
		if i == 5 {
			want = 429
		}
		if code != want {
			t.Fatalf("attempt %d = %d, want %d", i, code, want)
		}
	}
}

func TestOwnerWebNeverTakesExistingAgentName(t *testing.T) {
	m := testrelay.New(t, relay.Config{})
	if _, err := m.Server.OwnerWebBackend(t.Context(), "muse", []string{"instinct"}); err == nil {
		t.Fatal("took existing real agent name")
	}
	a, ok, err := m.Dir.Agent(t.Context(), "muse")
	if err != nil || !ok || a.NodeID == "virtual:muse" {
		t.Fatalf("Muse was changed: %+v %v", a, err)
	}
	if _, err := m.Server.OwnerWebBackend(t.Context(), "owner-web", []string{"muse"}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Server.OwnerWebBackend(t.Context(), "owner-web", []string{"muse"}); err != nil {
		t.Fatal(fmt.Errorf("restart with same owner identity: %w", err))
	}
}
