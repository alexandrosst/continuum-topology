package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"continuum/internal/store"
)

// hbOperator creates an operator with its heartbeat on, returning it and the heartbeat secret.
func (e *env) hbOperator(t *testing.T, name, fingerprint string) (store.Operator, string) {
	t.Helper()
	cl := e.approvedCluster(t, fingerprint)
	op, _, _, hb, err := e.core.CreateOperatorWithHeartbeat(e.ctx, "alex", name, []string{cl}, extDest("collector.example:4317"), nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if hb == "" {
		t.Fatal("no heartbeat secret returned")
	}
	return op, hb
}

func (e *env) health(t *testing.T, id string) OperatorHealth {
	t.Helper()
	op, err := e.st.GetOperator(e.ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return operatorHealthAt(op, e.core.Now())
}

func auditActions(t *testing.T, e *env) (actions []string, all string) {
	t.Helper()
	evs, err := e.st.ListAudit(e.ctx, "org-1", 200)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, ev := range evs {
		actions = append(actions, ev.Action)
		b.WriteString(ev.Actor + "|" + ev.Action + "|" + ev.TargetKind + "|" + ev.TargetID + "|" + ev.Detail + "\n")
	}
	return actions, b.String()
}

func TestCreateOperatorHeartbeatIsOptInAndSecretIsOnlyStoredHashed(t *testing.T) {
	e := newEnv(t)
	cl := e.approvedCluster(t, fp)
	// The pre-existing entry point never mints one.
	plain, _, _, err := e.core.CreateOperator(e.ctx, "alex", "plain", []string{cl}, extDest("c:4317"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := e.st.GetOperator(e.ctx, plain.ID); len(got.HeartbeatHash) != 0 || got.HeartbeatEnabledAt != nil {
		t.Fatalf("an operator created without the heartbeat has one: %+v", got)
	}
	if h := e.health(t, plain.ID); h.State != HealthUnknown || h.Reporting || h.LastSeenAt != nil {
		t.Fatalf("health of an operator that never opted in = %+v", h)
	}

	op, recv, _, hb, err := e.core.CreateOperatorWithHeartbeat(e.ctx, "alex", "with-hb", []string{cl}, extDest("c:4317"), nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hb, heartbeatPrefix) || !looksLikeHeartbeatSecret(hb) {
		t.Fatalf("heartbeat secret has the wrong shape: %q", hb)
	}
	if recv != "" {
		t.Fatalf("an mTLS operator has no receiver bearer token, got %q", recv)
	}
	got, _ := e.st.GetOperator(e.ctx, op.ID)
	if string(got.HeartbeatHash) == hb || len(got.HeartbeatHash) == 0 || got.HeartbeatEnabledAt == nil {
		t.Fatalf("stored heartbeat state is wrong: %+v", got)
	}
	if string(got.HeartbeatHash) == string(got.ReceiverAuthTokenHash) {
		t.Fatal("the two credentials share a hash")
	}
	// A receiver-token-shaped secret does not open the heartbeat door.
	recvShaped, _ := NewOperatorReceiverSecret()
	if err := e.core.RecordOperatorHeartbeat(e.ctx, recvShaped); err != errHeartbeatRejected {
		t.Fatalf("the receiver token was accepted as a heartbeat secret: %v", err)
	}
	if err := e.core.RecordOperatorHeartbeat(e.ctx, hb); err != nil {
		t.Fatalf("the heartbeat secret was refused: %v", err)
	}
	_, trail := auditActions(t, e)
	if strings.Contains(trail, hb) || strings.Contains(trail, heartbeatPrefix) {
		t.Fatalf("audit trail holds secret material:\n%s", trail)
	}
	if !strings.Contains(trail, "operator-created|operator|"+op.ID+"|with-hb (heartbeat enabled)") {
		t.Fatalf("creation with a heartbeat is not recorded as such:\n%s", trail)
	}
}

func TestEnableAndRotateOperatorHeartbeat(t *testing.T) {
	e := newEnv(t)
	cl := e.approvedCluster(t, fp)
	op, _, _, err := e.core.CreateOperator(e.ctx, "alex", "athens", []string{cl}, extDest("c:4317"), nil)
	if err != nil {
		t.Fatal(err)
	}
	first, rotated, err := e.core.EnableOperatorHeartbeat(e.ctx, "alex", op.ID)
	if err != nil || rotated {
		t.Fatalf("first enable: rotated=%v err=%v", rotated, err)
	}
	if err := e.core.RecordOperatorHeartbeat(e.ctx, first); err != nil {
		t.Fatalf("first secret refused: %v", err)
	}

	second, rotated, err := e.core.EnableOperatorHeartbeat(e.ctx, "alex", op.ID)
	if err != nil || !rotated {
		t.Fatalf("second enable: rotated=%v err=%v", rotated, err)
	}
	if second == first {
		t.Fatal("rotation returned the same secret")
	}
	if err := e.core.RecordOperatorHeartbeat(e.ctx, first); err != errHeartbeatRejected {
		t.Fatalf("the replaced secret still works: %v", err)
	}
	if err := e.core.RecordOperatorHeartbeat(e.ctx, second); err != nil {
		t.Fatalf("the new secret is refused: %v", err)
	}
	// Rotation keeps what was last seen: the operator did not stop existing.
	if got, _ := e.st.GetOperator(e.ctx, op.ID); got.LastSeenAt == nil {
		t.Fatal("rotation forgot last-seen")
	}

	actions, trail := auditActions(t, e)
	var enabled, rot int
	for _, a := range actions {
		switch a {
		case "operator-heartbeat-enabled":
			enabled++
		case "operator-heartbeat-rotated":
			rot++
		}
	}
	if enabled != 1 || rot != 1 {
		t.Fatalf("audit actions = %v, want one enabled and one rotated", actions)
	}
	for _, s := range []string{first, second, first[len(heartbeatPrefix):], second[len(heartbeatPrefix):]} {
		if strings.Contains(trail, s) {
			t.Fatalf("audit trail contains secret material %q:\n%s", s, trail)
		}
	}

	// Only an active operator of this organisation.
	if err := e.core.RevokeOperator(e.ctx, "alex", op.ID, "gone"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.core.EnableOperatorHeartbeat(e.ctx, "alex", op.ID); kindOf(err) != KindConflict {
		t.Fatalf("revoked operator: %v", err)
	}
	if _, _, err := e.core.EnableOperatorHeartbeat(e.ctx, "alex", "op-nope"); kindOf(err) != KindNotFound {
		t.Fatalf("unknown operator: %v", err)
	}
	if err := e.st.CreateOrg(e.ctx, store.Org{ID: "org-2", Name: "Two", CreatedAt: *e.now, CreatedBy: "u-owner"}, "u-owner"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.base.ForOrg("org-2").EnableOperatorHeartbeat(e.ctx, "alex", op.ID); kindOf(err) != KindNotFound {
		t.Fatalf("another organisation's operator: %v", err)
	}
}

func TestRevokedOperatorHeartbeatIsRejected(t *testing.T) {
	e := newEnv(t)
	op, hb := e.hbOperator(t, "athens", fp)
	if err := e.core.RecordOperatorHeartbeat(e.ctx, hb); err != nil {
		t.Fatal(err)
	}
	if err := e.core.RevokeOperator(e.ctx, "alex", op.ID, "decommissioned"); err != nil {
		t.Fatal(err)
	}
	*e.now = e.now.Add(time.Minute) // past the write coalescing window too
	if err := e.core.RecordOperatorHeartbeat(e.ctx, hb); err != errHeartbeatRejected {
		t.Fatalf("a revoked operator's heartbeat was accepted: %v", err)
	}
	if err := e.core.DeleteOperator(e.ctx, "alex", op.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.core.RecordOperatorHeartbeat(e.ctx, hb); err != errHeartbeatRejected {
		t.Fatalf("a deleted operator's heartbeat was accepted: %v", err)
	}
}

func TestOperatorHeartbeatWritesAreCoalesced(t *testing.T) {
	e := newEnv(t)
	op, hb := e.hbOperator(t, "athens", fp)
	t0 := *e.now
	seen := func() time.Time {
		got, err := e.st.GetOperator(e.ctx, op.ID)
		if err != nil || got.LastSeenAt == nil {
			t.Fatalf("last seen: %v %+v", err, got)
		}
		return *got.LastSeenAt
	}
	beat := func() {
		t.Helper()
		if err := e.core.RecordOperatorHeartbeat(e.ctx, hb); err != nil {
			t.Fatal(err)
		}
	}
	beat()
	if got := seen(); !got.Equal(t0.Truncate(time.Millisecond)) {
		t.Fatalf("first heartbeat not recorded: %v want %v", got, t0)
	}
	*e.now = t0.Add(heartbeatWriteEvery - time.Second)
	beat()
	if got := seen(); !got.Equal(t0.Truncate(time.Millisecond)) {
		t.Fatalf("a heartbeat %v after the last write hit the database: last seen moved to %v", heartbeatWriteEvery-time.Second, got)
	}
	*e.now = t0.Add(heartbeatWriteEvery)
	beat()
	if got := seen(); !got.Equal(e.now.Truncate(time.Millisecond)) {
		t.Fatalf("a heartbeat %v after the last write was not recorded: %v", heartbeatWriteEvery, got)
	}
}

func TestOperatorHealthThresholds(t *testing.T) {
	e := newEnv(t)
	op, hb := e.hbOperator(t, "athens", fp)
	if h := e.health(t, op.ID); h.State != HealthWaiting || h.Reporting || h.LastSeenAt != nil || h.HeartbeatEnabledAt == nil {
		t.Fatalf("credential but no heartbeat yet = %+v", h)
	}
	// Last-seen is stored to the millisecond, so start the clock on one.
	*e.now = e.now.Truncate(time.Millisecond)
	t0 := *e.now
	if err := e.core.RecordOperatorHeartbeat(e.ctx, hb); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		after time.Duration
		want  string
	}{
		{0, HealthOnline},
		{OperatorHeartbeatInterval, HealthOnline},
		{3 * OperatorHeartbeatInterval, HealthOnline}, // exactly three intervals: still online
		{3*OperatorHeartbeatInterval + time.Second, HealthOffline},
		{24 * time.Hour, HealthOffline},
	} {
		*e.now = t0.Add(c.after)
		h := e.health(t, op.ID)
		if h.State != c.want || !h.Reporting || h.LastSeenAt == nil {
			t.Fatalf("%v after the last heartbeat: %+v, want %s", c.after, h, c.want)
		}
	}
	// A heartbeat brings it back.
	if err := e.core.RecordOperatorHeartbeat(e.ctx, hb); err != nil {
		t.Fatal(err)
	}
	if h := e.health(t, op.ID); h.State != HealthOnline {
		t.Fatalf("after a fresh heartbeat: %+v", h)
	}
	// Revoked: it can no longer report, so it is not "online" however recent its last heartbeat.
	if err := e.core.RevokeOperator(e.ctx, "alex", op.ID, "x"); err != nil {
		t.Fatal(err)
	}
	if h := e.health(t, op.ID); h.State != HealthOffline || h.LastSeenAt == nil {
		t.Fatalf("revoked = %+v", h)
	}
}

func TestOperatorHeartbeatIsRateLimitedPerOperator(t *testing.T) {
	e := newEnv(t)
	op, hb := e.hbOperator(t, "athens", fp)
	limited := 0
	for i := 0; i < 60; i++ {
		switch err := e.core.RecordOperatorHeartbeat(e.ctx, hb); err {
		case nil:
		case errHeartbeatRateLimited:
			limited++
		default:
			t.Fatal(err)
		}
	}
	if limited == 0 {
		t.Fatal("60 back-to-back heartbeats from one operator were never limited")
	}
	// A different operator is unaffected.
	_, other := e.hbOperator(t, "patras", fp2)
	if err := e.core.RecordOperatorHeartbeat(e.ctx, other); err != nil {
		t.Fatalf("another operator was limited too: %v", err)
	}
	_ = op
}

// ---- HTTP ----

// beat POSTs a heartbeat the way the collector's otlphttp exporter does.
func (a *adminRig) beat(secret string, body []byte, opts ...opt) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, OperatorHeartbeatPath, bytes.NewReader(body))
	req.RemoteAddr = "10.7.7.7:4000"
	req.Header.Set("Content-Type", "application/x-protobuf")
	if secret != "" {
		req.Header.Set("Authorization", "Bearer "+secret)
	}
	for _, o := range opts {
		o(req)
	}
	rec := httptest.NewRecorder()
	a.h.ServeHTTP(rec, req)
	return rec
}

// collectorBody is the exact request body otelcol-contrib 0.160.0 sent when running the operator chart's
// rendered heartbeat config - see testdata/heartbeat_otlp_request.meta.json.
func collectorBody(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/heartbeat_otlp_request.bin")
	if err != nil || len(b) == 0 {
		t.Fatalf("capture missing: %v", err)
	}
	return b
}

func TestHeartbeatEndpointAcceptsWhatTheRealCollectorSends(t *testing.T) {
	a := newAdminRig(t)
	op, hb := a.hbOperator(t, "athens", fp)
	body := collectorBody(t)

	// Exactly as captured: protobuf, gzip, the collector's own User-Agent, and no cookie, no
	// X-Requested-With, no Origin - nothing a browser-facing route would require.
	rec := a.beat(hb, body, withHeader("Content-Encoding", "gzip"), withHeader("User-Agent", "OpenTelemetry Collector Contrib/0.160.0 (linux/amd64)"))
	if rec.Code != 200 {
		t.Fatalf("status %d body %q", rec.Code, rec.Body.String())
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("a success must have an EMPTY body (the exporter's success path), got %q", rec.Body.String())
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("the heartbeat endpoint must not speak CORS, got %q", got)
	}
	if h := a.health(t, op.ID); h.State != HealthOnline || !h.Reporting {
		t.Fatalf("health after the captured heartbeat = %+v", h)
	}
	// A cross-origin browser request is not given permission either.
	rec = a.beat(hb, body, withHeader("Origin", "https://evil.example"))
	if rec.Header().Get("Access-Control-Allow-Origin") != "" || rec.Header().Get("Access-Control-Allow-Credentials") != "" {
		t.Fatalf("CORS headers on the heartbeat endpoint: %v", rec.Header())
	}
}

func TestHeartbeatEndpointIsUniform401ForEveryKindOfFailure(t *testing.T) {
	a := newAdminRig(t)
	op, hb := a.hbOperator(t, "athens", fp)
	rotated, _, err := a.core.EnableOperatorHeartbeat(a.ctx, "alex", op.ID)
	if err != nil {
		t.Fatal(err)
	}
	revokedOp, revokedSecret := a.hbOperator(t, "patras", fp2)
	if err := a.core.RevokeOperator(a.ctx, "alex", revokedOp.ID, "x"); err != nil {
		t.Fatal(err)
	}
	validShape := heartbeatPrefix + strings.Repeat("A", 43)
	_, recv, _, _ := a.createReceiverOnly(t)

	cases := map[string]*httptest.ResponseRecorder{
		"no header":            a.beat("", nil),
		"garbage":              a.beat("hello", nil),
		"wrong prefix":         a.beat("cno_"+strings.Repeat("A", 43), nil),
		"well formed, unknown": a.beat(validShape, nil),
		"the receiver token":   a.beat(recv, nil),
		"rotated away":         a.beat(hb, nil),
		"revoked operator":     a.beat(revokedSecret, nil),
		"basic auth":           a.beat("", nil, withHeader("Authorization", "Basic Zm9vOmJhcg==")),
	}
	var ref *httptest.ResponseRecorder
	for name, rec := range cases {
		if rec.Code != 401 {
			t.Fatalf("%s: status %d", name, rec.Code)
		}
		if ref == nil {
			ref = rec
			continue
		}
		if rec.Body.String() != ref.Body.String() || rec.Header().Get("WWW-Authenticate") != ref.Header().Get("WWW-Authenticate") || rec.Header().Get("Content-Type") != ref.Header().Get("Content-Type") {
			t.Fatalf("%s: response differs from the others (an oracle): %q %v vs %q %v", name, rec.Body.String(), rec.Header(), ref.Body.String(), ref.Header())
		}
	}
	// Control: the current secret works.
	if rec := a.beat(rotated, nil); rec.Code != 200 {
		t.Fatalf("current secret: %d", rec.Code)
	}
}

// createReceiverOnly makes an operator with no heartbeat and returns its receiver token.
func (a *adminRig) createReceiverOnly(t *testing.T) (store.Operator, string, OperatorTLSBundle, error) {
	t.Helper()
	cl := a.approvedCluster(t, "5b8e1f2a-3333-4333-8444-955566667788")
	return a.core.CreateOperator(a.ctx, "alex", "no-heartbeat", []string{cl}, extDest("c:4317"), nil)
}

func TestHeartbeatEndpointCapsTheBodyAndRejectsOtherMethods(t *testing.T) {
	a := newAdminRig(t)
	_, hb := a.hbOperator(t, "athens", fp)

	if rec := a.beat(hb, bytes.Repeat([]byte("x"), maxHeartbeatBody)); rec.Code != 200 {
		t.Fatalf("a body exactly at the cap: %d", rec.Code)
	}
	if rec := a.beat(hb, bytes.Repeat([]byte("x"), maxHeartbeatBody+1)); rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("a body one byte over the cap (declared length): %d", rec.Code)
	}
	// An undeclared length (chunked) is caught by the reader, not the header.
	req := httptest.NewRequest(http.MethodPost, OperatorHeartbeatPath, bytes.NewReader(bytes.Repeat([]byte("x"), 2*maxHeartbeatBody)))
	req.ContentLength = -1
	req.RemoteAddr = "10.7.7.7:4000"
	req.Header.Set("Authorization", "Bearer "+hb)
	rec := httptest.NewRecorder()
	a.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("an oversized chunked body: %d", rec.Code)
	}
	// An unauthenticated oversized request is a plain 401: the body is never read for it.
	if rec := a.beat("", bytes.Repeat([]byte("x"), 4*maxHeartbeatBody)); rec.Code != 401 {
		t.Fatalf("unauthenticated oversized: %d", rec.Code)
	}
	for _, m := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		req := httptest.NewRequest(m, OperatorHeartbeatPath, nil)
		req.Header.Set("Authorization", "Bearer "+hb)
		rec := httptest.NewRecorder()
		a.h.ServeHTTP(rec, req)
		if rec.Code == 200 {
			t.Fatalf("%s was accepted", m)
		}
	}
}

func TestHeartbeatEndpointThrottlesFailuresPerAddressButNeverAGoodOperator(t *testing.T) {
	a := newAdminRig(t)
	_, hb := a.hbOperator(t, "athens", fp)
	bad := heartbeatPrefix + strings.Repeat("B", 43)
	var got429 bool
	for i := 0; i < 40; i++ {
		if a.beat(bad, nil, fromIP("10.9.9.9")).Code == 429 {
			got429 = true
			break
		}
	}
	if !got429 {
		t.Fatal("secret guessing from one address was never throttled")
	}
	// Another address is untouched, and the throttled address's failures did not lock out the operator.
	if rec := a.beat(hb, nil, fromIP("10.8.8.8")); rec.Code != 200 {
		t.Fatalf("a good heartbeat from another address: %d", rec.Code)
	}
	if rec := a.beat(hb, nil, fromIP("10.9.9.9")); rec.Code != 200 {
		t.Fatalf("a successful heartbeat is never counted against the failure limit: %d", rec.Code)
	}
}

func jsonMap(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("not JSON: %q", b)
	}
	return m
}

