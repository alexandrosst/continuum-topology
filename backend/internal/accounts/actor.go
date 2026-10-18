package accounts

import (
	"context"
	"strings"
)

// An Actor is who did a thing. There are four kinds: a signed-in person, an agent, the server acting on its own, and someone who is
// not signed in. The kind is part of the value, so "the server did this" and "a person named system did this" are different
// things even where both end up as text in the audit trail.
//
// The text is what the audit trail has always held, and it is unchanged: a person's username, "agent:<id>" for an agent, and a
// fixed word for the server ("system", "renewal") and for someone anonymous. Those words are defined here and nowhere else, and
// registration refuses them as usernames (see usernameProblem), so a row cannot be made to read as the server's by choosing a name.
//
// The zero Actor is anonymous: nothing about it grants anything.
type Actor struct {
	kind ActorKind
	name string
}

type ActorKind uint8

const (
	ActorAnonymous ActorKind = iota // the zero kind: someone who is not signed in
	ActorUser
	ActorAgent
	ActorServer
)

// The server's own actors. A new one is added here, and is reserved as a username by being here.
var (
	AnonymousActor = Actor{kind: ActorAnonymous, name: "anonymous"}
	SystemActor    = Actor{kind: ActorServer, name: "system"}  // what the server does on its own: expiry, bootstrap, first-run set-up
	RenewalActor   = Actor{kind: ActorServer, name: "renewal"} // an operator's certificate renewal, which no person signs
)

// UnenrolledAgentActor stands for an agent that has no identity yet (a refused enrollment): it has presented only a token.
var UnenrolledAgentActor = AgentActor("unenrolled")

// UserActor is a signed-in person.
func UserActor(username string) Actor { return Actor{kind: ActorUser, name: username} }

// AgentActor is an agent, by the id the server gave it.
func AgentActor(id string) Actor { return Actor{kind: ActorAgent, name: id} }

func (a Actor) Kind() ActorKind { return a.kind }

// IsPerson is whether a signed-in person is acting.
func (a Actor) IsPerson() bool { return a.kind == ActorUser }

// String is the text the audit trail holds for this actor.
func (a Actor) String() string {
	switch a.kind {
	case ActorAgent:
		return "agent:" + a.name
	case ActorUser, ActorServer:
		return a.name
	}
	return AnonymousActor.name
}

// ActorFrom is who a request context says is acting: the signed-in person if there is one, otherwise anonymous.
func ActorFrom(ctx context.Context) Actor {
	if p, ok := ctx.Value(ctxPrincipal{}).(Principal); ok {
		return UserActor(p.User.Username)
	}
	return Actor{}
}

// reservedActors are the words the audit trail uses for the server and for no one: a username may not be one of them, in any case,
// because Ikhnos treats names that differ only in case as one name. They are exactly the names of the actors above that are not a
// person or an agent.
var reservedActors = func() map[string]bool {
	m := map[string]bool{}
	for _, a := range []Actor{AnonymousActor, SystemActor, RenewalActor} {
		m[strings.ToLower(a.name)] = true
	}
	return m
}()
