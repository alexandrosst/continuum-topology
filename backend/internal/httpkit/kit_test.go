package httpkit

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"continuum/internal/accounts"
	"continuum/internal/kernel"
)

func body(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatalf("not JSON: %q", w.Body.String())
	}
	return m
}

func TestAnErrorIsAnsweredWithItsKindsStatusAndItsMessage(t *testing.T) {
	want := map[kernel.Kind]int{
		kernel.KindInvalid: 400, kernel.KindUnauthenticated: 401, kernel.KindNotFound: 404, kernel.KindConflict: 409,
		kernel.KindRateLimited: 429, kernel.KindInternal: 500, kernel.KindForbidden: 403, kernel.KindTwoFactorRequired: 401,
	}
	for k := kernel.KindInvalid; k <= kernel.KindTwoFactorRequired; k++ {
		code, ok := want[k]
		if !ok {
			t.Fatalf("kind %d is not in the table of this test: say what it answers", k)
		}
		w := httptest.NewRecorder()
		Fail(w, slog.Default(), kernel.Errf(k, "because %d", k))
		if w.Code != code || body(t, w)["error"] != "because "+strconv.Itoa(int(k)) {
			t.Errorf("kind %d: %d %s, want %d", k, w.Code, w.Body.String(), code)
		}
		if ct := w.Header().Get("Content-Type"); ct != "application/json" {
			t.Errorf("kind %d: content type %q", k, ct)
		}
	}
}

func TestAnErrorsDataIsMoreFieldsOfTheSameObjectAndNeverReplacesTheMessage(t *testing.T) {
	w := httptest.NewRecorder()
	e := kernel.Errf(kernel.KindConflict, "taken")
	e.Data = map[string]any{"holder": "agent-1"}
	Fail(w, slog.Default(), e)
	if m := body(t, w); w.Code != 409 || m["error"] != "taken" || m["holder"] != "agent-1" {
		t.Fatalf("%d %v", w.Code, m)
	}
}

func TestAnErrorFromOutsideTheKernelIsLoggedAndSaysNothing(t *testing.T) {
	var logs bytes.Buffer
	w := httptest.NewRecorder()
	Fail(w, slog.New(slog.NewTextHandler(&logs, nil)), errors.New("dial tcp 10.1.2.3: refused"))
	if m := body(t, w); w.Code != 500 || m["error"] != "internal error" {
		t.Fatalf("%d %v", w.Code, m)
	}
	if strings.Contains(w.Body.String(), "10.1.2.3") || !strings.Contains(logs.String(), "10.1.2.3") {
		t.Fatalf("the cause must be in the log and not in the answer:\nanswer %s\nlog %s", w.Body.String(), logs.String())
	}
}

func TestAWrappedKernelErrorIsStillOne(t *testing.T) {
	w := httptest.NewRecorder()
	Fail(w, slog.Default(), errors.Join(errors.New("context"), kernel.Errf(kernel.KindNotFound, "no such thing")))
	if w.Code != 404 {
		t.Fatalf("%d", w.Code)
	}
}

func TestDecodeRefusesWhatTheTypeDoesNotHave(t *testing.T) {
	var v struct {
		Name string `json:"name"`
	}
	r := httptest.NewRequest("POST", "/", strings.NewReader(`{"name":"a"}`))
	if err := Decode(r, &v); err != nil || v.Name != "a" {
		t.Fatalf("%v %v", err, v)
	}
	for _, bad := range []string{`{"name":"a","extra":1}`, `{"name":`, ``, `[]`} {
		err := Decode(httptest.NewRequest("POST", "/", strings.NewReader(bad)), &v)
		var e *kernel.Error
		if !errors.As(err, &e) || e.Kind != kernel.KindInvalid {
			t.Errorf("%q: %v", bad, err)
		}
	}
}

func TestParseTimeIsZeroWhenAbsentAndRefusesWhatIsNotRFC3339(t *testing.T) {
	r := httptest.NewRequest("GET", "/?since=2026-10-01T10:00:00Z&until=yesterday", nil)
	if got, err := ParseTime(r, "since"); err != nil || got.UTC().Hour() != 10 {
		t.Fatalf("%v %v", got, err)
	}
	if got, err := ParseTime(r, "missing"); err != nil || !got.IsZero() {
		t.Fatalf("%v %v", got, err)
	}
	if _, err := ParseTime(r, "until"); err == nil || !strings.Contains(err.Error(), "until must be an RFC 3339 time") {
		t.Fatalf("%v", err)
	}
}

func TestTheCallerIsWhoTheGuardPutInTheContext(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	if Actor(r) != "" {
		t.Fatalf("with nobody in the context the actor is %q", Actor(r))
	}
	p := accounts.Principal{Role: "editor"}
	p.User.Username = "alex"
	r = r.WithContext(accounts.WithCaller(r.Context(), p))
	if Actor(r) != "alex" || Principal(r).Role != "editor" {
		t.Fatalf("%q %+v", Actor(r), Principal(r))
	}
}

func TestEachOrganisationLevelAsksForAHigherActThanTheOneBelow(t *testing.T) {
	levels := []Need{MemberRole, EditorRole, AdminRole, OwnerRole}
	names := []string{"member", "editor", "admin", "owner"}
	acts := []accounts.Action{accounts.ActRead, accounts.ActEdit, accounts.ActAdminister, accounts.ActOwn}
	for i, n := range levels {
		if n.String() != names[i] || n.Action() != acts[i] {
			t.Errorf("%d: %s %v", n, n.String(), n.Action())
		}
	}
	for _, n := range []Need{AnySession, Settled} {
		if n.Action() != 0 {
			t.Errorf("%s names an organisation act: %v", n, n.Action())
		}
	}
	if AnySession.String() != "session" || Settled.String() != "settled" || Need(99).String() != "unknown" {
		t.Error("names")
	}
}
