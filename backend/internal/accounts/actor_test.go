package accounts

import (
	"context"
	"testing"
)

func TestTheTextOfAnActorIsWhatTheAuditTrailAlwaysHeld(t *testing.T) {
	for _, c := range []struct {
		a    Actor
		want string
	}{
		{UserActor("alice"), "alice"},
		{AgentActor("ag-1f"), "agent:ag-1f"},
		{UnenrolledAgentActor, "agent:unenrolled"},
		{SystemActor, "system"},
		{RenewalActor, "renewal"},
		{AnonymousActor, "anonymous"},
		{Actor{}, "anonymous"},
	} {
		if got := c.a.String(); got != c.want {
			t.Errorf("%+v is %q, want %q", c.a, got, c.want)
		}
	}
}

func TestAPersonCalledSystemIsNotTheServer(t *testing.T) {
	person := UserActor("system")
	if person == SystemActor || person.Kind() != ActorUser || SystemActor.Kind() != ActorServer || !person.IsPerson() || SystemActor.IsPerson() {
		t.Fatalf("a person named system and the server are the same: %+v %+v", person, SystemActor)
	}
	if person.String() != SystemActor.String() {
		t.Fatal("the text is expected to be the same; that is why the name is reserved at registration")
	}
}

func TestTheZeroActorIsAnonymousAndGrantsNothing(t *testing.T) {
	var a Actor
	if a.Kind() != ActorAnonymous || a.IsPerson() || a != (Actor{}) {
		t.Fatalf("zero actor: %+v", a)
	}
	if got := ActorFrom(context.Background()); got.Kind() != ActorAnonymous {
		t.Fatalf("a context with no one in it is %+v", got)
	}
}

func TestTheReservedWordsAreExactlyTheServersAndAnonymous(t *testing.T) {
	if len(reservedActors) != 3 || !reservedActors["system"] || !reservedActors["renewal"] || !reservedActors["anonymous"] {
		t.Fatalf("reserved: %v", reservedActors)
	}
	for _, a := range []Actor{SystemActor, RenewalActor, AnonymousActor} {
		if usernameProblem(a.String()) == "" {
			t.Errorf("%q can be a username", a.String())
		}
	}
	// An agent's text holds a colon, which a username cannot, so it needs no reservation.
	if usernameProblem(AgentActor("x").String()) == "" {
		t.Error("an agent's text can be a username")
	}
}
