package estate

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/accounts"
	"continuum/internal/agents"
	"continuum/internal/kernel"
	"continuum/internal/store"
	"continuum/internal/testkit"

	"google.golang.org/protobuf/encoding/protojson"
)

// ConfigFor is the one function that assembles every agent's Config from consent, link pairs, credentials, probe targets and the
// agent's tier. It is the code the config-channel work will move piece by piece, and the rule for that work is that the Config
// every agent gets does not change. This test is that rule written down: it takes three clusters through a series of settings,
// and records the whole Config each of them is sent after every step in testdata/config_matrix.golden.
//
// What cannot be compared is made comparable and nothing else is: the cluster ids are replaced by their names (they are random per
// run), the server's clock is zeroed, and the digest and the iperf credentials, which are random per run, are replaced by a marker
// that says whether they are present. The test then checks separately that a digest is present.
//
// To accept a change on purpose: go test ./internal/server -run TestTheConfigEachClusterIsSentIsPinned -update
func TestTheConfigEachClusterIsSentIsPinned(t *testing.T) {
	cr := newCredRig(t)
	cr.core.OnSettings = nil // the push a save starts runs in the background and would race the reads
	names := map[string]string{cr.first.ClusterID: "first", cr.second.ClusterID: "second", cr.third.ClusterID: "third"}
	byName := map[string]store.Agent{"first": cr.first, "second": cr.second, "third": cr.third}
	if len(names) != 3 {
		t.Fatal("the three clusters must differ")
	}
	var out bytes.Buffer
	step := func(title string) {
		t.Helper()
		cr.settle()
		out.WriteString("== " + title + "\n")
		for _, who := range []string{"first", "second", "third"} {
			a, err := cr.st.GetAgent(cr.ctx, byName[who].ID) // the agent as the store now has it: its tier may have changed
			if err != nil {
				t.Fatal(err)
			}
			cfg := cr.cfg(a)
			if cfg.Digest == "" {
				t.Fatalf("%s: %s: the Config carries no digest", title, who)
			}
			out.WriteString("-- " + who + "\n" + pinnedConfig(t, cfg, names) + "\n")
		}
	}
	save := func(s kernel.Settings) {
		t.Helper()
		if _, err := cr.core.SaveSettings(cr.ctx, "alex", s); err != nil {
			t.Fatal(err)
		}
	}
	consent := func(who string, c agents.Consent) {
		t.Helper()
		if _, err := cr.hub.SetConsent(testkit.AsRole(accounts.RoleAdmin), "alex", byName[who].ID, c); err != nil {
			t.Fatal(err)
		}
	}
	pair := func(th *kernel.LinkThroughput, via string, noProbe bool) kernel.LinkSetting {
		return kernel.LinkSetting{A: cr.first.ClusterID, B: cr.second.ClusterID, Throughput: th, Via: kernel.LinkVia(via), NoProbe: noProbe}
	}

	step("nothing asked for")

	targets := []kernel.ProbeTarget{{ID: "pt-1", ClusterID: cr.first.ClusterID, Label: "gateway", Host: "203.0.113.10", Port: 443}}
	save(kernel.Settings{MeasureSeconds: 90, ProbeTargets: targets})
	step("a probe target for the first cluster, measured every 90 seconds")

	save(kernel.Settings{MeasureSeconds: 90, ProbeTargets: targets, Links: []kernel.LinkSetting{pair(nil, "", false)}})
	step("a link pair between the first and the second")

	save(kernel.Settings{MeasureSeconds: 90, ProbeTargets: targets, Links: []kernel.LinkSetting{pair(&kernel.LinkThroughput{IntervalMinutes: 60, BudgetMB: 2048}, "mesh", false)}})
	cr.settle()
	step("the pair also asks for throughput tests, pinned to the mesh addresses")

	save(kernel.Settings{MeasureSeconds: 90, ProbeTargets: targets, Links: []kernel.LinkSetting{pair(nil, "public", true)}})
	step("the pair with its one-way probe off, pinned to the public addresses")

	save(kernel.Settings{MeasureSeconds: 90, ProbeTargets: targets, Links: []kernel.LinkSetting{pair(&kernel.LinkThroughput{}, "", false)}})
	consent("first", agents.Consent{Paused: []string{"flow"}})
	step("the first cluster's flow collector paused")

	consent("second", agents.Consent{Paused: []string{"flow"}})
	step("the second cluster's flow collector paused as well")

	consent("first", agents.Consent{Paused: []string{"flow", "measure"}, Excluded: []string{"payments", "monitoring"}})
	step("the first cluster also asked to stop measuring, and to leave two namespaces out")

	if _, err := cr.hub.SetAgentTier(cr.ctx, "alex", cr.second.ID, 1, func(int) string { return "" }); err != nil {
		t.Fatal(err)
	}
	step("the second cluster's approved tier lowered to 1")

	const golden = "testdata/config_matrix.golden"
	if *updateGolden {
		if err := os.WriteFile(golden, out.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("%v (create it with -update)", err)
	}
	if !bytes.Equal(want, out.Bytes()) {
		t.Errorf("the Config some cluster is sent changed. The difference, as lines only in one side:\n%s\nIf this is intended, run: go test ./internal/server -run TestTheConfigEachClusterIsSentIsPinned -update", testkit.LineDiff(string(want), out.String()))
	}
}

// pinnedConfig writes a Config as indented JSON with sorted keys (protojson alone is not stable between runs), with the
// run-dependent parts made comparable as the test above says.
func pinnedConfig(t *testing.T, cfg *continuumv1.Config, names map[string]string) string {
	t.Helper()
	raw, err := protojson.MarshalOptions{UseProtoNames: true, EmitUnpopulated: false}.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	delete(m, "server_time_unix")
	m["digest"] = "(present)"
	if peers, ok := m["link_peers"].([]any); ok {
		for _, p := range peers {
			if ic, ok := p.(map[string]any)["iperf_client"].(map[string]any); ok {
				for k := range ic {
					ic[k] = "(present)"
				}
			}
		}
	}
	if is, ok := m["link_iperf_server"].(map[string]any); ok {
		for k := range is {
			is[k] = "(present)"
		}
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for id, name := range names {
		s = strings.ReplaceAll(s, id, "cluster:"+name)
	}
	return s
}
