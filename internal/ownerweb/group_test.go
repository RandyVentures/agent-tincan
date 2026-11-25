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
