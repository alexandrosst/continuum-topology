package server

import (
	"context"
	"encoding/binary"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"continuum/internal/store"
)

func TestDeciderPolicyDeniesInternalAddressesByDefault(t *testing.T) {
	var p *DeciderPolicy // the default policy
	denied := []string{
		"127.0.0.1", "127.255.255.254", "::1", "10.0.0.1", "172.16.5.5", "172.31.255.255", "192.168.1.1",
		"100.64.0.1", "100.127.255.254", // CGNAT
		"fc00::1", "fd12:3456::1", // ULA
		"169.254.169.254", "169.254.170.2", "fe80::1", "fd00:ec2::254", "168.63.129.16", "100.100.100.200",
		"0.0.0.0", "::", "224.0.0.1", "ff02::1", "255.255.255.255",
		"::ffff:127.0.0.1", "::ffff:10.1.2.3", "::ffff:169.254.169.254", "::ffff:100.64.0.9", // IPv4-mapped
		"64:ff9b::7f00:1", "64:ff9b::a9fe:a9fe", // NAT64 wrapping loopback and the metadata address
		"2002:7f00:1::1", // 6to4 wrapping loopback
		"::127.0.0.1",    // deprecated IPv4-compatible form
		"fe80::1%eth0",
	}
	for _, s := range denied {
		if err := p.CheckAddr(netip.MustParseAddr(s)); err == nil {
			t.Errorf("%s was allowed by the default policy", s)
		}
	}
	for _, s := range []string{"93.184.216.34", "8.8.8.8", "2606:2800:220:1:248:1893:25c8:1946", "100.63.255.255", "100.128.0.1", "172.32.0.1", "::ffff:8.8.8.8"} {
		if err := p.CheckAddr(netip.MustParseAddr(s)); err != nil {
			t.Errorf("%s is public but was refused: %v", s, err)
		}
	}
}

func TestDeciderAllowListPermitsPrivateRangesButNeverMetadata(t *testing.T) {
	p, err := NewDeciderPolicy("10.0.0.0/8, 127.0.0.1, fd00::/8, 169.254.0.0/16, 168.63.129.16/32, ::ffff:192.168.0.0/112")
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"10.9.8.7", "127.0.0.1", "fd12::5", "192.168.4.4", "::ffff:10.0.0.1"} {
		if err := p.CheckAddr(netip.MustParseAddr(s)); err != nil {
			t.Errorf("%s is in the allow-list but was refused: %v", s, err)
		}
	}
	for _, s := range []string{"127.0.0.2", "172.16.0.1", "100.64.0.1", "fc00::1", "::1"} {
		if err := p.CheckAddr(netip.MustParseAddr(s)); err == nil {
			t.Errorf("%s is outside the allow-list but was allowed", s)
		}
	}
	// A wide or even direct allow-list entry cannot open the cloud metadata services.
	for _, s := range []string{"169.254.169.254", "fd00:ec2::254", "168.63.129.16", "::ffff:169.254.169.254", "64:ff9b::a9fe:a9fe", "0.0.0.0", "224.0.0.251", "100.100.100.200"} {
		if err := p.CheckAddr(netip.MustParseAddr(s)); err == nil {
			t.Errorf("%s was allowed although the allow-list names it: metadata addresses are never allowed", s)
		}
	}
	all, _ := NewDeciderPolicy("0.0.0.0/0,::/0")
	if err := all.CheckAddr(netip.MustParseAddr("169.254.169.254")); err == nil {
		t.Error("an allow-everything list opened the metadata address")
	}
	for _, bad := range []string{"10.0.0.0/33", "not-an-address", "10.0.0.0/8;evil"} {
		if _, err := NewDeciderPolicy(bad); err == nil || !strings.Contains(err.Error(), "--decider-allow-cidrs") {
			t.Errorf("%q: expected a clear error, got %v", bad, err)
		}
	}
	if p, err := NewDeciderPolicy(""); err != nil || len(p.Allowed()) != 0 {
		t.Errorf("an empty list is the default policy: %v %v", p, err)
	}
}

func TestDeciderURLIsCheckedWhenSaved(t *testing.T) {
	p := &DeciderPolicy{lookup: func(_ context.Context, host string) ([]netip.Addr, error) {
		switch host {
		case "internal.example":
			return []netip.Addr{netip.MustParseAddr("93.184.216.34"), netip.MustParseAddr("10.0.0.5")}, nil
		case "public.example":
			return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
		}
		return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	}}
	ctx := context.Background()
	for _, u := range []string{
		"http://127.0.0.1:8080/d", "http://[::1]/d", "http://10.0.0.1/d", "http://172.20.0.1/d", "http://192.168.0.1/d", "http://100.64.1.1/d",
		"http://[fd00::1]/d", "http://169.254.169.254/latest", "http://[fd00:ec2::254]/", "http://168.63.129.16/", "http://[::ffff:127.0.0.1]/",
		"http://internal.example/d",                                  // one of its addresses is private
		"http://2130706433/", "http://0x7f.1/", "http://0177.0.0.1/", // numeric spellings of loopback
		"http://u:p@public.example/", "ftp://public.example/", "http:///x",
	} {
		if err := p.CheckURL(ctx, u); err == nil {
			t.Errorf("%s was accepted", u)
		}
	}
	if err := p.CheckURL(ctx, "https://public.example/decide?key=1"); err != nil {
		t.Errorf("a public decider was refused: %v", err)
	}
	if err := p.CheckURL(ctx, "https://not-yet-resolvable.example/"); err != nil {
		t.Errorf("a name that does not resolve yet may be saved (the dial-time check still applies): %v", err)
	}
	err := p.CheckURL(ctx, "http://10.0.0.1/d")
	if err == nil || !strings.Contains(err.Error(), "--decider-allow-cidrs") {
		t.Errorf("the message must tell the operator what to do: %v", err)
	}
}

