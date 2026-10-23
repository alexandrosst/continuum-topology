package fusionpages

import (
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

// One person cannot fill the table: past the cap their oldest page session goes, and others are not touched.
func TestAPersonHoldsAtMostSomePageSessionsAndTheOldestGoesFirst(t *testing.T) {
	f := NewAccess()
	now := time.Now()
	var secrets []string
	for i := 0; i < MaxGrantsPerUser+4; i++ {
		secret, _, out := f.RedeemAndOpen(f.Ticket("u-1", "dana", now), now)
		if out != Opened {
			t.Fatalf("open %d: %v", i, out)
		}
		secrets = append(secrets, secret)
	}
	other, _, _ := f.RedeemAndOpen(f.Ticket("u-2", "olga", now), now)
	if n := len(f.sessions); n != MaxGrantsPerUser+1 {
		t.Fatalf("%d sessions held, want %d for dana and 1 for olga", n, MaxGrantsPerUser)
	}
	for i, s := range secrets {
		_, ok := f.Session(s, now)
		if want := i >= 4; ok != want {
			t.Errorf("dana's session %d alive = %v, want %v (the 4 oldest go)", i, ok, want)
		}
	}
	if _, ok := f.Session(other, now); !ok {
		t.Error("another person's session was evicted for dana's")
	}
	// Tickets are capped the same way.
	for i := 0; i < MaxGrantsPerUser+3; i++ {
		f.Ticket("u-3", "sam", now)
	}
	n := 0
	for _, g := range f.tickets {
		if g.UserID == "u-3" {
			n++
		}
	}
	if n != MaxGrantsPerUser {
		t.Fatalf("%d tickets held for one person, want %d", n, MaxGrantsPerUser)
	}
}

// Expired entries are cleared out as time goes by, not only once the table is full.
func TestExpiredPageGrantsAreSweptWithoutTheTableBeingFull(t *testing.T) {
	f := NewAccess()
	now := time.Now()
	for i := 0; i < 5; i++ {
		f.Ticket("u-"+string(rune('a'+i)), "x", now)
		f.RedeemAndOpen(f.Ticket("u-"+string(rune('a'+i)), "x", now), now)
	}
	later := now.Add(SessionTTL + time.Minute)
	f.Ticket("u-new", "x", later)
	if len(f.tickets) != 1 || len(f.sessions) != 0 {
		t.Fatalf("after the sweep: %d tickets and %d sessions left, want 1 and 0", len(f.tickets), len(f.sessions))
	}
}

func TestFusionProxyPolicy(t *testing.T) {
	for _, tc := range []struct {
		component, method, path string
		status                  int
		allow                   string
	}{
		// Prometheus: read with GET and HEAD; POST only where a long expression needs it.
		{"metrics", "GET", "/graph", 0, ""},
		{"metrics", "HEAD", "/api/v1/query", 0, ""},
		{"metrics", "GET", "/api/v1/label/job/values", 0, ""},
		{"metrics", "GET", "/api/v1/status/config", 0, ""},
		{"metrics", "POST", "/api/v1/query", 0, ""},
		{"metrics", "POST", "/api/v1/query_range", 0, ""},
		{"metrics", "POST", "/api/v1/series", 0, ""},
		{"metrics", "POST", "/api/v1/labels", 0, ""},
		{"metrics", "POST", "/api/v1/query_exemplars", 0, ""},
		{"metrics", "POST", "/graph", 405, "GET, HEAD"},
		{"metrics", "POST", "/api/v1/label/job/values", 405, "GET, HEAD"},
		{"metrics", "PUT", "/api/v1/query", 405, "GET, HEAD, POST"},
		{"metrics", "DELETE", "/api/v1/series", 405, "GET, HEAD, POST"},
		{"metrics", "OPTIONS", "/api/v1/query", 405, "GET, HEAD, POST"},
		{"metrics", "TRACE", "/graph", 405, "GET, HEAD"},
		// ... and never the doors that write, reconfigure or stop it, by any method.
		{"metrics", "POST", "/api/v1/otlp/v1/metrics", 403, ""},
		{"metrics", "GET", "/api/v1/otlp/v1/metrics", 403, ""},
		{"metrics", "POST", "/api/v1/write", 403, ""},
		{"metrics", "POST", "/api/v1/read", 403, ""},
		{"metrics", "POST", "/api/v1/admin/tsdb/delete_series", 403, ""},
		{"metrics", "PUT", "/api/v1/admin/tsdb/snapshot", 403, ""},
		{"metrics", "GET", "/api/v1/admin/tsdb/snapshot", 403, ""},
		{"metrics", "POST", "/-/reload", 403, ""},
		{"metrics", "PUT", "/-/quit", 403, ""},
		{"metrics", "GET", "/-/healthy", 403, ""},
		{"metrics", "GET", "/-", 403, ""},
		{"metrics", "GET", "/debug/pprof/heap", 403, ""},
		// Grafana saves what it is given, so it takes the methods a web page uses; Live is off, and stays unreachable.
		{"grafana", "GET", "/fusion/grafana/d/abc", 0, ""},
		{"grafana", "POST", "/fusion/grafana/api/ds/query", 0, ""},
		{"grafana", "PUT", "/fusion/grafana/api/dashboards/uid/abc", 0, ""},
		{"grafana", "PATCH", "/fusion/grafana/api/user/preferences", 0, ""},
		{"grafana", "DELETE", "/fusion/grafana/api/dashboards/uid/abc", 0, ""},
		{"grafana", "OPTIONS", "/fusion/grafana/api/ds/query", 405, "GET, HEAD, POST, PUT, PATCH, DELETE"},
		{"grafana", "TRACE", "/fusion/grafana/", 405, "GET, HEAD, POST, PUT, PATCH, DELETE"},
		{"grafana", "GET", "/fusion/grafana/api/live/ws", 403, ""},
		{"grafana", "POST", "/fusion/grafana/api/live/publish", 403, ""},
	} {
		status, allow := Policy(tc.component, tc.method, tc.path)
		if status != tc.status || allow != tc.allow {
			t.Errorf("%s %s %s: %d %q, want %d %q", tc.component, tc.method, tc.path, status, allow, tc.status, tc.allow)
		}
	}
}

func TestLocalLocation(t *testing.T) {
	up, _ := url.Parse("http://ikhnos-fusion-prometheus.ikhnos.svc:9090")
	for loc, want := range map[string]string{
		"/fusion/prometheus/graph": "/fusion/prometheus/graph",
		"graph":                    "graph",
		"?x=1":                     "?x=1",
		"http://localhost:3000/fusion/grafana/login":            "/fusion/grafana/login",
		"http://ikhnos-fusion-prometheus.ikhnos.svc:9090/graph": "/graph",
		"https://ikhnos.example/fusion/grafana/":                "/fusion/grafana/",
	} {
		if got, err := LocalLocation(loc, "ikhnos.example", up); err != nil || got != want {
			t.Errorf("%q: %q %v, want %q", loc, got, err, want)
		}
	}
	for _, loc := range []string{"https://evil.example/", "//evil.example", `/\evil.example`, "http://%zz", "mailto:a@b", "file:///etc/passwd"} {
		if got, err := LocalLocation(loc, "ikhnos.example", up); err == nil {
			t.Errorf("%q was passed on as %q", loc, got)
		}
	}
}

func TestGrafanaLogin(t *testing.T) {
	for in, want := range map[string]string{
		"admin":           "ikhnos-admin",
		"Alex.Smith@x.io": "ikhnos-alex.smith@x.io", // case only: Ikhnos reads these as one account
		"first_last-2":    "ikhnos-first_last-2",
		"a b":             "ikhnos-a+20+b", // cannot come from a valid username; never reaches the header as it is
		"a+b":             "ikhnos-a+2b+b",
		"ünï":             "ikhnos-+fc+n+ef+",
	} {
		if got := GrafanaLogin(in); got != want {
			t.Errorf("GrafanaLogin(%q) = %q, want %q", in, got, want)
		}
	}
	// No two names share a login, and none can be Grafana's own: every login starts with the prefix.
	seen := map[string]string{}
	for _, n := range []string{"admin", "Admin", "a b", "a+20+b", "a+b", "a+2b+b", "x_y", "x-y"} {
		g := GrafanaLogin(n)
		if !strings.HasPrefix(g, "ikhnos-") {
			t.Errorf("%q: %q has no prefix", n, g)
		}
		if prev, dup := seen[g]; dup && !strings.EqualFold(prev, n) {
			t.Errorf("%q and %q give the same login %q", prev, n, g)
		}
		seen[g] = n
	}
}

// A table with no room for another page session answers "busy" and does not spend the ticket.
func TestAFullTableAnswersBusyAndKeepsTheTicket(t *testing.T) {
	f := NewAccess()
	now := time.Now()
	far := now.Add(24 * time.Hour)
	f.mu.Lock()
	for i := 0; i < MaxGrants; i++ {
		f.sessions[strconv.Itoa(i)] = Grant{UserID: "u-" + strconv.Itoa(i), expires: far}
	}
	f.mu.Unlock()
	ticket := f.Ticket("u-new", "dana", now)
	if ticket == "" {
		t.Fatal("a ticket could not be made while only the sessions table was full")
	}
	if _, _, out := f.RedeemAndOpen(ticket, now); out != Busy {
		t.Fatalf("a full table: %v, want busy", out)
	}
	f.mu.Lock()
	for k := range f.sessions {
		delete(f.sessions, k)
		break
	}
	f.mu.Unlock()
	if _, _, out := f.RedeemAndOpen(ticket, now); out != Opened {
		t.Fatalf("the same ticket once there was room: %v", out)
	}
	if _, _, out := f.RedeemAndOpen(ticket, now); out != Invalid {
		t.Fatalf("the ticket worked twice: %v", out)
	}
}

func TestATicketOnlyCountsOnAGetThatCarriesIt(t *testing.T) {
	get := httptest.NewRequest("GET", "/fusion/grafana/?"+TicketParam+"=abc", nil)
	if got := TicketOf(get); got != "abc" {
		t.Errorf("ticket = %q", got)
	}
	post := httptest.NewRequest("POST", "/fusion/grafana/?"+TicketParam+"=abc", nil)
	if got := TicketOf(post); got != "" {
		t.Errorf("a POST carried a ticket: %q", got)
	}
	if got := TicketOf(httptest.NewRequest("GET", "/fusion/grafana/", nil)); got != "" {
		t.Errorf("ticket = %q", got)
	}
}
