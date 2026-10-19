package fusionhttp

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"continuum/internal/accounts"
	"continuum/internal/fusion"
	"continuum/internal/fusionapi"
	"continuum/internal/kernel"
	"continuum/internal/store"
)

type fakeAccounts struct {
	p       accounts.Principal
	err     error
	memberr error
}

func (f fakeAccounts) Authenticate(context.Context, string) (accounts.Principal, error) {
	return f.p, f.err
}
func (f fakeAccounts) AuthenticateAPIToken(context.Context, string) (accounts.Principal, error) {
	return f.p, f.err
}
func (f fakeAccounts) Member(_ context.Context, p accounts.Principal, org string) (accounts.Principal, error) {
	p.Org = org
	return p, f.memberr
}

type fakeTokens struct {
	tok store.FusionToken
	err error
}

func (f fakeTokens) AuthenticateFusionToken(context.Context, string) (store.FusionToken, error) {
	return f.tok, f.err
}

func bearer(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, "Bearer ") {
		return "", false
	}
	return strings.TrimPrefix(h, "Bearer "), true
}

func session(r *http.Request) (string, bool) {
	c, err := r.Cookie("s")
	if err != nil {
		return "", false
	}
	return c.Value, true
}

func newGuard(acc fakeAccounts, tok fakeTokens) *Guard {
	return &Guard{
		Control:      func() *fusion.FusionControl { return &fusion.FusionControl{Data: &fusionapi.Client{}} },
		Org:          func() string { return "org1" },
		Accounts:     acc,
		Tokens:       tok,
		Bearer:       bearer,
		Session:      session,
		ClientIP:     func(*http.Request) string { return "192.0.2.1" },
		AuthFailures: kernel.NewLimiter(60, 3),
		Reads:        kernel.NewLimiter(60, 2),
		Extras:       func() fusionapi.Extras { return nil },
		Log:          slog.Default(),
	}
}

// admin is a person who administers org1.
var admin = accounts.Principal{User: store.User{ID: "u1", Username: "ada"}, Role: "admin"}

// fusionSecret is shaped like a FUSION access token.
var fusionSecret = func() string { s, _ := kernel.NewFusionTokenSecret(); return s }()

func call(g *Guard, mod func(*http.Request)) (*httptest.ResponseRecorder, *Caller) {
	var got *Caller
	h := g.Serve(func(w http.ResponseWriter, r *http.Request, _ *fusionapi.Client, who Caller) {
		got = &who
		w.WriteHeader(http.StatusNoContent)
	})
	r := httptest.NewRequest("GET", "/x", nil)
	mod(r)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w, got
}

func TestATokenReadsWithTheScopeItWasMintedWith(t *testing.T) {
	exp := time.Now().Add(time.Hour)
	g := newGuard(fakeAccounts{}, fakeTokens{tok: store.FusionToken{ID: "t1", OrgID: "org1", Name: "ci", Signals: []string{"logs"}, Namespaces: []string{"shop"}, ExpiresAt: exp}})
	w, who := call(g, func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+fusionSecret) })
	if w.Code != http.StatusNoContent || who == nil {
		t.Fatalf("code %d", w.Code)
	}
	if who.Kind != "token" || who.ID != "t1" || len(who.Scope.Signals) != 1 || who.Scope.Namespaces[0] != "shop" || who.ExpiresAt == nil {
		t.Errorf("caller = %+v", *who)
	}
}

func TestATokenOfAnotherOrganisationIsNotKnown(t *testing.T) {
	g := newGuard(fakeAccounts{}, fakeTokens{tok: store.FusionToken{ID: "t1", OrgID: "other"}})
	if w, _ := call(g, func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+fusionSecret) }); w.Code != http.StatusUnauthorized {
		t.Errorf("code %d, want 401", w.Code)
	}
}

func TestAnAdministratorReadsEverythingBySessionOrPersonalToken(t *testing.T) {
	for name, mod := range map[string]func(*http.Request){
		"session":  func(r *http.Request) { r.AddCookie(&http.Cookie{Name: "s", Value: "x"}) },
		"personal": func(r *http.Request) { r.Header.Set("Authorization", "Bearer pat_abc") },
		"both given": func(r *http.Request) {
			r.Header.Set("Authorization", "Bearer pat_abc")
			r.AddCookie(&http.Cookie{Name: "s", Value: "x"})
		},
	} {
		g := newGuard(fakeAccounts{p: admin}, fakeTokens{err: errors.New("never asked")})
		w, who := call(g, mod)
		if w.Code != http.StatusNoContent || who == nil || who.Kind != "user" || !who.Scope.Unrestricted() {
			t.Errorf("%s: code %d, caller %+v", name, w.Code, who)
		}
	}
}

