// Package httpkit is what every JSON handler of the API needs and none of them owns: write a JSON answer, write an error, read a
// request body, say who is calling, and name what a route asks of the caller. It is not a module. It stands on the kernel (the error
// kinds) and on the accounts' vocabulary of who is calling, and on nothing else, so that a module's handlers can live in the module's
// own package and use it without reaching back into the shell.
//
// What stays in the shell is what needs the server itself: finding the organisation a request is for (the Tenant), the rate limiters,
// and the guard that authenticates a request and puts the caller and the organisation into its context.
package httpkit

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"continuum/internal/accounts"
	"continuum/internal/kernel"
)

// WriteJSON writes v as the body of the answer, with the status code.
func WriteJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// WriteErr writes {"error": msg} with the status code.
func WriteErr(w http.ResponseWriter, code int, msg string) {
	WriteJSON(w, code, map[string]string{"error": msg})
}

// status is the HTTP status of each kind of error a module can return.
var status = map[kernel.Kind]int{
	kernel.KindInvalid:           http.StatusBadRequest,
	kernel.KindUnauthenticated:   http.StatusUnauthorized,
	kernel.KindNotFound:          http.StatusNotFound,
	kernel.KindConflict:          http.StatusConflict,
	kernel.KindRateLimited:       http.StatusTooManyRequests,
	kernel.KindInternal:          http.StatusInternalServerError,
	kernel.KindForbidden:         http.StatusForbidden,
	kernel.KindTwoFactorRequired: http.StatusUnauthorized,
}

// Fail answers with the error. A kernel.Error becomes its kind's status and its message (and its Data, when it carries some, as
// more fields of the same object); anything else is logged and answered as an internal error that says nothing about what happened.
func Fail(w http.ResponseWriter, log *slog.Logger, err error) {
	var e *kernel.Error
	if errors.As(err, &e) {
		code := status[e.Kind]
		if len(e.Data) > 0 {
			out := map[string]any{"error": e.Msg}
			for k, v := range e.Data {
				out[k] = v
			}
			WriteJSON(w, code, out)
			return
		}
		WriteErr(w, code, e.Msg)
		return
	}
	log.Error("admin request failed", "err", err)
	WriteErr(w, http.StatusInternalServerError, "internal error")
}

// Decode reads the request body into v and refuses a field v does not have.
func Decode(r *http.Request, v any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return kernel.Errf(kernel.KindInvalid, "invalid request body")
	}
	return nil
}

// Reasoned answers a route whose body may carry {"reason": ...}: it hands the reason to do and answers 204 when do succeeds.
func Reasoned(w http.ResponseWriter, r *http.Request, log *slog.Logger, do func(reason string) error) {
	var req struct {
		Reason string `json:"reason"`
	}
	if err := Decode(r, &req); err != nil {
		Fail(w, log, err)
		return
	}
	if err := do(req.Reason); err != nil {
		Fail(w, log, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ParseTime reads the RFC 3339 time in the query parameter name: the zero time if it is not there.
func ParseTime(r *http.Request, name string) (time.Time, error) {
	v := r.URL.Query().Get(name)
	if v == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return time.Time{}, fmt.Errorf("%s must be an RFC 3339 time", name)
	}
	return t, nil
}

// Principal is who is calling, as the guard put it in the request's context.
func Principal(r *http.Request) accounts.Principal { return accounts.CallerFrom(r.Context()) }

// Actor is the name the caller is recorded under in the audit trail.
func Actor(r *http.Request) string { return Principal(r).User.Username }

// Need is what a route asks of the caller. The organisation levels also require the {org} in the path to be one the caller belongs to.
type Need int

const (
	AnySession Need = iota // signed in, even if a password change is still pending
	Settled                // signed in, password settled, no organisation involved
	MemberRole             // belongs to the organisation (any role: reads)
	EditorRole
	AdminRole
	OwnerRole
)

// String is the name the route table shows for the need.
func (n Need) String() string {
	switch n {
	case AnySession:
		return "session"
	case Settled:
		return "settled"
	case MemberRole:
		return "member"
	case EditorRole:
		return "editor"
	case AdminRole:
		return "admin"
	case OwnerRole:
		return "owner"
	}
	return "unknown"
}

// Action is the kind of act the need stands for in the accounts' policy; zero for the levels that name no organisation.
func (n Need) Action() accounts.Action {
	switch n {
	case MemberRole:
		return accounts.ActRead
	case EditorRole:
		return accounts.ActEdit
	case AdminRole:
		return accounts.ActAdminister
	case OwnerRole:
		return accounts.ActOwn
	}
	return 0
}

// OrgPath is the prefix of every route that belongs to one organisation. The {org} in it is what the guard checks the caller against.
const OrgPath = "/api/v1/orgs/{org}"

// Router is where a module's handlers are mounted. The shell's implementation guards each route by what it asks of the caller
// (authentication, the organisation, the role, the size of the body) and records it in the route table, so a module cannot mount a
// route that skips either.
type Router interface {
	Handle(pattern string, n Need, h http.HandlerFunc)
	// Open mounts a route that asks nothing of the caller: sign-in, registration, what the server says of itself.
	Open(pattern string, h http.HandlerFunc)
}
