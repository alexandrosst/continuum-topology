package workspace

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// oldDoc is a workspace as version 3 saved it: discovered records with their last observed values, mixed in
// with what a person declared.
const oldDoc = `{
 "schemaVersion": 3,
 "clusters": [
  {"id":"cl-1","orgId":"o","source":"discovered","name":"edge-a","tier":"edge","agentId":"ag-1","lastSeen":"2026-01-01T00:00:00Z","siteId":"site-1","overrides":{"trustZone":"restricted"}},
  {"id":"cl-2","orgId":"o","source":"discovered","name":"cloud","tier":"cloud","agentId":"ag-2","stale":true},
  {"id":"cl-m","orgId":"o","source":"manual","name":"my-lab","tier":"edge"}
 ],
 "nodes": [
  {"id":"nd-1","source":"discovered","clusterId":"cl-1","name":"n1","cpu":8,"deletedAt":"2026-01-02T00:00:00Z"},
  {"id":"nd-m","source":"manual","clusterId":"cl-m","name":"pi"}
 ],
 "namespaces": [{"id":"ns-1","source":"discovered","clusterId":"cl-1","name":"shop","applicationId":"app-1"}],
 "services": [
  {"id":"sv-1","source":"discovered","clusterId":"cl-1","name":"checkout","replicas":3,"applicationId":"app-1","overrides":{"sensitivity":"confidential"}},
  {"id":"sv-2","source":"discovered","clusterId":"cl-1","name":"cart","replicas":1}
 ],
 "applications": [{"id":"app-1","name":"Shop","source":"discovered","origin":"helm","confidence":"high","lastSeen":"x","evidence":{"grouping":{"signal":"s"}},"agentId":"ag-1"}],
 "devices": [{"id":"dev-1","name":"cam","source":"manual"}],
 "sites": [{"id":"site-1","name":"Patras","country":"GR"}],
 "siteLinks": [{"id":"l1","a":"site-1","b":"site-2","source":"declared","rttMs":9},{"id":"l2","a":"site-1","b":"site-2","source":"measured","measuredAt":"t"}],
 "dependencies": [
  {"id":"d1","from":"sv-1","to":"sv-2","sources":["manual"],"stale":true},
  {"id":"d2","from":"sv-1","to":"sv-2","sources":["observed"],"bytes":10},
  {"id":"d3","from":"sv-1","to":"sv-2","sources":["manual","observed"],"connections":4}
 ],
 "externalEndpoints": [{"id":"ext-1","host":"api.example","source":"discovered","lastSeen":"t"}],
 "suggestions": [{"id":"sg-1","status":"open"},{"id":"sg-2","status":"accepted"},{"id":"sg-3","status":"dismissed"}],
 "agents": [{"id":"ag-1"}],
 "auditLog": [{"id":"ev-1","action":"x"},{"id":"au-9","action":"server"}],
 "savedViews": [{"id":"v1","name":"n","params":"a=b"}]
}`