func TestSomeoneWhoIsNotAnAdministratorIsRefusedAndToldWhy(t *testing.T) {
	viewer := admin
	viewer.Role = "viewer"
	g := newGuard(fakeAccounts{p: viewer}, fakeTokens{})
	w, _ := call(g, func(r *http.Request) { r.AddCookie(&http.Cookie{Name: "s", Value: "x"}) })
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "administrator") {
		t.Errorf("code %d body %s", w.Code, w.Body)
	}
	pending := admin
	pending.User.MustChange = true
	g = newGuard(fakeAccounts{p: pending}, fakeTokens{})
	if w, _ := call(g, func(r *http.Request) { r.AddCookie(&http.Cookie{Name: "s", Value: "x"}) }); w.Code != http.StatusForbidden {
		t.Errorf("a pending password change: code %d, want 403", w.Code)
	}
}

func TestNoCredentialIsUnauthorisedAndFailuresAreThrottledPerAddress(t *testing.T) {
	g := newGuard(fakeAccounts{err: errors.New("no")}, fakeTokens{})
	var codes []int
	for range 5 {
		w, _ := call(g, func(r *http.Request) { r.AddCookie(&http.Cookie{Name: "s", Value: "stale"}) })
		codes = append(codes, w.Code)
	}
	if codes[0] != 401 || codes[len(codes)-1] != 429 {
		t.Errorf("codes = %v, want 401 first and 429 once the address has used its failures", codes)
	}
}

func TestACallerIsRateLimitedAndToldWhenToComeBack(t *testing.T) {
	g := newGuard(fakeAccounts{p: admin}, fakeTokens{})
	var last *httptest.ResponseRecorder
	var codes []int
	for range 4 {
		last, _ = call(g, func(r *http.Request) { r.Header.Set("Authorization", "Bearer pat_abc") })
		codes = append(codes, last.Code)
	}
	if codes[0] != 204 || codes[3] != 429 || last.Header().Get("Retry-After") == "" {
		t.Errorf("codes = %v, Retry-After %q", codes, last.Header().Get("Retry-After"))
	}
}

func TestWithNoFusionThereIsNothingToAuthenticateFor(t *testing.T) {
	g := newGuard(fakeAccounts{p: admin}, fakeTokens{})
	g.Control = func() *fusion.FusionControl { return nil }
	if w, _ := call(g, func(r *http.Request) {}); w.Code != http.StatusServiceUnavailable {
		t.Errorf("code %d, want 503", w.Code)
	}
}

func TestTheHandlerRunsUnderTheRequestBudget(t *testing.T) {
	g := newGuard(fakeAccounts{p: admin}, fakeTokens{})
	var left time.Duration
	g.Serve(func(w http.ResponseWriter, r *http.Request, _ *fusionapi.Client, _ Caller) {
		d, ok := r.Context().Deadline()
		if !ok {
			t.Error("no deadline")
		}
		left = time.Until(d)
	}).ServeHTTP(httptest.NewRecorder(), func() *http.Request {
		r := httptest.NewRequest("GET", "/x", nil)
		r.Header.Set("Authorization", "Bearer pat_abc")
		return r
	}())
	if left <= 0 || left > RequestTimeout {
		t.Errorf("budget left %v", left)
	}
}

func TestErrWritesTheStatusOfWhatWentWrong(t *testing.T) {
	g := newGuard(fakeAccounts{}, fakeTokens{})
	for _, c := range []struct {
		err  error
		code int
	}{
		{&fusionapi.Error{Status: 422, Msg: "bad"}, 422},
		{context.DeadlineExceeded, http.StatusGatewayTimeout},
		{kernel.Errf(kernel.KindNotFound, "gone"), http.StatusNotFound},
		{errors.New("boom"), http.StatusInternalServerError},
	} {
		w := httptest.NewRecorder()
		g.Err(w, c.err)
		if w.Code != c.code {
			t.Errorf("%v: code %d, want %d", c.err, w.Code, c.code)
		}
	}
	w := httptest.NewRecorder()
	g.Err(w, context.Canceled)
	if w.Body.Len() != 0 {
		t.Errorf("a caller that went away is told %q", w.Body)
	}
}

func TestACredentialGivenOnPurposeBeatsTheSessionTheBrowserSendsAlong(t *testing.T) {
	g := newGuard(fakeAccounts{p: admin}, fakeTokens{tok: store.FusionToken{ID: "t1", OrgID: "org1", Signals: []string{"logs"}}})
	_, who := call(g, func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer "+fusionSecret)
		r.AddCookie(&http.Cookie{Name: "s", Value: "x"})
	})
	if who == nil || who.Kind != "token" || len(who.Scope.Signals) != 1 {
		t.Errorf("caller = %+v: someone trying a token while signed in as an administrator must read with the token's rights", who)
	}
}