func TestStoredDeciderThatIsNoLongerAllowedIsSwitchedOffAlone(t *testing.T) {
	e := newEnv(t)
	e.core.Decider, _ = NewDeciderPolicy("127.0.0.0/8")
	if _, err := e.core.SaveSettings(e.ctx, "alex", Settings{SnapshotMinutes: 9, DeciderURL: "http://127.0.0.1:9/d"}); err != nil {
		t.Fatal(err)
	}
	c2 := NewCore(e.st, e.core.CA, "org-1", nil) // a restart without --decider-allow-cidrs
	c2.LoadSettings(e.ctx)
	if got := c2.Settings(); got.DeciderURL != "" || got.SnapshotMinutes != 9 {
		t.Fatalf("only the decider should have been switched off: %+v", got)
	}
}

// rigWithDecider is an organisation with an editor, a viewer and an admin, and a decider that counts its calls.
func rigWithDecider(t *testing.T, policy string) (a *adminRig, admin, editor, viewer string, calls *atomic.Int32, url string) {
	t.Helper()
	a = newAdminRig(t)
	p, err := NewDeciderPolicy(policy)
	if err != nil {
		t.Fatal(err)
	}
	a.base.Decider = p
	_, admin = a.user(t, "root", RoleAdmin)
	_, editor = a.user(t, "ed", RoleEditor)
	_, viewer = a.user(t, "eve", RoleViewer)
	calls = &atomic.Int32{}
	dec := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(`{"recommendations":[]}`))
	}))
	t.Cleanup(dec.Close)
	return a, admin, editor, viewer, calls, dec.URL
}

func TestDecideNeedsAnEditorAndEveryCallIsAudited(t *testing.T) {
	a, admin, editor, viewer, calls, url := rigWithDecider(t, "127.0.0.0/8")
	if r := a.do("PUT", "/api/v1/settings", Settings{DeciderURL: url + "/decide?key=SECRET"}, withCookie(admin)); r.Code != 200 {
		t.Fatalf("settings: %d %s", r.Code, r.Body.String())
	}
	if r := a.do("POST", "/api/v1/decide", map[string]any{"schema": 1}, withCookie(viewer)); r.Code != 403 {
		t.Fatalf("a viewer used the decider: %d", r.Code)
	}
	if calls.Load() != 0 {
		t.Fatal("the decider was called on behalf of a viewer")
	}
	body := map[string]any{"schema": 1, "facts": strings.Repeat("x", 100)}
	if r := a.do("POST", "/api/v1/decide", body, withCookie(editor)); r.Code != 200 {
		t.Fatalf("an editor was refused: %d %s", r.Code, r.Body.String())
	}
	if r := a.do("POST", "/api/v1/decide", []byte("nope"), withCookie(admin)); r.Code != 400 {
		t.Fatalf("bad body: %d", r.Code)
	}
	evs, _ := a.st.ListAudit(a.ctx, "org-1", 50)
	var rows []store.AuditEvent
	for _, e := range evs {
		if e.Action == "decide" {
			rows = append(rows, e)
		}
	}
	if len(rows) != 2 {
		t.Fatalf("want one audit row for each of the two calls that reached the handler, got %d: %+v", len(rows), rows)
	}
	for _, e := range rows {
		if strings.Contains(e.Detail+e.TargetID, "SECRET") || strings.Contains(e.Detail+e.TargetID, "/decide") {
			t.Fatalf("more than the host was audited: %+v", e)
		}
		if e.TargetID != "127.0.0.1" || !strings.Contains(e.Detail, "org org-1") {
			t.Fatalf("host or org missing: %+v", e)
		}
	}
	// newest first: the bad body (400, 4 bytes) then the good call (200)
	if !strings.Contains(rows[0].Detail, "status 400") || !strings.Contains(rows[0].Detail, "request 4 bytes") || rows[0].Actor != "root" {
		t.Fatalf("bad call row: %+v", rows[0])
	}
	if !strings.Contains(rows[1].Detail, "status 200") || rows[1].Actor != "ed" || !strings.Contains(rows[1].Detail, "request 1") {
		t.Fatalf("good call row: %+v", rows[1])
	}
}

