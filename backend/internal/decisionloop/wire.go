package decisionloop

import (
	"continuum/internal/wire"
)

// WireTypes are the shapes this package puts on the wire (or reads from it), for the TypeScript the server writes of them
// (internal/wire). A document the package encodes and that is not listed here is not in that file: the page would then have to write its
// type by hand again.
func WireTypes() []wire.Root {
	return []wire.Root{
		{Value: decisionLogDoc{}},
		{Value: proposalDoc{}},
		{Value: advisorRecordDoc{}},
		{Value: outcomeDoc{}},
		{Value: capacityDoc{}},
		{Value: excludedDoc{}},
		{Value: recordedDoc{}},
		{Value: decisionsDoc{}},
		{Value: externalDecisionDoc{}},
		{Value: proposalsDoc{}},
		{Value: advisorsDoc{}},
		{Value: recordDecisionsRequest{}},
		{Value: submitProposalRequest{}},
		{Value: decideProposalRequest{}},
	}
}
