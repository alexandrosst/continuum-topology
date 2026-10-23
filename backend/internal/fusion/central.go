package fusion

import (
	"context"

	"ikhnos/internal/kernel"
	"ikhnos/internal/operators"
)

// Central is the control as the operators' port for the central operator (operators.Central). It is nil, an interface that holds
// nothing, when there is no control: a method on a nil *FusionControl would answer, but a caller that asks "is there a central
// operator" must be able to tell.
func (f *FusionControl) Central() operators.Central {
	if f == nil {
		return nil
	}
	return central{f}
}

// central is the port. The control keeps the organisation, the namespace and the cluster UID as fields, so they are read through here.
type central struct{ f *FusionControl }

var _ operators.Central = central{}

func (c central) Org() string { return c.f.Org }
func (c central) Managed(ctx context.Context) operators.ManagedStatus {
	return c.f.Status(ctx).Managed()
}
func (c central) Exposed() bool    { return c.f.Exposed() }
func (c central) Endpoint() string { return c.f.CentralEndpoint() }
func (c central) WithGatewayPort(ctx context.Context, addr string) string {
	return c.f.WithGatewayPort(ctx, addr)
}
func (c central) SetPublicAddress(addr string) { c.f.SetPublicAddress(addr) }
func (c central) GatewayService(ctx context.Context) *kernel.KubeService {
	return c.f.GatewayService(ctx)
}
func (c central) ServiceName() string { return c.f.ServiceName() }
func (c central) Namespace() string   { return c.f.Namespace }
func (c central) ClusterUID() string  { return c.f.ClusterUID }
