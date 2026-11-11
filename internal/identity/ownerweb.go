package identity

import (
	"context"
	"fmt"
)

// BindOwnerWeb reserves an internal ask-only phone identity. Unlike the
// general gateway bind, it never changes an existing identity's kind or
// binding. Check and write share the same lock as invites and joins.
func (d *Directory) BindOwnerWeb(ctx context.Context, name string) (bool, error) {
	if !nameRE.MatchString(name) {
		return false, ErrUnknownAgent
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	a, exists, err := d.Agent(ctx, name)
	if err != nil {
		return false, err
	}
	if exists {
		if a.NodeID == VirtualAddr(name) && a.Kind == "phone" && !a.Guest.Set() {
			return false, nil
		}
		return false, fmt.Errorf("owner web name %q: %w", name, ErrNameTaken)
	}
	err = d.store.PutAgent(ctx, Agent{Name: name, NodeID: VirtualAddr(name), NodeName: VirtualAddr(name), JoinedAt: d.cfg.Now(), Kind: "phone"})
	return err == nil, err
}
