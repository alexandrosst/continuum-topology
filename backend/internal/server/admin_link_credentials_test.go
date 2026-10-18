package server

import (
	"strings"
	"testing"

	"continuum/internal/accounts"
	"continuum/internal/kernel"
)

func TestRotatingLinkCredentialsIsAnAdminActionThatShowsNoSecrets(t *testing.T) {
	a := newAdminRig(t)
	_, admin := a.user(t, "ada", accounts.RoleAdmin)
	_, viewer := a.user(t, "vera", accounts.RoleViewer)
	const path = "/api/v1/links/rotate-credentials"

	if r := a.do("POST", path, map[string]string{"cluster": "cl-b"}); r.Code != 401 {
		t.Fatalf("anonymous: %d", r.Code)
	}
	if r := a.do("POST", path, map[string]string{"cluster": "cl-b"}, withCookie(viewer)); r.Code != 403 {
		t.Fatalf("a viewer: %d", r.Code)
	}

	put := a.do("PUT", "/api/v1/settings", kernel.Settings{Links: []kernel.LinkSetting{{A: "cl-a", B: "cl-b", Throughput: &kernel.LinkThroughput{}}}}, withCookie(admin))
	if put.Code != 200 {
		t.Fatalf("%d %s", put.Code, put.Body.String())
	}
	a.hub().syncLinkCredentials() // the background work a saved change starts, run to the end
	before, err := a.st.ListLinkCredentials(a.ctx, "org-1")
	if err != nil || len(before) != 2 { // the serving cluster's key pair and the pair's login
		t.Fatalf("rows after saving a pair with throughput tests: %d, %v", len(before), err)
	}

	for name, body := range map[string]any{
		"empty":            map[string]string{},
		"cluster and pair": map[string]string{"cluster": "cl-b", "a": "cl-a", "b": "cl-b"},
		"half a pair":      map[string]string{"a": "cl-a"},
	} {
		if r := a.do("POST", path, body, withCookie(admin)); r.Code != 400 {
			t.Errorf("%s: %d %s", name, r.Code, r.Body.String())
		}
	}
	// A request that cannot rotate anything is refused as unprocessable, before anything is audited.
	rows := len(a.auditActions(t))
	for name, body := range map[string]any{
		"a pair not set up":             map[string]string{"a": "cl-a", "b": "cl-z"},
		"a cluster that serves nothing": map[string]string{"cluster": "cl-a"},
		"an unknown cluster":            map[string]string{"cluster": "cl-zzz"},
		"a very long cluster id":        map[string]string{"cluster": strings.Repeat("x", 200)},
		"a very long pair":              map[string]string{"a": strings.Repeat("x", 65), "b": "cl-b"},
	} {
		if r := a.do("POST", path, body, withCookie(admin)); r.Code != 422 {
			t.Errorf("%s: %d %s", name, r.Code, r.Body.String())
		}
	}
	if got := len(a.auditActions(t)); got != rows {
		t.Errorf("refused rotations left %d audit rows", got-rows)
	}

	r := a.do("POST", path, map[string]string{"a": "cl-b", "b": "cl-a"}, withCookie(admin))
	if r.Code != 200 || r.json(t)["rotated"] != true {
		t.Fatalf("rotate a pair: %d %s", r.Code, r.Body.String())
	}
	r2 := a.do("POST", path, map[string]string{"cluster": "cl-b"}, withCookie(admin))
	if r2.Code != 200 {
		t.Fatalf("rotate a cluster: %d %s", r2.Code, r2.Body.String())
	}

	after, _ := a.st.ListLinkCredentials(a.ctx, "org-1")
	changed := 0
	for _, x := range after {
		for _, y := range before {
			if x.Kind == y.Kind && x.ID == y.ID && string(x.Sealed) != string(y.Sealed) {
				changed++
			}
		}
	}
	if changed != 2 {
		t.Errorf("%d rows changed, want the pair's and the cluster's", changed)
	}

	// Nothing secret in the answers or the audit trail.
	creds, err := a.hub().C.Connectivity.LinkCredentials(a.ctx, a.hub().C.Settings().Links, false)
	if err != nil {
		t.Fatal(err)
	}
	var secrets []string
	for _, k := range creds.Clusters {
		secrets = append(secrets, k.Private.Reveal())
	}
	for _, p := range creds.Pairs {
		secrets = append(secrets, p.Password.Reveal())
	}
	if len(secrets) == 0 {
		t.Fatal("no credentials to look for")
	}
	seen := r.Body.String() + r2.Body.String() + strings.Join(a.auditActions(t), "\n")
	for _, s := range secrets {
		if strings.Contains(seen, s) || strings.Contains(seen, s[len(s)/2:]) {
			t.Error("credential material in an answer or the audit trail")
		}
	}
	if !strings.Contains(strings.Join(a.auditActions(t), "\n"), "link-credentials-rotate") {
		t.Errorf("no audit row for the rotation: %v", a.auditActions(t))
	}
	a.chainOK(t)
}
