package store

import (
	"testing"
	"time"

	"github.com/mvanhorn/agent-tincan/internal/envelope"
)

func TestOwnerDeliveryUsesOnlyThisOwnersAsksToThisAgent(t *testing.T) {
	s, clock := open(t, ":memory:")
	ctx := t.Context()
	finish := func(owner, agent string, status envelope.Status, body string) {
		t.Helper()
		req := ask(t, s, owner, agent, "private request")
		if _, err := s.Claim(ctx, req.ID, agent, time.Minute); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Reply(ctx, req.ID, agent, envelope.Reply{Body: body, Status: status}); err != nil {
			t.Fatal(err)
		}
		clock.advance(time.Minute)
	}
	tt := clock.now()
	finish("owner", "dot", envelope.StatusFailed, "Sorry, source unavailable: dots: the send button stayed disabled (private diagnostic)")
	finish("owner", "dot", envelope.StatusAnswered, "private reply")
	finish("other", "dot", envelope.StatusFailed, "unrelated")
	finish("owner", "muse", envelope.StatusFailed, "unrelated")
	for range 22 {
		ask(t, s, "owner", "muse", "newer request")
	}
	d, err := s.OwnerDelivery(ctx, "owner", "dot")
	if err != nil || d.Problem != "editor" || !d.LastProblem.Equal(tt) || !d.LastReply.Equal(tt.Add(time.Minute)) {
		t.Fatalf("wrong observations: %+v, %v", d, err)
	}
	empty, err := s.OwnerDelivery(ctx, "another-owner", "dot")
	if err != nil || empty != (OwnerDelivery{}) {
		t.Fatalf("unrelated history: %+v, %v", empty, err)
	}
	// A subsequent expiry is a delivery problem even when there is no reply row.
	ask(t, s, "owner", "dot", "will expire")
	clock.advance(2 * time.Hour)
	if _, err := s.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	d, err = s.OwnerDelivery(ctx, "owner", "dot")
	if err != nil || d.Problem != "expired" || !d.LastProblem.Equal(clock.now()) || !d.LastReply.Equal(tt.Add(time.Minute)) {
		t.Fatalf("expired request: %+v, %v", d, err)
	}

}
