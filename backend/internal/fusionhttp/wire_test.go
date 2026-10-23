package fusionhttp

import (
	"encoding/json"
	"testing"
	"time"

	"ikhnos/internal/fusion"
	"ikhnos/internal/fusionapi"
	"ikhnos/internal/wire"
)

func TestEveryDocumentIsListedForTheWire(t *testing.T) {
	wire.RequireDocsListed(t, ".", WireTypes())
}

// sameBytes fails the test unless got is written exactly as was is: was being the map a handler wrote before the answer had a type.
func sameBytes(t *testing.T, name string, got, was any) {
	t.Helper()
	g, err := json.Marshal(got)
	if err != nil {
		t.Fatal(name, err)
	}
	w, err := json.Marshal(was)
	if err != nil {
		t.Fatal(name, err)
	}
	if string(g) != string(w) {
		t.Errorf("%s:\n got %s\nwant %s", name, g, w)
	}
}

// The answers that were maps at the handlers are structs now, with their fields in the order a map's keys were encoded in. Each way an answer
// could come out is held to the map it replaced: the bytes on the wire are the ones they were.
func TestAnswersThatWereMapsAreWrittenAsTheyWere(t *testing.T) {
	from, to := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), time.Date(2026, 1, 2, 4, 4, 5, 0, time.UTC)
	logs := []fusionapi.LogEntry{{}}
	sameBytes(t, "logs", logsDoc{Entries: logs, Truncated: true, Limit: 200, From: from, To: to},
		map[string]any{"entries": logs, "truncated": true, "limit": 200, "from": from, "to": to})
	sameBytes(t, "metric names", metricNamesDoc{Names: []string{"a"}, Categories: map[string]string{"a": "cpu"}, Limit: 500, From: from, To: to},
		map[string]any{"names": []string{"a"}, "categories": map[string]string{"a": "cpu"}, "truncated": false, "limit": 500, "from": from, "to": to})
	sameBytes(t, "metric series", metricSeriesDoc{Series: []map[string]string{{"job": "x"}}, Limit: 200, From: from, To: to},
		map[string]any{"series": []map[string]string{{"job": "x"}}, "truncated": false, "limit": 200, "from": from, "to": to})
	sameBytes(t, "metric range", metricRangeDoc{Series: []fusionapi.MetricSeries{}, StepSeconds: 30, Limit: 20, From: from, To: to},
		map[string]any{"series": []fusionapi.MetricSeries{}, "truncated": false, "limit": 20, "stepSeconds": 30.0, "from": from, "to": to})

	st := fusion.FusionStatus{Available: true, State: "running"}
	sameBytes(t, "status, a user", statusDoc{Fusion: st, Access: callerAccessDoc{Kind: "user", Name: "alex", Signals: []fusionapi.Signal{"logs"}, Namespaces: []string{}, Clusters: []string{}, RawQueries: true}},
		map[string]any{"fusion": st, "access": map[string]any{"kind": "user", "name": "alex", "signals": []string{"logs"}, "namespaces": []string{}, "clusters": []string{}, "rawQueries": true}})
	sameBytes(t, "status, a token that expires", statusDoc{Fusion: st, Access: callerAccessDoc{Kind: "token", Name: "ci", Signals: []fusionapi.Signal{"logs"}, Namespaces: []string{"a"}, Clusters: []string{}, ExpiresAt: "2026-02-01T00:00:00Z"}},
		map[string]any{"fusion": st, "access": map[string]any{"kind": "token", "name": "ci", "signals": []string{"logs"}, "namespaces": []string{"a"}, "clusters": []string{}, "rawQueries": false, "expiresAt": "2026-02-01T00:00:00Z"}})

	src := map[string]string{"metrics": "ok"}
	sameBytes(t, "applications", applicationsDoc{Applications: []fusionapi.AppView{}, Sources: src, Warnings: []string{}, From: from, To: to},
		map[string]any{"applications": []fusionapi.AppView{}, "sources": src, "warnings": []string{}, "from": from, "to": to})
	svc := []serviceDoc{{Application: fusionapi.Application{Name: "web", Signals: []fusionapi.Signal{"logs"}}, Applications: []string{"shop"}}}
	sameBytes(t, "services", servicesDoc{Services: svc, Sources: src, Warnings: []string{}, From: from, To: to},
		map[string]any{"services": svc, "sources": src, "warnings": []string{}, "from": from, "to": to})

	// a trace search: plain, streamed (the first line), fused (with nothing found, and with something)
	tr := []fusionapi.TraceSummary{{TraceID: "t1"}}
	sameBytes(t, "traces", tracesDoc{Traces: tr, Limit: 20, From: from, To: to}, map[string]any{"traces": tr, "truncated": false, "limit": 20, "from": from, "to": to})
	sameBytes(t, "traces, streamed", tracesDoc{Traces: tr, Limit: 10, From: from, To: to, Type: "traces"},
		map[string]any{"traces": tr, "truncated": false, "limit": 10, "from": from, "to": to, "type": "traces"})
	items := []fusionapi.BulkItem{{ID: "t1", Status: 200}}
	sum := fusionapi.Summarise(items)
	sameBytes(t, "traces, fused", tracesDoc{Traces: tr, Limit: 10, From: from, To: to, Results: items, Summary: &sum},
		map[string]any{"traces": tr, "truncated": false, "limit": 10, "from": from, "to": to, "results": items, "summary": sum})
	none := fusionapi.Summarise(nil)
	sameBytes(t, "traces, fused, none found", tracesDoc{Traces: []fusionapi.TraceSummary{}, Limit: 10, From: from, To: to, Results: []fusionapi.BulkItem{}, Summary: &none},
		map[string]any{"traces": []fusionapi.TraceSummary{}, "truncated": false, "limit": 10, "from": from, "to": to, "results": []fusionapi.BulkItem{}, "summary": none})
	sameBytes(t, "trace batch", traceBatchDoc{Results: items, Summary: sum}, map[string]any{"results": items, "summary": sum})
}