func TestDecideRefusesAPrivateTargetAtDialTimeEvenIfSavedEarlier(t *testing.T) {
	a, admin, editor, _, calls, url := rigWithDecider(t, "127.0.0.0/8")
	if r := a.do("PUT", "/api/v1/settings", Settings{DeciderURL: url}, withCookie(admin)); r.Code != 200 {
		t.Fatal(r.Body.String())
	}
	a.base.Decider.allow = nil // the operator restarts without the allow-list (or a name now points inside)
	r := a.do("POST", "/api/v1/decide", map[string]any{"schema": 1}, withCookie(editor))
	if r.Code != 502 || !strings.Contains(r.Body.String(), "--decider-allow-cidrs") {
		t.Fatalf("private target: %d %s", r.Code, r.Body.String())
	}
	if calls.Load() != 0 {
		t.Fatal("the server connected to a private address")
	}
}

func TestSavingAPrivateOrMetadataDeciderIsRefusedWithAClearMessage(t *testing.T) {
	a, admin, _, _, _, url := rigWithDecider(t, "") // default policy
	for name, u := range map[string]string{"loopback": url, "private": "http://10.1.1.1/d", "metadata": "http://169.254.169.254/", "ula": "http://[fd00::5]/"} {
		r := a.do("PUT", "/api/v1/settings", Settings{DeciderURL: u}, withCookie(admin))
		if r.Code != 400 || !strings.Contains(r.Body.String(), "not allowed") {
			t.Errorf("%s: %d %s", name, r.Code, r.Body.String())
		}
	}
	// Even with the range allowed, the metadata address stays refused.
	a2, admin2, _, _, _, _ := rigWithDecider(t, "0.0.0.0/0,169.254.0.0/16")
	if r := a2.do("PUT", "/api/v1/settings", Settings{DeciderURL: "http://169.254.169.254/latest"}, withCookie(admin2)); r.Code != 400 {
		t.Fatalf("metadata although allow-listed: %d", r.Code)
	}
}

// fakeDNS answers A queries for any name with whatever answer() returns, and nothing for AAAA.
func fakeDNS(t *testing.T, answer func() net.IP) string {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pc.Close() })
	go func() {
		buf := make([]byte, 512)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			if n < 12 {
				continue
			}
			q := append([]byte(nil), buf[:n]...)
			i := 12
			for i < n && q[i] != 0 {
				i += int(q[i]) + 1
			}
			end := i + 5 // the zero byte, type, class
			if end > n {
				continue
			}
			qtype := binary.BigEndian.Uint16(q[i+1:])
			resp := append([]byte(nil), q[:end]...)
			resp[2], resp[3] = 0x81, 0x80 // response, recursion available, no error
			resp[6], resp[7], resp[8], resp[9], resp[10], resp[11] = 0, 0, 0, 0, 0, 0
			if qtype == 1 {
				resp[7] = 1
				resp = append(resp, 0xc0, 0x0c, 0, 1, 0, 1, 0, 0, 0, 0, 0, 4)
				resp = append(resp, answer().To4()...)
			}
			_, _ = pc.WriteTo(resp, from)
		}
	}()
	return pc.LocalAddr().String()
}

func TestDeciderDNSRebindingIsStoppedAtDialTime(t *testing.T) {
	var target atomic.Value
	target.Store(net.ParseIP("93.184.216.34"))
	dns := fakeDNS(t, func() net.IP { return target.Load().(net.IP) })
	res := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "udp", dns)
	}}
	p := &DeciderPolicy{resolver: res}
	p.lookup = func(ctx context.Context, h string) ([]netip.Addr, error) { return res.LookupNetIP(ctx, "ip", h) }

	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1); _, _ = w.Write([]byte("internal")) }))
	defer srv.Close()
	port := srv.Listener.Addr().(*net.TCPAddr).Port
	u := "http://decider.rebind.test.:" + itoa(int64(port)) + "/x"

	// At save time the name points at a public address: accepted.
	if err := p.CheckURL(context.Background(), u); err != nil {
		t.Fatalf("the public answer should be accepted when saving: %v", err)
	}
	// Later the same name points at the server's own loopback (a rebinding attack).
	target.Store(net.ParseIP("127.0.0.1"))
	c := p.client(3 * time.Second)
	resp, err := c.Get(u)
	if err == nil {
		resp.Body.Close()
		t.Fatal("the connection to a name that now resolves to loopback succeeded")
	}
	if !isDeciderDenied(err) {
		t.Fatalf("refused, but not by the policy: %v", err)
	}
	if hits.Load() != 0 {
		t.Fatal("the internal service received a request")
	}
	// The same name is accepted for a range the operator opened, so the test proves the resolver path works.
	p.allow = []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}
	resp, err = c.Get(u)
	if err != nil {
		t.Fatalf("allow-listed loopback via DNS: %v", err)
	}
	resp.Body.Close()
	if hits.Load() != 1 {
		t.Fatalf("hits = %d", hits.Load())
	}
}
