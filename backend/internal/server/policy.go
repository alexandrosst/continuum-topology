package server

import "context"

// What a caller may do is decided here and nowhere else. Before this file the answer was spread over the route guard's rank
// comparison, canGrant (who may give a role), mayWiden (who may widen an agent's access) and nine more inline comparisons of a
// role with a rank. Those now all ask Decide. It is a pure function of who is asking, what they want to do and what it is done
// to, so it can be tested without a server, and the route table (routes.go) says which routes ask for which action.
//
// It decides only the caller's standing. Facts about the thing itself - that an agent is approved, that a tier is inside its
// ceiling (tier.go), that the last owner cannot leave - are checked where the thing is changed, as before.

// The roles inside an organisation, weakest first. A role that is not in this list (an empty one included) has no rank, and no
// rank is below every level.
var roleRank = map[string]int{RoleViewer: 1, RoleEditor: 2, RoleAdmin: 3, RoleOwner: 4}

func validRole(r string) bool { return roleRank[r] > 0 }

// Action is what a caller wants to do.
type Action int

const (
	ActRead         Action = iota + 1 // see what the organisation holds: any role
	ActEdit                           // change the human layer (workspace, saved views): an editor
	ActAdminister                     // manage agents, tokens, operators and settings, and read who did what: an administrator
	ActOwn                            // owners-only matters
	ActGrantRole                      // give or take away the role in Resource.Role: an owner any role, an administrator an editor or a viewer
	ActChangeAccess                   // change what an agent shares: narrowing is open to the caller, widening (Resource.Widening) takes an administrator
)

// Resource is what the action is done to, as far as the decision depends on it.
type Resource struct {
	Role     string // ActGrantRole: the role given or taken away
	Widening bool   // ActChangeAccess: the change lifts a limit (a higher tier, a pause or an exclusion lifted)
}

// Decision is the answer. Widens echoes whether the change asked about was a widening, so a caller that records or shows the
// decision does not work it out again.
type Decision struct {
	Allowed bool
	Widens  bool
}

// levelRole is the weakest role each ranked action admits.
var levelRole = map[Action]string{ActRead: RoleViewer, ActEdit: RoleEditor, ActAdminister: RoleAdmin, ActOwn: RoleOwner}

// Decide says whether who may do act to res. It fails closed: an action it does not know, and a caller with no role (a context
// that carried no one), are refused.
func Decide(who Principal, act Action, res Resource) Decision {
	switch act {
	case ActRead, ActEdit, ActAdminister, ActOwn:
		return Decision{Allowed: roleRank[who.Role] >= roleRank[levelRole[act]] && roleRank[who.Role] > 0}
	case ActGrantRole:
		switch who.Role {
		case RoleOwner:
			return Decision{Allowed: validRole(res.Role)}
		case RoleAdmin:
			return Decision{Allowed: res.Role == RoleEditor || res.Role == RoleViewer}
		}
	case ActChangeAccess:
		return Decision{Allowed: !res.Widening || Decide(who, ActAdminister, Resource{}).Allowed, Widens: res.Widening}
	}
	return Decision{}
}

// Allows is Decide for an action that depends on nothing but the caller's role.
func Allows(who Principal, act Action) bool { return Decide(who, act, Resource{}).Allowed }

// Standing is a caller known only by the role they hold, for code that has looked a role up (a membership) and not a request.
func Standing(role string) Principal { return Principal{Role: role} }

type ctxPrincipal struct{}

// WithCaller is ctx carrying p as the caller, for CallerFrom and ActorFrom to read.
func WithCaller(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, ctxPrincipal{}, p)
}

// CallerFrom is the caller a context carries, or the zero Principal - which holds no role and so may do nothing - when it
// carries none. A code path that forgot to carry one therefore cannot widen anything.
func CallerFrom(ctx context.Context) Principal {
	p, _ := ctx.Value(ctxPrincipal{}).(Principal)
	return p
}
