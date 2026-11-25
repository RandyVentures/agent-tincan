package ownerweb_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/mvanhorn/agent-tincan/internal/client"
	"github.com/mvanhorn/agent-tincan/internal/envelope"
	"github.com/mvanhorn/agent-tincan/internal/ownerweb"
)

type groupFault struct {
	ownerweb.Backend
	dotCalls atomic.Int32
}

func (b *groupFault) SendGrouped(ctx context.Context, to, body, group string) (envelope.SendResponse, error) {
	if to == "dot-web" {
		b.dotCalls.Add(1)
		return envelope.SendResponse{}, errors.New("test disconnect")
	}
	return b.Backend.SendGrouped(ctx, to, body, group)
}

func TestGroupRoutingAndHistory(t *testing.T) {
	e := webSetupTargets(t, []string{"muse", "dot-web"}, nil)
	post := map[string]string{"audience": "both", "body": "shared request", "submission_key": "group-test-original-entry"}
	check := func(want int) {
		t.Helper()
		code, raw, _ := e.request(t, "POST", "/api/group", post)
		if code != want {
			t.Fatalf("want %d, got %d: %s", want, code, raw)
		}
	}
	check(401)
	e.login(t)
	csrf := e.csrf
	e.csrf = "wrong"
	check(403)
	e.csrf = csrf
	e.send(t, "private-direct-submission", "this is private")
	check(201)
	check(200)
	post["audience"] = "muse"
	check(409)
	post["submission_key"] = "next-group-post-muse-only"
	post["body"] = "only Muse sees this"
	check(201)
	post["audience"] = "another-agent"
	check(400)
	post["audience"] = "both"
	post["body"] = strings.Repeat("界", 5462)
	check(400)
	in, err := e.m.Client(t, "dot-web").Poll(t.Context(), 0)
	if err != nil || len(in.Requests) != 1 || in.Requests[0].Body != "shared request" {
		t.Fatalf("dot inbox %+v %v", in, err)
	}
	id := in.Requests[0].ID
	if _, err = e.m.Client(t, "dot-web").Claim(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	if _, err = e.m.Client(t, "dot-web").Reply(t.Context(), id, "Dot reply", envelope.StatusAnswered); err != nil {
		t.Fatal(err)
	}
	in, err = e.m.Client(t, "muse").Poll(t.Context(), 0)
	if err != nil || len(in.Requests) != 0 {
		t.Fatalf("Muse gate %+v %v", in, err)
	}
	code, raw, _ := e.request(t, "GET", "/api/requests", nil)
	var data struct {
		Requests []client.Result
		Posts    []struct {
			Audience, Body string
			Requests       []client.Result
		} `json:"group_posts"`
	}
	if err = json.Unmarshal(raw, &data); err != nil || code != 200 {
		t.Fatalf("list: %s, %v", raw, err)
	}
	if len(data.Requests) != 1 || data.Requests[0].Request.Body != "this is private" || len(data.Posts) != 2 {
		t.Fatalf("incorrect partition: %s", raw)
	}
	for _, p := range data.Posts {
		if p.Audience == "both" && len(p.Requests) != 2 || p.Audience == "muse" && len(p.Requests) != 1 {
			t.Fatalf("members: %+v", p)
		}
		for _, r := range p.Requests {
			if r.Request.Body != p.Body || r.Request.To == "muse" && r.Status != envelope.StatusHeld || r.Request.To == "dot-web" && (r.Reply == nil || r.Reply.Body != "Dot reply") {
				t.Fatalf("group result: %+v", r)
			}
		}
	}
}

func TestGroupPartialSendIsNotRetried(t *testing.T) {
	var fault *groupFault
	e := webSetupTargets(t, []string{"muse", "dot-web"}, func(b ownerweb.Backend) ownerweb.Backend { fault = &groupFault{Backend: b}; return fault })
	e.login(t)
	post := map[string]string{"audience": "both", "body": "partial", "submission_key": "group-partial-submission"}
	code, raw, _ := e.request(t, "POST", "/api/group", post)
	if code != 201 || !strings.Contains(string(raw), `"unconfirmed":["dot-web"]`) {
		t.Fatalf("%d %s", code, raw)
	}
	code, _, _ = e.request(t, "POST", "/api/group", post)
	if code != 200 || fault.dotCalls.Load() != 1 {
		t.Fatal("repeated partial send")
	}
	one := webSetup(t)
	one.login(t)
	if code, _, _ = one.request(t, "POST", "/api/group", post); code != 404 {
		t.Fatalf("requires both configured agents: %d", code)
	}
}

func TestSharedThreadOnlySuppliesReleasedSharedContextToSelectedResponders(t *testing.T) {
	e := webSetupTargets(t, []string{"muse", "dot-web"}, nil)
	e.login(t)
	post := func(path, audience, body, key string, want int) {
		t.Helper()
		code, raw, _ := e.request(t, "POST", "/api/"+path, map[string]string{"audience": audience, "body": body, "submission_key": key})
		if code != want || strings.Contains(string(raw), `"unconfirmed":[`) {
			t.Fatalf("%s: %d %s", path, code, raw)
		}
	}
	e.send(t, "private-direct-not-shared", "DIRECT SECRET")
	post("group", "muse", "LEGACY SECRET", "legacy-partition-key-001", 201)
	post("shared", "muse", "PENDING SECRET", "pending-partition-key-001", 201)
	post("shared", "dot-web", "Suggest a landing page.", "first-shared-post-key-01", 201)
	dot := e.m.Client(t, "dot-web")
	in, err := dot.Poll(t.Context(), 0)
	if err != nil || len(in.Requests) != 1 || in.Requests[0].Body != "Suggest a landing page." {
		t.Fatalf("first shared ask: %+v %v", in, err)
	}
	first := in.Requests[0].ID
	if _, err = dot.Claim(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	if _, err = dot.Reply(t.Context(), first, "Lead with a live demo.", envelope.StatusAnswered); err != nil {
		t.Fatal(err)
	}
	otherOwner := "different-owner"
	if _, err = e.m.Store.DB().Exec(`INSERT INTO owner_web_group_posts(owner,post_id,audience,body,created_at) VALUES(?,?,?,?,?)`, otherOwner, "owner-shared-unrelated", "both", "OTHER OWNER SECRET", 1); err != nil {
		t.Fatal(err)
	}
	post("shared", "muse", "What would you change?", "next-shared-post-key-002", 201)
	post("shared", "muse", "What would you change?", "next-shared-post-key-002", 200)
	code, raw, _ := e.request(t, "GET", "/api/requests", nil)
	var data struct {
		Direct []client.Result         `json:"requests"`
		Old    []struct{ Body string } `json:"group_posts"`
		Shared []struct {
			Body     string
			Requests []client.Result
		} `json:"shared_posts"`
	}
	if code != 200 || json.Unmarshal(raw, &data) != nil || len(data.Direct) != 1 || len(data.Old) != 1 || len(data.Shared) != 3 {
		t.Fatalf("history: %s", raw)
	}
	if data.Old[0].Body != "LEGACY SECRET" || data.Direct[0].Request.Body != "DIRECT SECRET" {
		t.Fatal("direct or earlier group misclassified")
	}
	var id string
	for _, p := range data.Shared {
		if p.Body != "What would you change?" {
			continue
		}
		if len(p.Requests) != 1 || p.Requests[0].Request.To != "muse" || p.Requests[0].Status != envelope.StatusHeld {
			t.Fatal("incorrect route or gate")
		}
		id = p.Requests[0].Request.ID
	}
	if id == "" {
		t.Fatal("missing follow-up")
	}
	code, raw, _ = e.request(t, "POST", "/api/requests/"+id+"/review", map[string]string{})
	var review struct{ Request envelope.Request }
	if code != 200 || json.Unmarshal(raw, &review) != nil {
		t.Fatalf("review: %d %s", code, raw)
	}
	body := review.Request.Body
	for _, text := range []string{"Owner: Suggest a landing page.", "Dot: Lead with a live demo.", "What would you change?"} {
		if !strings.Contains(body, text) {
			t.Fatalf("missing %q: %s", text, body)
		}
	}
	if strings.Contains(body, "SECRET") {
		t.Fatalf("private or held material in outgoing context: %s", body)
	}
	in, err = dot.Poll(t.Context(), 0)
	if err != nil || len(in.Requests) != 0 {
		t.Fatal("Dot received another ask or an automatic reply")
	}
	maxBody := strings.Repeat("界", 5461) + "!"
	post("shared", "dot-web", maxBody, "maximum-shared-post-key-03", 201)
	in, err = dot.Poll(t.Context(), 0)
	if err != nil || len(in.Requests) != 1 || in.Requests[0].Body != maxBody {
		t.Fatal("shared context displaced the full-size user message")
	}
}
