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
	// loading is the organisations being brought up right now, each by the one caller that is doing it; the others wait for that one
	// instead of the lock, so a slow organisation does not hold up every other organisation's requests and agents.
	loading map[string]*tenantLoad
	ctx     context.Context
}

type tenantLoad struct {
	done chan struct{}
	t    *Tenant
	err  error
	// dropped is set when the organisation was deleted while it was being brought up: what was built is stopped, not kept.
	dropped bool
}

func NewPlatform(base *Core, geo *agents.Geo) *Platform {
	return &Platform{BaseAgentService: &agents.BaseAgentService{C: base.Agents}, Base: base, Geo: geo, tenants: map[string]*Tenant{}, loading: map[string]*tenantLoad{}, ctx: context.Background()}
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

// Tenant returns the live state of an organisation, creating it the first time it is needed. Creating it reads the organisation's whole
// state from the database, which on a large one takes seconds, so it is done without the lock: another organisation's request, an agent
// connecting to it, and the metrics scrape do not wait for it. Two callers for the same organisation share one load.
func (p *Platform) Tenant(ctx context.Context, id string) (*Tenant, error) {
	for {
		p.mu.Lock()
		if t := p.tenants[id]; t != nil {
			p.mu.Unlock()
			return t, nil
		}
		if l := p.loading[id]; l != nil {
			p.mu.Unlock()
			select {
			case <-l.done:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			if l.err == nil {
				return l.t, nil
			}
			// A load that failed because its own caller gave up says nothing about this one: try again.
			if (errors.Is(l.err, context.Canceled) || errors.Is(l.err, context.DeadlineExceeded)) && ctx.Err() == nil {
				continue
			}
			return nil, l.err
		}
		l := &tenantLoad{done: make(chan struct{})}
		p.loading[id] = l
		runCtx := p.ctx
		p.mu.Unlock()

		t, err := p.build(ctx, runCtx, id)

		p.mu.Lock()
		delete(p.loading, id)
		if err == nil && l.dropped {
			t.Hub.DropAll("the organisation was deleted")
			t.stop()
			t, err = nil, kernel.Errf(kernel.KindNotFound, "no such organisation")
		} else if err == nil {
			p.tenants[id] = t
		}
		p.mu.Unlock()
		l.t, l.err = t, err
		close(l.done)
		return t, err
	}
}

// build brings one organisation up: its settings, its hub restored from the database, and the hub's background work tied to runCtx.
func (p *Platform) build(ctx, runCtx context.Context, id string) (*Tenant, error) {
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
	rctx, cancel := context.WithCancel(runCtx)
	go h.Run(rctx)
	return &Tenant{ID: id, C: c, Hub: h, stop: cancel}, nil
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
	if l := p.loading[id]; l != nil {
		l.dropped = true
	}
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
