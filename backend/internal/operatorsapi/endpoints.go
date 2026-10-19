package operatorsapi

import (
	"context"
	"fmt"

	"continuum/internal/kernel"
	"continuum/internal/operators"
	"continuum/internal/store"
)

// Advertised is op as a caller elsewhere sees it: the central operator is reached at the address FUSION is exposed at
// (empty when it is not, which leaves its in-cluster name), so from here on it is handled like any operator that has an
// Advertised address - dialled there, and verified by its stable name.
func (h *API) Advertised(op store.Operator) store.Operator {
	if c := h.central(); op.ID == operators.CentralOperatorID && c != nil && c.Exposed() {
		op.Address = c.Endpoint()
	}
	return op
}

// Endpoint is where an exporter is pointed to reach op's receiver: the operator's own Service in the
// namespace the install commands use, or - for the central operator, which lives with the server and may be
// exposed - the address FusionControl says (see CentralEndpoint). Any other operator with an Advertised address
// (see store.Operator.Address) is reached there, so a cluster elsewhere can find it.
func (h *API) Endpoint(op store.Operator) string {
	if c := h.central(); op.ID == operators.CentralOperatorID && c != nil {
		return c.Endpoint()
	}
	if op.Address != "" {
		return op.Address
	}
	return operators.OperatorInClusterEndpoint(op)
}

// DestEndpoint is the endpoint a sender dials to reach op as its destination d: the address that sender chose
// (d.Address) when it chose one, else op's own (Endpoint). Only a destination that names op counts.
func (h *API) DestEndpoint(op store.Operator, d store.Destination) string {
	if d.Kind == store.DestinationOperator && d.TargetOperatorID == op.ID && d.Address != "" {
		return d.Address
	}
	return h.Endpoint(op)
}

// DestWarnings are the doubts about sending to op as destination d. An address the sender chose is held to the
// same checks as a recorded one; without one, targetWarnings says what the operator's own address allows.
func (h *API) DestWarnings(ctx context.Context, op store.Operator, d store.Destination) []string {
	if d.Kind == store.DestinationOperator && d.TargetOperatorID == op.ID && d.Address != "" {
		name := op.Name
		if op.ID == operators.CentralOperatorID && name == "" {
			name = CentralOperatorName
		}
		var out []string
		for _, w := range AddressWarningsFor(d.Address, nil, false) {
			out = append(out, name+": "+w)
		}
		return out
	}
	return h.targetWarnings(ctx, op)
}

// targetWarnings are the doubts about pointing something at op, for the commands that do: an operator with no address
// recorded is dialled by its in-cluster name, which resolves only inside the cluster it runs in, and an address that
// is recorded is only what a person typed (see AddressWarnings). Empty when there is nothing to say.
func (h *API) targetWarnings(ctx context.Context, op store.Operator) []string {
	name := op.Name
	if op.ID == operators.CentralOperatorID && name == "" {
		name = CentralOperatorName
	}
	op = h.Advertised(op)
	if op.Address == "" {
		return []string{fmt.Sprintf("%s has no address recorded, so this command dials its in-cluster name (%s), which resolves only inside the cluster it runs in. If the cluster being pointed at it is another one, record where %s is reached from outside first.", name, h.Endpoint(op), name)}
	}
	var svc *kernel.KubeService
	if c := h.central(); op.ID == operators.CentralOperatorID && c != nil {
		svc = c.GatewayService(ctx)
	}
	var out []string
	for _, w := range AddressWarnings(op.Address, svc) {
		out = append(out, name+": "+w)
	}
	return out
}

// CentralAddressWarnings are the doubts about the public address recorded for the gateway (see AddressWarnings), checked against the
// gateway's own Service. Empty when there is no address, or nothing to doubt.
func CentralAddressWarnings(ctx context.Context, c operators.Central) []string {
	if c == nil || !c.Exposed() {
		return nil
	}
	return AddressWarnings(c.Endpoint(), c.GatewayService(ctx))
}