func decode(t *testing.T, b []byte) map[string]json.RawMessage {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func ids(t *testing.T, raw json.RawMessage) []string {
	t.Helper()
	var l []map[string]any
	if err := json.Unmarshal(raw, &l); err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, r := range l {
		out = append(out, r["id"].(string))
	}
	return out
}

func TestDeclareStripsDiscoveredAndKeepsWhatPeopleSaid(t *testing.T) {
	out, rep, err := Declare([]byte(oldDoc))
	if err != nil {
		t.Fatal(err)
	}
	d := decode(t, out)
	if string(d["schemaVersion"]) != "4" {
		t.Fatalf("schemaVersion = %s", d["schemaVersion"])
	}
	if got := ids(t, d["clusters"]); len(got) != 1 || got[0] != "cl-m" {
		t.Fatalf("clusters left: %v", got)
	}
	if got := ids(t, d["nodes"]); len(got) != 1 || got[0] != "nd-m" {
		t.Fatalf("nodes left: %v", got)
	}
	if got := ids(t, d["services"]); len(got) != 0 {
		t.Fatalf("services left: %v", got)
	}
	// Nothing a discovery run reported survives anywhere in the document.
	for _, banned := range []string{`"checkout"`, `"edge-a"`, `"replicas"`, `"agentId"`, `"lastSeen"`, `"evidence"`, `"tier":"cloud"`, `"stale"`, `"connections"`, `measuredAt`, `"cpu":8`} {
		if strings.Contains(string(out), banned) {
			t.Errorf("document still carries %s:\n%s", banned, out)
		}
	}
	var refs map[string]Ref
	if err := json.Unmarshal(d["refs"], &refs); err != nil {
		t.Fatal(err)
	}
	// Only records a person touched leave a ref: cl-1 (site + override), ns-1 and sv-1 (application), not cl-2, nd-1 or sv-2.
	if len(refs) != 3 {
		t.Fatalf("refs = %v", refs)
	}
	if refs["cl-1"].SiteID != "site-1" || refs["cl-1"].Overrides["trustZone"] != "restricted" || refs["cl-1"].Kind != "cluster" {
		t.Errorf("cl-1 ref = %+v", refs["cl-1"])
	}
	if refs["sv-1"].ApplicationID != "app-1" || refs["sv-1"].Overrides["sensitivity"] != "confidential" {
		t.Errorf("sv-1 ref = %+v", refs["sv-1"])
	}
	if refs["ns-1"].ApplicationID != "app-1" {
		t.Errorf("ns-1 ref = %+v", refs["ns-1"])
	}
	// Declared things are all still there.
	if got := ids(t, d["sites"]); len(got) != 1 {
		t.Errorf("sites: %v", got)
	}
	if got := ids(t, d["devices"]); len(got) != 1 {
		t.Errorf("devices: %v", got)
	}
	if got := ids(t, d["applications"]); len(got) != 1 {
		t.Errorf("applications: %v", got)
	}
	if got := ids(t, d["externalEndpoints"]); len(got) != 1 {
		t.Errorf("external endpoints: %v", got)
	}
	if got := ids(t, d["suggestions"]); len(got) != 2 {
		t.Errorf("decided suggestions kept, open ones dropped: %v", got)
	}
	if got := ids(t, d["siteLinks"]); len(got) != 1 || got[0] != "l1" {
		t.Errorf("site links: %v", got)
	}
	if got := ids(t, d["dependencies"]); len(got) != 2 || got[0] != "d1" || got[1] != "d3" {
		t.Errorf("dependencies: %v", got)
	}
	if string(d["agents"]) != "[]" {
		t.Errorf("agents = %s", d["agents"])
	}
	if got := ids(t, d["auditLog"]); len(got) != 1 || got[0] != "ev-1" {
		t.Errorf("auditLog: %v", got)
	}
	if !rep.Changed() || rep.From != 3 || rep.Stripped["cluster"] != 2 || rep.Stripped["node"] != 1 || rep.Stripped["service"] != 2 || rep.Refs != 3 {
		t.Errorf("report = %+v", rep)
	}
	if n := rep.Note(); !strings.Contains(n, "Removed 6 discovered records") || !strings.Contains(n, "3 overrides") {
		t.Errorf("note = %q", n)
	}
}

func TestDeclareIsIdempotentAndCleanDocumentsAreQuiet(t *testing.T) {
	once, _, err := Declare([]byte(oldDoc))
	if err != nil {
		t.Fatal(err)
	}
	twice, rep, err := Declare(once)
	if err != nil {
		t.Fatal(err)
	}
	if string(once) != string(twice) {
		t.Errorf("not idempotent:\n%s\n%s", once, twice)
	}
	if rep.Changed() || rep.Note() != "" {
		t.Errorf("a clean document must produce no note, got %+v %q", rep, rep.Note())
	}
	if rep.From != 4 {
		t.Errorf("from = %d", rep.From)
	}
}

func TestDeclareKeepsExistingRefsForRecordsThatAreGone(t *testing.T) {
	doc := `{"schemaVersion":4,"clusters":[],"refs":{"cl-old":{"kind":"cluster","siteId":"s"},"cl-empty":{"kind":"cluster"}}}`
	out, _, err := Declare([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	var refs map[string]Ref
	_ = json.Unmarshal(decode(t, out)["refs"], &refs)
	if len(refs) != 1 || refs["cl-old"].SiteID != "s" {
		t.Errorf("refs = %v (an orphaned assignment must be kept; an empty one is noise)", refs)
	}
}

func TestDeclareRefusesNewerAndMalformed(t *testing.T) {
	_, _, err := Declare([]byte(`{"schemaVersion":5,"clusters":[]}`))
	var nw ErrNewer
	if !errors.As(err, &nw) || nw.Have != 5 || !strings.Contains(err.Error(), "newer than the newest this server understands (4)") {
		t.Fatalf("err = %v", err)
	}
	for _, bad := range []string{`[]`, `null`, `{"clusters":[]}`, `{"schemaVersion":"x"}`, `{"schemaVersion":0}`, `nope`} {
		if _, _, err := Declare([]byte(bad)); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
}

func TestPeek(t *testing.T) {
	if Peek([]byte(`{"schemaVersion":3}`)) != 3 || Peek([]byte(`{}`)) != 0 || Peek([]byte(`x`)) != 0 {
		t.Error("Peek")
	}
}
