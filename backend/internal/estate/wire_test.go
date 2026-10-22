package estate

import (
	"encoding/json"
	"testing"

	"continuum/internal/agents"
	"continuum/internal/connectivity"
	"continuum/internal/graph"
	"continuum/internal/history"
	"continuum/internal/model"
	"continuum/internal/wire"
)

func TestEveryDocumentIsListedForTheWire(t *testing.T) {
	wire.RequireDocsListed(t, ".", WireTypes())
}

// The answers that were maps at the handlers are structs now. Each is checked against the map it replaces, so that the bytes on the wire
// are the ones they were: a client that compares them, or a log that was grepped, sees no difference. (A map is encoded with its keys in
// alphabetical order, which is the order the struct fields are in.)
func TestAnswersThatWereMapsAreWrittenAsTheyWere(t *testing.T) {
	yes, no, bad := true, false, "no route"
	stats := &graph.Stats{Snapshots: 2, Versions: 3, Entities: 4}
	topo := model.Topology{}
	ag := []agents.HistoricAgent{{ID: "a1"}}
	data := json.RawMessage(`{"x":1}`)
	tests := connectivity.AddressTestDoc{}
	cases := []struct {
		name string
		got  any
		was  map[string]any
	}{
		{"storage without the graph", storageDoc{Backend: "sqlite"}, map[string]any{"backend": "sqlite", "enabled": false}},
		{"storage, for a member", storageDoc{Backend: "neo4j", Enabled: true, Connected: &yes, Ready: &no, Buffering: &yes},
			map[string]any{"backend": "neo4j", "enabled": true, "connected": true, "ready": false, "buffering": true}},
		{"storage, for an administrator", storageDoc{Backend: "neo4j", Enabled: true, Connected: &yes, Ready: &yes, Buffering: &no, Error: &bad, Stats: stats},
			map[string]any{"backend": "neo4j", "enabled": true, "connected": true, "ready": true, "buffering": false, "error": "no route", "stats": stats}},
		{"storage, an administrator with no error", storageDoc{Backend: "neo4j", Enabled: true, Connected: &yes, Ready: &yes, Buffering: &no, Error: new(string)},
			map[string]any{"backend": "neo4j", "enabled": true, "connected": true, "ready": true, "buffering": false, "error": ""}},
		{"graph snapshot", graphSnapshotDoc{At: "t", Entities: []graph.EntitySnapshot{}}, map[string]any{"at": "t", "entities": []graph.EntitySnapshot{}}},
		{"graph walk", graphWalkDoc{At: "t", Kind: "service", ID: "s", Hops: 3, Reached: []graph.Reached{}},
			map[string]any{"at": "t", "kind": "service", "id": "s", "hops": 3, "reached": []graph.Reached{}}},
		{"graph diff", graphDiffDoc{From: "f", To: "t"}, map[string]any{"from": "f", "to": "t", "added": []graph.EntitySnapshot(nil), "removed": []graph.EntitySnapshot(nil), "changed": []graph.EntityDiff(nil)}},
		{"timeline", timelineDoc{Kind: "k", ID: "i", Events: []eventDoc{}, Audit: []graph.AuditRow{}},
			map[string]any{"kind": "k", "id": "i", "versions": []graph.TimelineVersion(nil), "events": []eventDoc{}, "audit": []graph.AuditRow{}}},
		{"audit", auditSearchDoc{Source: "local", Rows: []graph.AuditRow{}}, map[string]any{"source": "local", "rows": []graph.AuditRow{}}},
		{"workspace revisions", workspaceRevisionsDoc{Revisions: []graph.WorkspaceRev{}}, map[string]any{"revisions": []graph.WorkspaceRev{}}},
		{"workspace at", workspaceAtDoc{Rev: 4, UpdatedAt: "u", UpdatedBy: "b", Data: data},
			map[string]any{"rev": int64(4), "updatedAt": "u", "updatedBy": "b", "data": data}},
		{"workspace at, with a note", workspaceAtDoc{Rev: 4, UpdatedAt: "u", UpdatedBy: "b", Data: data, Note: "n"},
			map[string]any{"rev": int64(4), "updatedAt": "u", "updatedBy": "b", "data": data, "note": "n"}},
		{"events", eventsDoc{Events: []eventDoc{{ID: "ev-1"}}}, map[string]any{"events": []eventDoc{{ID: "ev-1"}}}},
		{"history index", historyIndexDoc{Points: []historyPointDoc{{"t", 9}}, SnapshotMinutes: 5, RetentionDays: 30},
			map[string]any{"points": []historyPointDoc{{"t", 9}}, "snapshotMinutes": 5, "retentionDays": 30}},
		{"history snapshot", historySnapshotDoc{At: "t", Topology: topo}, map[string]any{"at": "t", "topology": topo}},
		{"history snapshot with agents", historySnapshotDoc{At: "t", Topology: topo, Agents: ag}, map[string]any{"at": "t", "topology": topo, "agents": ag}},
		{"traffic", trafficDoc{Hours: 24, Snapshots: 3, Rates: []history.Rate{{ID: "d"}}},
			map[string]any{"hours": 24, "snapshots": 3, "rates": []history.Rate{{ID: "d"}}}},
		{"dependency series", dependencySeriesDoc{ID: "d", Hours: 24, Points: []dependencySeriesPoint{}}, map[string]any{"id": "d", "hours": 24, "points": []dependencySeriesPoint{}}},
		{"workspace", workspaceDoc{Rev: 2, UpdatedAt: "u", UpdatedBy: "b", FormatVersion: 7, Data: data},
			map[string]any{"rev": int64(2), "updatedAt": "u", "updatedBy": "b", "formatVersion": 7, "data": data}},
		{"workspace, meta only", workspaceDoc{Rev: 2, UpdatedAt: "u", UpdatedBy: "b", FormatVersion: 7},
			map[string]any{"rev": int64(2), "updatedAt": "u", "updatedBy": "b", "formatVersion": 7}},
		{"workspace saved", workspaceSavedDoc{Rev: 3, UpdatedAt: "u", UpdatedBy: "b"}, map[string]any{"rev": int64(3), "updatedAt": "u", "updatedBy": "b"}},
		{"workspace saved with a note", workspaceSavedDoc{Rev: 3, UpdatedAt: "u", UpdatedBy: "b", Note: "n"},
			map[string]any{"rev": int64(3), "updatedAt": "u", "updatedBy": "b", "note": "n"}},
		{"workspace conflict", workspaceConflictDoc{Error: "e", Rev: 3, UpdatedAt: "u", UpdatedBy: "b"}, map[string]any{"error": "e", "rev": int64(3), "updatedAt": "u", "updatedBy": "b"}},
		{"created token", createdTokenDoc{Token: "s", Meta: tokenDoc{ID: "i"}, Install: "helm"}, map[string]any{"token": "s", "meta": tokenDoc{ID: "i"}, "install": "helm"}},
		{"agent tier", agentTierDoc{AccessTier: 1, InstalledTier: 2}, map[string]any{"accessTier": 1, "installedTier": 2}},
		{"agent tier with the command", agentTierDoc{AccessTier: 1, InstalledTier: 2, HardenHelm: "helm"}, map[string]any{"accessTier": 1, "installedTier": 2, "hardenHelm": "helm"}},
		{"address tests", addressTestsDoc{Tests: []connectivity.AddressTestDoc{tests}}, map[string]any{"tests": []connectivity.AddressTestDoc{tests}}},
		{"server cluster choice", serverClusterChoiceDoc{ClusterID: "c"}, map[string]any{"clusterId": "c"}},
	}
	for _, c := range cases {
		got, err := json.Marshal(c.got)
		if err != nil {
			t.Fatal(c.name, err)
		}
		want, err := json.Marshal(c.was)
		if err != nil {
			t.Fatal(c.name, err)
		}
		if string(got) != string(want) {
			t.Errorf("%s:\n got %s\nwant %s", c.name, got, want)
		}
	}
}

// The words the page switches on are the constants the code writes, no more and no fewer.
func TestTheWordsOfStorageAreItsConstants(t *testing.T) {
	wire.RequireWordsComplete(t, ".", StorageBackend(""), AuditSource(""))
}
