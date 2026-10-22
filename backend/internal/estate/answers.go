package estate

import (
	"encoding/json"

	"continuum/internal/agents"
	"continuum/internal/connectivity"
	"continuum/internal/graph"
	"continuum/internal/history"
	"continuum/internal/model"
)

// The answers this package's routes give that are not one of the larger documents (StateDoc, AgentDoc and the others in their own
// files). Each was a map written at the handler; each is a struct now so that the TypeScript written from the Go types (internal/wire)
// has it, and so that a handler cannot misspell a key or send a number where the page reads text.
//
// The fields are in the alphabetical order of their keys, which is the order a map was encoded in: the bytes on the wire are the ones
// they were before.

// StorageBackend is where history is kept.
type StorageBackend string

// The places history is kept.
const (
	BackendSQLite StorageBackend = "sqlite" // the server's own database: events and audit, no graph
	BackendNeo4j  StorageBackend = "neo4j"  // the graph database
)

// WireWords is the list of StorageBackend's words, as the generated types give it.
func (StorageBackend) WireWords() []string {
	return []string{string(BackendSQLite), string(BackendNeo4j)}
}

// storageDoc answers GET /storage. Without the graph database only Backend and Enabled are there; with it the health follows, and an
// administrator also has Error (empty when there is none) and Stats.
type storageDoc struct {
	Backend   StorageBackend `json:"backend"`
	Buffering *bool          `json:"buffering,omitempty"`
	Connected *bool          `json:"connected,omitempty"`
	Enabled   bool           `json:"enabled"`
	Error     *string        `json:"error,omitempty"`
	Ready     *bool          `json:"ready,omitempty"`
	Stats     *graph.Stats   `json:"stats,omitempty"`
}

// graphSnapshotDoc answers GET /graph/snapshot: every entity the graph held at a moment.
type graphSnapshotDoc struct {
	At       string                 `json:"at"`
	Entities []graph.EntitySnapshot `json:"entities"`
}

// graphWalkDoc answers GET /graph/dependents and /graph/dependencies: what a walk from one entity reached.
type graphWalkDoc struct {
	At      string          `json:"at"`
	Hops    int             `json:"hops"`
	ID      string          `json:"id"`
	Kind    string          `json:"kind"`
	Reached []graph.Reached `json:"reached"`
}

// graphDiffDoc answers GET /graph/diff: how the estate differs between two moments.
type graphDiffDoc struct {
	Added   []graph.EntitySnapshot `json:"added"`
	Changed []graph.EntityDiff     `json:"changed"`
	From    string                 `json:"from"`
	Removed []graph.EntitySnapshot `json:"removed"`
	To      string                 `json:"to"`
}

// timelineDoc answers GET /graph/timeline: one entity's versions, the events about it and (for administrators) the actions on it.
type timelineDoc struct {
	Audit    []graph.AuditRow        `json:"audit"`
	Events   []eventDoc              `json:"events"`
	ID       string                  `json:"id"`
	Kind     string                  `json:"kind"`
	Versions []graph.TimelineVersion `json:"versions"`
}

// AuditSource says where the rows of an audit search came from.
type AuditSource string

// The places an audit search is read from.
const (
	AuditGraph AuditSource = "graph" // searchable
	AuditLocal AuditSource = "local" // the latest actions the control plane keeps itself
)

// WireWords is the list of AuditSource's words, as the generated types give it.
func (AuditSource) WireWords() []string { return []string{string(AuditGraph), string(AuditLocal)} }

// auditSearchDoc answers GET /audit. Source says where the rows came from: "graph" (searchable) or "local" (the latest actions the control
// store keeps, filtered by the server).
type auditSearchDoc struct {
	Rows   []graph.AuditRow `json:"rows"`
	Source AuditSource      `json:"source"`
}

// workspaceRevisionsDoc answers GET /workspace/revisions.
type workspaceRevisionsDoc struct {
	Revisions []graph.WorkspaceRev `json:"revisions"`
}

