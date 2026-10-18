package server

import (
	"context"
	"testing"

	"continuum/internal/accounts"
)

func TestTheActorOfAContextIsTheSignedInPerson(t *testing.T) {
	ctx := accounts.WithCaller(context.Background(), accounts.Principal{Role: accounts.RoleViewer})
	if got := accounts.ActorFrom(ctx); !got.IsPerson() {
		t.Fatalf("a context with a signed-in principal is %+v", got)
	}
	ctx = ActingAs(context.Background(), accounts.RoleAdmin)
	if got := accounts.ActorFrom(ctx); !got.IsPerson() {
		t.Fatalf("ActingAs is %+v", got)
	}
}

// The audit text of a refused login is unchanged by the move to Actor values.
func TestAFailedLoginIsStillAuditedAsAnonymous(t *testing.T) {
	a := newAdminRig(t)
	a.register(t, "alice", "Alice Lab")
	r := a.do("POST", "/api/v1/auth/login", map[string]string{"username": "alice", "password": "wrong-password-123"})
	if r.Code == 200 {
		t.Fatal("a wrong password signed in")
	}
	evs, err := a.st.ListAudit(a.ctx, "", 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range evs {
		if e.Action == "login-failed" {
			if e.Actor != "anonymous" {
				t.Fatalf("login-failed by %q", e.Actor)
			}
			return
		}
	}
	t.Fatalf("no login-failed row among %d", len(evs))
}