func TestOperatorsHTTPHealthShapeAndHeartbeatEnableRoute(t *testing.T) {
	a := newAdminRig(t)
	_, cookie := a.user(t, "alex", RoleAdmin)
	cl := a.approvedCluster(t, fp)
	create := func(extra map[string]any) map[string]any {
		body := map[string]any{"name": "athens", "sourceClusterIds": []string{cl}, "destination": map[string]any{"kind": "external", "endpoint": "c:4317"}}
		for k, v := range extra {
			body[k] = v
		}
		r := a.do("POST", "/api/v1/operators", body, withCookie(cookie))
		if r.Code != 201 {
			t.Fatalf("create: %d %s", r.Code, r.Body.String())
		}
		return r.json(t)
	}

	// Absent heartbeat field = off: nothing heartbeat-shaped in the response, and the install command is
	// exactly what it was before this feature.
	off := create(nil)
	if _, has := off["heartbeatToken"]; has {
		t.Fatalf("heartbeat material returned for an operator that did not ask: %v", off)
	}
	if strings.Contains(off["install"].(string), "heartbeat") {
		t.Fatalf("install command mentions the heartbeat when it was not asked for: %v", off["install"])
	}
	offOp := off["operator"].(map[string]any)
	if h, _ := offOp["health"].(map[string]any); len(h) != 2 || h["state"] != "unknown" || h["reporting"] != false {
		t.Fatalf("health of an operator with no heartbeat = %v", offOp["health"])
	}

	// Explicitly on at creation.
	on := create(map[string]any{"heartbeat": true})
	tok, _ := on["heartbeatToken"].(string)
	if !strings.HasPrefix(tok, heartbeatPrefix) {
		t.Fatalf("no heartbeat token: %v", on)
	}
	if on["token"] == tok {
		t.Fatal("receiver token reused as the heartbeat secret")
	}
	install := on["install"].(string)
	for _, want := range []string{"--set heartbeat.enabled=true", "--set heartbeat.url=http://example.com" + OperatorHeartbeatPath, "--set heartbeat.auth.secretName=" + on["operator"].(map[string]any)["id"].(string) + "-heartbeat-auth"} {
		if !strings.Contains(install, want) {
			t.Fatalf("install command lacks %q:\n%s", want, install)
		}
	}
	if sc := on["heartbeatSecretCommand"].(string); !strings.Contains(sc, "token: \""+tok+"\"") || !strings.Contains(sc, "kubectl apply -f - <<'CONTINUUM_SECRET'") || strings.Contains(sc, "--from-literal") {
		t.Fatalf("heartbeat secret command = %q", sc)
	}
	if on["heartbeatIntervalSeconds"] != float64(60) || on["heartbeatUrl"] != "http://example.com"+OperatorHeartbeatPath {
		t.Fatalf("heartbeat facts = %v %v", on["heartbeatIntervalSeconds"], on["heartbeatUrl"])
	}
	if on["heartbeatWarning"] == nil {
		t.Fatal("plain-HTTP heartbeat URL was not flagged")
	}

	// The secret is never readable again.
	id := off["operator"].(map[string]any)["id"].(string)
	for _, path := range []string{"/api/v1/operators", "/api/v1/operators/" + id} {
		if body := a.do("GET", path, nil, withCookie(cookie)).Body.String(); strings.Contains(body, tok) || strings.Contains(strings.ToLower(body), "hash") {
			t.Fatalf("GET %s leaks heartbeat material: %s", path, body)
		}
	}

	// Enable for an existing operator, then rotate.
	r := a.do("POST", "/api/v1/operators/"+id+"/heartbeat", nil, withCookie(cookie))
	if r.Code != 200 {
		t.Fatalf("enable: %d %s", r.Code, r.Body.String())
	}
	en := r.json(t)
	first := en["heartbeatToken"].(string)
	if en["rotated"] != false || en["heartbeatRestartCommand"] != nil {
		t.Fatalf("first enable = %v", en)
	}
	if !strings.HasPrefix(en["heartbeatUpgradeCommand"].(string), "helm upgrade "+id+" ") || !strings.Contains(en["heartbeatUpgradeCommand"].(string), "--reuse-values --set heartbeat.enabled=true") {
		t.Fatalf("upgrade command = %v", en["heartbeatUpgradeCommand"])
	}
	if a.beat(first, nil).Code != 200 {
		t.Fatal("the enabled secret does not work")
	}
	r = a.do("POST", "/api/v1/operators/"+id+"/heartbeat", nil, withCookie(cookie))
	rot := r.json(t)
	if rot["rotated"] != true || !strings.Contains(rot["heartbeatRestartCommand"].(string), "rollout restart") || !strings.Contains(rot["heartbeatSecretCommand"].(string), "kubectl apply -f -") {
		t.Fatalf("rotation = %v", rot)
	}
	if a.beat(first, nil).Code != 401 || a.beat(rot["heartbeatToken"].(string), nil).Code != 200 {
		t.Fatal("rotation did not swap the secret")
	}
	// Now it has reported, and the read model says so in exactly the documented shape.
	got := a.do("GET", "/api/v1/operators/"+id, nil, withCookie(cookie)).json(t)["health"].(map[string]any)
	if got["state"] != "online" || got["reporting"] != true || got["lastSeenAt"] == nil || got["heartbeatEnabledAt"] == nil || len(got) != 4 {
		t.Fatalf("health = %v", got)
	}
	if _, err := time.Parse(time.RFC3339, got["lastSeenAt"].(string)); err != nil {
		t.Fatalf("lastSeenAt is not RFC3339: %v", got["lastSeenAt"])
	}
	list := a.do("GET", "/api/v1/operators", nil, withCookie(cookie)).jsonArray(t)
	for _, o := range list {
		if o["health"] == nil {
			t.Fatalf("an operator in the list has no health: %v", o)
		}
	}
}

