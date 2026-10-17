package install

import (
	"strings"
	"testing"
)

// Edges the commands depend on that the pinned cases of the web page never reached. They came out of mutating the renderer line by line
// and seeing which changes the tests did not notice; each one is a behaviour the page's own tests used to guard when it wrote the commands.

func TestJSONKeepsHTMLCharactersAndNumbersAsTyped(t *testing.T) {
	p := Processor{ID: "p", Kind: Filter, Name: "raw", Raw: `{"cond": "a > b && c < d", "n": 1.50, "big": 12345678901234567890, "tiny": 1e-7}`}
	got := Bodies([]Processor{p}, false)
	for _, want := range []string{`"cond":"a > b && c < d"`, `"n":1.50`, `"big":12345678901234567890`, `"tiny":1e-7`} {
		if !strings.Contains(got, want) {
			t.Errorf("%s is not as typed in %s", want, got)
		}
	}
}

func TestExactlyTheTagLimitIsAllowed(t *testing.T) {
	var tags []Tag
	for i := 0; i < TagLimit; i++ {
		tags = append(tags, Tag{Key: "k" + string(rune('a'+i)), Value: "v"})
	}
	at := valid()
	at.Processing.Tags = tags
	if p := at.Problems(); len(p) != 0 {
		t.Fatalf("%d tags are allowed, got %v", TagLimit, p)
	}
	over := valid()
	over.Processing.Tags = append(tags, Tag{Key: "one-more", Value: "v"})
	if p := over.Problems(); len(p) != 1 || !strings.Contains(p[0], "At most") {
		t.Fatalf("one more is too many, got %v", p)
	}
}

func TestACredentialOfYourOwnForTheThroughputTestsNeedsBothSwitches(t *testing.T) {
	const key = "telemetry.clusterLinks.probe.capacity.auth.existingSecret"
	for _, c := range []struct {
		probe, throughput, stated bool
	}{{true, true, true}, {true, false, false}, {false, true, false}, {false, false, false}} {
		tel := valid()
		tel.Signals.ClusterLinks = true
		tel.ClusterLinks = ClusterLinks{Probe: c.probe, Throughput: c.throughput, AuthSecret: "own-iperf"}
		v, err := tel.Values()
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Contains(Flags(v), key); got != c.stated {
			t.Errorf("probe %v, throughput %v: credential stated %v, want %v", c.probe, c.throughput, got, c.stated)
		}
	}
}

func TestExtraProcessorsOfAnInstallThatHasTelemetryAreClearedNotStatedEmpty(t *testing.T) {
	tel := valid()
	tel.HadTelemetry = true
	v, err := tel.Values()
	if err != nil {
		t.Fatal(err)
	}
	got := Flags(v)
	// No bodies to state (an empty map would be a processor of no name), and both lists stated whole, empty, so a processor taken out stops running.
	if strings.Contains(got, "telemetry.processors.extraProcessors=") || strings.Contains(got, "telemetry.processors.extraProcessors ") {
		t.Errorf("an empty set of bodies is stated:\n%s", got)
	}
	for _, list := range []string{"extraProcessorNames", "extraTracesProcessorNames"} {
		if !strings.Contains(got, "--set-json telemetry.processors."+list+"='[]'") {
			t.Errorf("%s is not cleared:\n%s", list, got)
		}
	}
	fresh := valid()
	v, _ = fresh.Values()
	if strings.Contains(Flags(v), "extraProcessor") {
		t.Errorf("a fresh install with none states nothing about them:\n%s", Flags(v))
	}
}

func TestTheScopeInfrastructureFollowsIsAlwaysAListInOrder(t *testing.T) {
	tel := valid()
	tel.Signals.Traces, tel.Signals.ApplicationLogs = true, true
	tel.Scopes.Infrastructure = true
	// Excluded by every signal, given out of order and twice: sorted, once each.
	tel.Scopes.Traces = ScopeOverride{Exclude: []string{"zeta", "alpha", "alpha", "only-traces"}}
	tel.Scopes.ApplicationLogs = ScopeOverride{Exclude: []string{"alpha", "zeta", "zeta"}}
	v, err := tel.Values()
	if err != nil {
		t.Fatal(err)
	}
	if got := Flags(v); !strings.Contains(got, `telemetry.scope.infra.exclude='{alpha,zeta}'`) {
		t.Errorf("excluded by both, sorted and once:\n%s", got)
	}
	// Narrowed by neither's exclude list: an empty list, never null.
	tel.Scopes.Traces = ScopeOverride{Exclude: nil, Namespaces: []string{"shop"}}
	tel.Scopes.ApplicationLogs = ScopeOverride{Exclude: []string{"x"}}
	v, _ = tel.Values()
	if got := Flags(v); !strings.Contains(got, `--set-json telemetry.scope.infra.exclude='[]'`) || strings.Contains(got, "null") {
		t.Errorf("nothing excluded by all is the empty list:\n%s", got)
	}
}

