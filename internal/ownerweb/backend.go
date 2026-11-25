package ownerweb

import (
	"context"
	"errors"
	"net/url"
	"slices"
	"time"

	"github.com/mvanhorn/agent-tincan/internal/client"
	"github.com/mvanhorn/agent-tincan/internal/envelope"
	"github.com/mvanhorn/agent-tincan/internal/store"
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
	SendGrouped(context.Context, string, string, string) (envelope.SendResponse, error)
	Decide(context.Context, string, string) error
}

type Target struct {
	Delivery         *store.OwnerDelivery `json:"delivery,omitempty"`
	BrowserSignedOut bool                 `json:"browser_signed_out,omitempty"`
	LimitedUntil     time.Time            `json:"limited_until,omitzero"`
	Name             string               `json:"name"`
	Status           string               `json:"status"`
	Online           bool                 `json:"online"`
	Wake             string               `json:"wake"`
}

type RelayBackend struct {
	DirectIDs       func(context.Context, string) ([]string, error)
	DeliveryHistory func(context.Context, string, string) (store.OwnerDelivery, error)
	Agent           string
	Targets         []string
	Client          *client.Relay
	Admin           *client.Relay
}

func (b *RelayBackend) allowed(target string) bool {
	return slices.Contains(b.Targets, target)
}

func (b *RelayBackend) Agents(ctx context.Context) ([]Target, error) {
	agents, err := b.Client.Agents(ctx)
	if err != nil {
		return nil, err
	}
	out := []Target{}
	for _, a := range agents {
		if b.allowed(a.Name) {
			target := Target{Name: a.Name, Status: a.Status, Online: a.Online, Wake: a.Wake, BrowserSignedOut: a.SignedOutSite != "", LimitedUntil: a.LimitedUntil}
			// Keep the conversation usable when history is unavailable.
			if b.DeliveryHistory != nil {
				if delivery, err := b.DeliveryHistory(ctx, b.Agent, a.Name); err == nil {
					target.Delivery = &delivery
				}
			}
			out = append(out, target)
		}
	}
	return out, nil
}

func (b *RelayBackend) List(ctx context.Context) ([]client.Result, error) {
	ids, err := b.DirectIDs(ctx, b.Agent)
	if err != nil {
		return nil, err
	}
	items := []client.Result{}
	for _, id := range ids {
		full, err := b.Get(ctx, id)
		if errors.Is(err, ErrNotFound) {
			continue
		}
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
	return b.SendGrouped(ctx, target, body, "")
}

func (b *RelayBackend) SendGrouped(ctx context.Context, target, body, group string) (envelope.SendResponse, error) {
	if !b.allowed(target) {
		return envelope.SendResponse{}, ErrNotFound
	}
	return b.Client.SendGrouped(ctx, target, body, envelope.KindAsk, "", group)
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