func TestOperatorHeartbeatEnableRouteNeedsAdmin(t *testing.T) {
	a := newAdminRig(t)
	_, admin := a.user(t, "alex", RoleAdmin)
	_, editor := a.user(t, "ed", RoleEditor)
	_, viewer := a.user(t, "vi", RoleViewer)
	cl := a.approvedCluster(t, fp)
	r := a.do("POST", "/api/v1/operators", map[string]any{"name": "x", "sourceClusterIds": []string{cl}, "destination": map[string]any{"kind": "external", "endpoint": "c:4317"}}, withCookie(admin))
	id := r.json(t)["operator"].(map[string]any)["id"].(string)
	for name, c := range map[string]string{"editor": editor, "viewer": viewer} {
		if code := a.do("POST", "/api/v1/operators/"+id+"/heartbeat", nil, withCookie(c)).Code; code != 403 {
			t.Fatalf("%s: %d", name, code)
		}
	}
	if code := a.do("POST", "/api/v1/operators/"+id+"/heartbeat", nil).Code; code != 401 {
		t.Fatalf("no session: %d", code)
	}
	if code := a.do("POST", "/api/v1/operators/"+id+"/heartbeat", nil, withCookie(admin), withoutXRW()).Code; code != 403 {
		t.Fatalf("no CSRF header: %d", code)
	}
	if code := a.do("POST", "/api/v1/operators/op-nope/heartbeat", nil, withCookie(admin)).Code; code != 404 {
		t.Fatalf("unknown operator: %d", code)
	}
}
