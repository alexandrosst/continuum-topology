package fusionhttp

import (
	"net/http"

	"continuum/internal/fusionapi"
)

// Router is where the data API is mounted. The server records each route in its route table, so a route cannot be mounted
// without saying how it is guarded.
type Router interface {
	// Open mounts a route that asks nothing of the caller: the description of the API.
	Open(pattern string, h http.HandlerFunc)
	// Credential mounts a route that authenticates itself with a FUSION credential: a FUSION access token, or an administrator
	// of the FUSION organisation. The caller is not placed in an organisation by the path.
	Credential(pattern string, h http.Handler)
}

// Mount mounts the data API from the one table that also describes it, so a route cannot exist without being documented, and
// serves that description beside it. Each route refuses a query string it does not take (Op.CheckQuery) before its handler runs.
func (g *Guard) Mount(r Router, ops []Op) {
	for _, op := range ops {
		if op.Handler == nil { // the table can be read without handlers to describe the API, but not mounted without them
			panic("fusionhttp: " + op.Method + " " + op.Path + " has no handler")
		}
		h := g.Serve(g.checked(op))
		r.Credential(op.Method+" "+APIPath+op.Path, h)
		if op.AlsoPost {
			r.Credential("POST "+APIPath+op.Path, h)
		}
	}
	mountDocs(r, ops)
}

func (g *Guard) checked(op Op) Handler {
	return func(w http.ResponseWriter, r *http.Request, c *fusionapi.Client, who Caller) {
		if err := op.CheckQuery(r.URL.Query()); err != nil {
			g.Err(w, err)
			return
		}
		op.Handler(w, r, c, who)
	}
}
