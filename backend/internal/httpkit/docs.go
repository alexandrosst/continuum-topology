package httpkit

// The documents every module's routes share. They are listed for the TypeScript written from the Go types (internal/wire) by the server,
// which mounts them all (internal/server/wire.go).

// ErrorDoc is the body of every refusal: what went wrong, in words a person can read. A kernel.Error that carries data (the fields a client
// can act on) answers with those as more fields of the same object (see Fail), which a type cannot name.
type ErrorDoc struct {
	Error string `json:"error"`
}

// OKDoc is the answer of a route that has nothing to say but that it did it.
type OKDoc struct {
	OK bool `json:"ok"`
}

// ReasonRequest is the body of a route that may be given a reason (see Reasoned): revoking, rejecting. It may be absent.
type ReasonRequest struct {
	Reason string `json:"reason"`
}
