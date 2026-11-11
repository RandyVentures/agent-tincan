package ownerweb

import (
	"context"
	"errors"
	"net/url"

	"github.com/mvanhorn/agent-tincan/internal/client"
	"github.com/mvanhorn/agent-tincan/internal/envelope"
)

var ErrNotFound = errors.New("request not found")
var ErrNotHeld = errors.New("request is no longer waiting for approval")

// Backend is deliberately small: there is no invite, join, raw proxy,
// arbitrary target, room, file, history, credential or settings operation.
type Backend interface {
	Agents(context.Context) ([]Target, error)
	List(context.Context) ([]client.Result, error)
	Get(context.Context, string) (client.Result, error)
	Send(context.Context, string, string) (envelope.SendResponse, error)
	Decide(context.Context, string, string) error
}

type Target struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Online bool   `json:"online"`
	Wake   string `json:"wake"`
}

type RelayBackend struct {
	Agent   string
	Targets []string
	Client  *client.Relay
	Admin   *client.Relay
}

func (b *RelayBackend) allowed(target string) bool {
	for _, name := range b.Targets {
		if name == target {
			return true
		}
	}
	return false
}

func (b *RelayBackend) Agents(ctx context.Context) ([]Target, error) {
	agents, err := b.Client.Agents(ctx)
	if err != nil {
		return nil, err
	}
	out := []Target{}
	for _, a := range agents {
		if b.allowed(a.Name) {
			out = append(out, Target{Name: a.Name, Status: a.Status, Online: a.Online, Wake: a.Wake})
		}
	}
	return out, nil
}

func (b *RelayBackend) List(ctx context.Context) ([]client.Result, error) {
	var out struct {
		Traces []client.Result `json:"traces"`
	}
	// Recent trace listings require an admin device. The server fixes the
	// owner filter here, then independently filters every returned row and
	// reads full bodies with the unprivileged owner client below.
	err := b.Admin.Raw(ctx, "GET", "/v1/trace?limit=20&exclude_pings=true&from="+url.QueryEscape(b.Agent), nil, &out)
	if err != nil {
		return nil, err
	}
	items := []client.Result{}
	for _, item := range out.Traces {
		if item.Request.From != b.Agent || !b.allowed(item.Request.To) {
			continue
		}
		// Trace listings can be previews. Read each own request for its full
		// body, latest state and reply rather than presenting an excerpt as
		// the text the owner is approving.
		full, err := b.Get(ctx, item.Request.ID)
		if err != nil {
			return nil, err
		}
		items = append(items, full)
	}
	return items, nil
}

func (b *RelayBackend) Get(ctx context.Context, id string) (client.Result, error) {
	r, err := b.Client.Get(ctx, id, 0)
	if client.IsStatus(err, 403) || client.IsStatus(err, 404) {
		return r, ErrNotFound
	}
	if err != nil {
		return r, err
	}
	if r.Request.From != b.Agent || !b.allowed(r.Request.To) {
		return client.Result{}, ErrNotFound
	}
	return r, nil
}

func (b *RelayBackend) Send(ctx context.Context, target, body string) (envelope.SendResponse, error) {
	if !b.allowed(target) {
		return envelope.SendResponse{}, ErrNotFound
	}
	return b.Client.SendGrouped(ctx, target, body, envelope.KindAsk, "", "")
}

func (b *RelayBackend) Decide(ctx context.Context, id, decision string) error {
	if decision != "approve" && decision != "deny" {
		return errors.New("invalid decision")
	}
	r, err := b.Get(ctx, id)
	if err != nil {
		return err
	}
	if r.Status != envelope.StatusHeld {
		return ErrNotHeld
	}
	return b.Admin.Raw(ctx, "POST", "/v1/admin/requests/"+url.PathEscape(id)+"/"+decision, map[string]string{}, nil)
}
