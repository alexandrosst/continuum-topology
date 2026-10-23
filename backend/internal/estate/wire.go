package estate

import (
	"ikhnos/internal/agents"
	"ikhnos/internal/model"
	"ikhnos/internal/twin"
	"ikhnos/internal/wire"
	"ikhnos/internal/workspace"
)

// WireTypes are the shapes this package puts on the wire (or reads from it), for the TypeScript the server writes of them
// (internal/wire). A document the package encodes and that is not listed here is not in that file: the page would then have to write its
// type by hand again.
func WireTypes() []wire.Root {
	return []wire.Root{
		{Value: tokenDoc{}},
		{Value: agentUpgradeRequest{}},
		{Value: credentialSecretDoc{}},
		{Value: agentUpgradeDoc{}},
		{Value: eventDoc{}},
		{Value: SelfTelemetryEntity{}},
		{Value: applicationDoc{}},
		{Value: model.Application{}},
		{Value: workspace.Site{}},
		{Value: workspace.SiteLink{}},
		{Value: workspace.Device{}},
		{Value: AgentDoc{}},
		{Value: StateDoc{}},
		{Value: TeardownDoc{}},
		{Value: AddressCandidates{}},
		{Value: ServerClusterDoc{}},
		{Value: ExposeDoc{}},
		{Value: TombstoneDoc{}},
		{Value: agents.ConsentDoc{}},
		{Name: "TwinModel", Value: twin.Model{}},
		{Value: storageDoc{}},
		{Value: graphSnapshotDoc{}},
		{Value: graphWalkDoc{}},
		{Value: graphDiffDoc{}},
		{Value: timelineDoc{}},
		{Value: auditSearchDoc{}},
		{Value: workspaceRevisionsDoc{}},
		{Value: workspaceAtDoc{}},
		{Value: eventsDoc{}},
		{Value: historyIndexDoc{}},
		{Value: historySnapshotDoc{}},
		{Value: trafficDoc{}},
		{Value: dependencySeriesDoc{}},
		{Value: workspaceDoc{}},
		{Value: workspaceSavedDoc{}},
		{Value: workspaceConflictDoc{}},
		{Value: createdTokenDoc{}},
		{Value: agentTierDoc{}},
		{Value: addressTestsDoc{}},
		{Value: serverClusterChoiceDoc{}},
		{Value: createTokenRequest{}},
		{Value: approveAgentRequest{}},
		{Value: setAgentTierRequest{}},
		{Value: setAgentConsentRequest{}},
		{Value: addressTestsRequest{}},
		{Value: serverClusterRequest{}},
	}
}