// workspaceAtDoc answers GET /workspace/at: the workspace as it was saved at a moment. Note is what the server changed when it brought an
// older document up to date.
type workspaceAtDoc struct {
	Data      json.RawMessage `json:"data"`
	Note      string          `json:"note,omitempty"`
	Rev       int64           `json:"rev"`
	UpdatedAt string          `json:"updatedAt"`
	UpdatedBy string          `json:"updatedBy"`
}

// eventsDoc answers GET /events.
type eventsDoc struct {
	Events []eventDoc `json:"events"`
}

// historyPointDoc is one recorded snapshot in the history index.
type historyPointDoc struct {
	At    string `json:"at"`
	Bytes int    `json:"bytes"`
}

// historyIndexDoc answers GET /history: when snapshots were taken, and how often and for how long they are kept.
type historyIndexDoc struct {
	Points          []historyPointDoc `json:"points"`
	RetentionDays   int               `json:"retentionDays"`
	SnapshotMinutes int               `json:"snapshotMinutes"`
}

// historySnapshotDoc answers GET /history/snapshot: the topology at a moment. Agents is absent when the server has no memory of how the
// agents looked then, and the page shows today's unchanged.
type historySnapshotDoc struct {
	Agents   []agents.HistoricAgent `json:"agents,omitempty"`
	At       string                 `json:"at"`
	Topology model.Topology         `json:"topology"`
}

// trafficDoc answers GET /history/traffic: what each dependency carried on average and at most over a window.
type trafficDoc struct {
	Hours     int            `json:"hours"`
	Rates     []history.Rate `json:"rates"`
	Snapshots int            `json:"snapshots"`
}

// dependencySeriesDoc answers GET /history/dependencies/{id}: one dependency's trend.
type dependencySeriesDoc struct {
	Hours  int                     `json:"hours"`
	ID     string                  `json:"id"`
	Points []dependencySeriesPoint `json:"points"`
}

// workspaceDoc answers GET /workspace. With ?meta=1 (or before a first save) only the revision part is there.
type workspaceDoc struct {
	Data          json.RawMessage `json:"data,omitempty"`
	FormatVersion int             `json:"formatVersion"`
	Note          string          `json:"note,omitempty"`
	Rev           int64           `json:"rev"`
	UpdatedAt     string          `json:"updatedAt"`
	UpdatedBy     string          `json:"updatedBy"`
}

// workspaceSavedDoc answers PUT /workspace.
type workspaceSavedDoc struct {
	Note      string `json:"note,omitempty"`
	Rev       int64  `json:"rev"`
	UpdatedAt string `json:"updatedAt"`
	UpdatedBy string `json:"updatedBy"`
}

// workspaceConflictDoc answers PUT /workspace with 409: someone saved first, and this is their revision.
type workspaceConflictDoc struct {
	Error     string `json:"error"`
	Rev       int64  `json:"rev"`
	UpdatedAt string `json:"updatedAt"`
	UpdatedBy string `json:"updatedBy"`
}

// createdTokenDoc answers POST /tokens: the token itself, which is shown once, what is kept of it, and the command that uses it.
type createdTokenDoc struct {
	Install string   `json:"install"`
	Meta    tokenDoc `json:"meta"`
	Token   string   `json:"token"`
}

// agentTierDoc answers PUT /agents/{id}/tier. HardenHelm is there when the new tier is below what the install allows: the command that
// narrows the cluster's own permission to match.
type agentTierDoc struct {
	AccessTier    int    `json:"accessTier"`
	HardenHelm    string `json:"hardenHelm,omitempty"`
	InstalledTier int    `json:"installedTier"`
}

// addressTestsDoc answers POST and GET /agents/{id}/address-tests.
type addressTestsDoc struct {
	Tests []connectivity.AddressTestDoc `json:"tests"`
}

// serverClusterChoiceDoc answers PUT /operators/central/server-cluster: the cluster now chosen (empty when the choice was taken back).
type serverClusterChoiceDoc struct {
	ClusterID string `json:"clusterId"`
}
