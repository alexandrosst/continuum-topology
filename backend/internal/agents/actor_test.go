package agents

import (
	"context"
	"testing"

	"ikhnos/internal/accounts"
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
