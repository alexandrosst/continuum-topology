package server

import (
	"context"
	"errors"
	"sync"

	ikhnosv1 "ikhnos/gen/ikhnosv1"
	"ikhnos/internal/agents"
	"ikhnos/internal/estate"
	"ikhnos/internal/kernel"
	"ikhnos/internal/store"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Tenant is everything the server keeps live for one organisation: its scoped core (settings and
// callbacks), and its hub (the agents' streams, their facts, the history recorder). Two tenants share
// the database and the CA and nothing else, so one cannot see or reach the other's agents.
type Tenant struct {
	ID   string
	C    *Core
	Hub  *estate.Hub
	stop context.CancelFunc
}

// Platform holds the tenants. It is also the AgentService the gRPC listener serves: an agent's stream
// is handed to the hub of the organisation its certificate was issued for, and to no other.
type Platform struct {
	*agents.BaseAgentService // Renew, which works for any organisation's agent
	Base                     *Core
	Geo                      *agents.Geo
	// Tune, when set, is applied to each new tenant's hub (tests use it to shorten intervals).
	Tune func(*estate.Hub)

	mu      sync.Mutex
	tenants map[string]*Tenant
	ctx     context.Context
}

func NewPlatform(base *Core, geo *agents.Geo) *Platform {
	return &Platform{BaseAgentService: &agents.BaseAgentService{C: base.Agents}, Base: base, Geo: geo, tenants: map[string]*Tenant{}, ctx: context.Background()}
}

// Start brings every existing organisation up (restoring what each last knew) and ties all their
// background work to ctx.
func (p *Platform) Start(ctx context.Context) error {
	p.mu.Lock()
	p.ctx = ctx
	p.mu.Unlock()
	orgs, err := p.Base.Store.ListOrgs(ctx)
	if err != nil {
		return err
	}
	for _, o := range orgs {
		if _, err := p.Tenant(ctx, o.ID); err != nil {
			return err
		}
	}
	return nil
}

// Tenant returns the live state of an organisation, creating it the first time it is needed.
func (p *Platform) Tenant(ctx context.Context, id string) (*Tenant, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if t := p.tenants[id]; t != nil {
		return t, nil
	}
	if _, err := p.Base.Store.GetOrg(ctx, id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, kernel.Errf(kernel.KindNotFound, "no such organisation")
		}
		return nil, err
	}
	c := p.Base.ForOrg(id)
	c.OnOrgDeleted = p.drop
	c.LoadSettings(ctx)
	h := estate.NewHub(c.Estate)
	h.Geo = p.Geo
	if p.Tune != nil {
		p.Tune(h)
	}
	h.Restore(ctx)
	rctx, cancel := context.WithCancel(p.ctx)
	go h.Run(rctx)
	t := &Tenant{ID: id, C: c, Hub: h, stop: cancel}
	p.tenants[id] = t
	return t, nil
}

// FlushAll writes every live organisation's write-behind state (tombstones and node identities, which are
// saved on a timer) to the database now. A graceful shutdown calls it after the servers have stopped and
// before the database closes; without it every restart loses whatever the last timer interval had gathered.
// It is deliberately not part of drop: a deleted organisation's data is gone and must not be written back.
func (p *Platform) FlushAll(ctx context.Context) {
	p.mu.Lock()
	hubs := make([]*estate.Hub, 0, len(p.tenants))
	for _, t := range p.tenants {
		hubs = append(hubs, t.Hub)
	}
	p.mu.Unlock()
	for _, h := range hubs {
		h.Flush(ctx)
	}
}

// drop stops an organisation's live state after its data was deleted, and ends its agents' streams.
func (p *Platform) drop(id string) {
	p.mu.Lock()
	t := p.tenants[id]
	delete(p.tenants, id)
	p.mu.Unlock()
	if t != nil {
		t.Hub.DropAll("the organisation was deleted")
		t.stop()
	}
}

// Connect routes the agent's stream to its own organisation's hub.
func (p *Platform) Connect(stream ikhnosv1.AgentService_ConnectServer) error {
	a, ok := agents.AgentFrom(stream.Context())
	if !ok {
		return status.Error(codes.Unauthenticated, "no agent")
	}
	t, err := p.Tenant(stream.Context(), a.OrgID)
	if err != nil {
		return status.Error(codes.Unauthenticated, "agent is not approved")
	}
	return t.Hub.Connect(stream)
}