func TestAScopeIsActiveWhenAnyPartOfItIsSet(t *testing.T) {
	for _, c := range []struct {
		s    Scope
		want bool
	}{
		{Scope{}, false}, {Scope{Selector: "  "}, false}, {Scope{Namespaces: []string{"a"}}, true}, {Scope{Exclude: []string{"a"}}, true}, {Scope{Selector: "a=b"}, true},
	} {
		if got := c.s.Active(); got != c.want {
			t.Errorf("%+v: active %v, want %v", c.s, got, c.want)
		}
	}
}

func TestABlockWithNothingStatedByTheServerEndsWhereTheTelemetryEnds(t *testing.T) {
	spec := valid()
	spec.Destination.Operator = "op-eu"
	block, err := OperatorTelemetry{AgentUpgrade: AgentUpgrade{Telemetry: &spec}}.Render()
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasSuffix(strings.TrimRight(block, " "), "\\") || strings.HasSuffix(block, "\\\n  ") {
		t.Errorf("a dangling line continuation:\n%q", block[len(block)-40:])
	}
}

// A destination that is a regional operator changes nothing in the command but the client-certificate lines: its receiver is mutual TLS,
// which only the server's own values (it holds the Secret) can state, so the telemetry's must not contradict them. An ordinary destination
// states "no client certificate" so one an earlier command installed does not linger.
func TestAnOperatorDestinationOnlyLeavesOutTheClientCertificateLines(t *testing.T) {
	certificate := func(line string) bool {
		return strings.Contains(line, "tls.mtls") || strings.Contains(line, "tls.serverName") || strings.Contains(line, "tls.caFile")
	}
	split := func(v []Value) (cert, rest []string) {
		for _, x := range v {
			if certificate(x.Key) {
				cert = append(cert, x.Key)
			} else {
				rest = append(rest, x.Key+"="+x.Value)
			}
		}
		return
	}
	var drafts []Telemetry
	drafts = append(drafts, valid())
	traces := valid()
	traces.Signals = Signals{Traces: true}
	traces.Scopes.Traces = ScopeOverride{Namespaces: []string{"shop"}, Exclude: []string{"kube-system"}}
	traces.Destination = Destination{Endpoint: "x.example.com:4318", Protocol: "http", Insecure: true}
	drafts = append(drafts, traces)
	auth := valid()
	auth.Destination.AuthHeader, auth.Destination.AuthSecret, auth.Destination.AuthKey = "x-api-key", "tok", "k"
	drafts = append(drafts, auth)
	for i, d := range drafts {
		plain, err := d.Values()
		if err != nil {
			t.Fatal(err)
		}
		op := d
		op.Destination.Operator = "op-eu"
		forOperator, err := op.Values()
		if err != nil {
			t.Fatal(err)
		}
		plainCert, plainRest := split(plain)
		opCert, opRest := split(forOperator)
		if len(opCert) != 0 {
			t.Errorf("draft %d: an operator destination states %v", i, opCert)
		}
		if len(plainCert) == 0 {
			t.Errorf("draft %d: an ordinary destination does not clear the client certificate it may have had", i)
		}
		if strings.Join(plainRest, "\n") != strings.Join(opRest, "\n") {
			t.Errorf("draft %d: the operator id changed more than the certificate lines:\n%s\nvs\n%s", i, strings.Join(plainRest, "\n"), strings.Join(opRest, "\n"))
		}
		// The credential Secrets are the same either way.
		if got, want := len(op.CredentialSecrets("")), len(d.CredentialSecrets("")); got != want {
			t.Errorf("draft %d: %d credential Secrets for an operator, %d otherwise", i, got, want)
		}
	}
}
