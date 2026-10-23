package estate

import (
	"context"
	"log/slog"
	"sync"

	ikhnosv1 "ikhnos/gen/ikhnosv1"
	"ikhnos/internal/agents"
	"ikhnos/internal/connectivity"
	"ikhnos/internal/decisionloop"
	"ikhnos/internal/kernel"
	"ikhnos/internal/operators"
	"ikhnos/internal/pki"
	"ikhnos/internal/store"
	"ikhnos/internal/telemetry"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// wrap is what the shell does to a kernel Core: the modules the estate stands on, built over it.
func wrap(k *kernel.Core) *Service {
	ops := operators.NewService(k)
	tel := telemetry.NewService(k, ops)
	return NewService(k, agents.NewService(k, ops, tel), connectivity.NewService(k), decisionloop.NewService(k))
}

// newCore returns a Service scoped to org (see kernel.NewCore).
func newCore(st store.Store, ca *pki.CA, org string, log *slog.Logger) *Service {
	return wrap(kernel.NewCore(st, ca, org, log))
}

// ForOrg returns a view of the same server scoped to one organisation.
func (s *Service) ForOrg(org string) *Service { return wrap(s.Core.ForOrg(org)) }

// testTenant is a hub with the scoped core it was made from, as the shell keeps one for each organisation.
type testTenant struct {
	C   *Service
	Hub *Hub
}

// testPlatform is the shell's Platform, reduced to what the Hub's tests need: a hub for each organisation, made the first time it is
// asked for, and the gRPC service that hands an agent's stream to the hub of the organisation its certificate was issued for.
type testPlatform struct {
	*agents.BaseAgentService
	Base *Service
	// Tune, when set, is applied to each new hub (tests use it to shorten intervals).
	Tune func(*Hub)

	mu      sync.Mutex
	tenants map[string]*testTenant
}

func newTestPlatform(base *Service) *testPlatform {
	return &testPlatform{BaseAgentService: &agents.BaseAgentService{C: base.Agents}, Base: base, tenants: map[string]*testTenant{}}
}

func (p *testPlatform) Tenant(ctx context.Context, id string) (*testTenant, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if t := p.tenants[id]; t != nil {
		return t, nil
	}
	if _, err := p.Base.Store.GetOrg(ctx, id); err != nil {
		return nil, err
	}
	c := p.Base.ForOrg(id)
	c.LoadSettings(ctx)
	h := NewHub(c)
	if p.Tune != nil {
		p.Tune(h)
	}
	h.Restore(ctx)
	rctx, cancel := context.WithCancel(context.Background())
	go h.Run(rctx)
	t := &testTenant{C: c, Hub: h}
	p.tenants[id] = t
	_ = cancel // the hub runs until the test process ends, as it does in the shell's tests
	return t, nil
}

// Connect routes the agent's stream to its own organisation's hub.
func (p *testPlatform) Connect(stream ikhnosv1.AgentService_ConnectServer) error {
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
