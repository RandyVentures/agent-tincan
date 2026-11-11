package relay

import (
	"context"
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/mvanhorn/agent-tincan/internal/client"
	"github.com/mvanhorn/agent-tincan/internal/identity"
	"github.com/mvanhorn/agent-tincan/internal/ownerweb"
	"github.com/mvanhorn/agent-tincan/internal/store"
)

// OwnerWebBackend is called only during explicit, authenticated owner-web
// startup. The ask-only identity is internal, as for the OAuth gateway; it
// receives no admin rights. Admin decisions use a separate in-process client,
// available only to the owner's authenticated, narrowly scoped web service.
func (s *Server) OwnerWebBackend(ctx context.Context, name string, targets []string) (*ownerweb.RelayBackend, error) {
	created, err := s.dir.BindOwnerWeb(ctx, name)
	if err != nil {
		return nil, err
	}
	if created {
		s.record(ctx, "join", "", "", name, store.DetailJSON(map[string]any{"via": "owner-web", "node": identity.VirtualAddr(name)}))
	}
	ask := client.NewRelayHTTP("http://tincan-relay", &http.Client{Transport: ownerTransport{s.Handler(), identity.VirtualAddr(name)}, Timeout: 15 * time.Second})
	admin := client.NewRelayHTTP("http://tincan-relay", &http.Client{Transport: ownerTransport{s.AdminHandler(), ""}, Timeout: 15 * time.Second})
	return &ownerweb.RelayBackend{Agent: name, Targets: targets, Client: ask, Admin: admin}, nil
}

type ownerTransport struct {
	handler http.Handler
	address string
}

func (t ownerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.RemoteAddr = t.address
	rec := httptest.NewRecorder()
	t.handler.ServeHTTP(rec, r)
	return rec.Result(), nil
}
