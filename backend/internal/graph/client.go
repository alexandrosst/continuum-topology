// Package graph keeps Continuum's memory in a Neo4j graph: what the estate looked like at any moment
// (a temporal graph of versioned entities), what changed, and who did what.
//
// Neo4j Community has one database, so tenants are separated by the queries and nothing else. Every
// tenant-owned statement therefore goes through Scope, which refuses a statement that does not use
// the $org parameter and always supplies it from the caller's tenant, never from request content.
// Secrets (passwords, sessions, tokens, the CA) are deliberately not here: they stay in the SQLite
// store, which works when this database does not.
package graph

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// ErrUnavailable is returned while the database cannot be reached. Callers fall back to their buffer.
var ErrUnavailable = errors.New("the graph database is not reachable")

// Config says how to reach Neo4j. The password is never logged.
type Config struct {
	URL      string // http(s)://host:7474
	User     string
	Password string
	Database string // default "neo4j"
	Timeout  time.Duration
}

// Client speaks Neo4j's transactional HTTP API. One Run call is one atomic transaction.
type Client struct {
	cfg  Config
	base string
	http *http.Client

	mu        sync.Mutex
	downUntil time.Time
	lastErr   string
}

func NewClient(cfg Config) (*Client, error) {
	u, err := url.Parse(strings.TrimRight(cfg.URL, "/"))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("the Neo4j address must look like http://host:7474 (got %q)", cfg.URL)
	}
	if u.User != nil {
		return nil, errors.New("put the Neo4j user and password in their own settings, not in the address")
	}
	if cfg.Database == "" {
		cfg.Database = "neo4j"
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 30 * time.Second
	}
	return &Client{cfg: cfg, base: u.String() + "/db/" + url.PathEscape(cfg.Database), http: &http.Client{Timeout: cfg.Timeout}}, nil
}

// Stmt is one Cypher statement and its parameters. It can only be built by Scope.S (a statement for one
// tenant) or Global (one that deliberately touches no tenant data): the fields that say which are
// unexported, and Client.Run refuses a Stmt that was assembled by hand.
type Stmt struct {
	Q string
	P map[string]any

	kind stmtKind
	org  string // the tenant a scoped statement was built for
}

type stmtKind uint8

const (
	kindUnset stmtKind = iota // a hand-assembled Stmt{...}: refused
	kindScoped
	kindGlobal
)

// Global builds a statement that touches no tenant's data: the schema, the schema version, a liveness
// ping, the list of tenant ids. Every use is a deliberate decision (a test counts them). A query that
// takes the $org parameter is not global and is refused.
func Global(q string, p map[string]any) Stmt {
	if usesParam(q, "org") {
		panic("graph: a statement that uses $org is a tenant statement; build it with Scope.S: " + firstLine(q))
	}
	return Stmt{Q: q, P: p, kind: kindGlobal}
}

// check is what Client.Run requires of every statement before it is sent.
func (s Stmt) check() error {
	switch s.kind {
	case kindGlobal:
		if usesParam(s.Q, "org") {
			return errors.New("graph: refusing a global statement that uses $org")
		}
		return nil
	case kindScoped:
		if s.org == "" || !usesParam(s.Q, "org") {
			return errors.New("graph: refusing a tenant statement that does not use the $org parameter")
		}
		if o, _ := s.P["org"].(string); o != s.org {
			return errors.New("graph: refusing a tenant statement whose $org was changed after it was built")
		}
		return nil
	}
	return errors.New("graph: refusing a statement that was not built by Scope.S or Global: " + firstLine(s.Q))
}

// usesParam reports whether the Cypher text uses the parameter $name as a parameter: outside string
// literals, quoted identifiers and comments, and as a whole word ($org, not $organisation or a mention
// in a comment or a string).
func usesParam(q, name string) bool {
	for i := 0; i < len(q); i++ {
		switch c := q[i]; c {
		case '\'', '"', '`':
			// skip to the matching quote; a backslash escapes the next byte in strings
			for i++; i < len(q) && q[i] != c; i++ {
				if q[i] == '\\' && c != '`' {
					i++
				}
			}
		case '/':
			if i+1 < len(q) && q[i+1] == '/' {
				for i < len(q) && q[i] != '\n' {
					i++
				}
			} else if i+1 < len(q) && q[i+1] == '*' {
				end := strings.Index(q[i+2:], "*/")
				if end < 0 {
					return false
				}
				i += end + 3
			}
		case '$':
			rest := q[i+1:]
			if strings.HasPrefix(rest, name) {
				if n := len(name); n == len(rest) || !isIdent(rest[n]) {
					return true
				}
			}
		}
	}
	return false
}

func isIdent(b byte) bool {
	return b == '_' || (b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// Result is the rows one statement returned.
type Result struct {
	Cols []string
	Rows [][]any
}

// Healthy reports whether the last call worked (or none has failed recently).
func (c *Client) Healthy() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return time.Now().After(c.downUntil)
}

// LastError is the most recent reason the database could not be used ("" when it can).
func (c *Client) LastError() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastErr
}

func (c *Client) markDown(err error) {
	c.mu.Lock()
	c.downUntil = time.Now().Add(5 * time.Second)
	c.lastErr = err.Error()
	c.mu.Unlock()
}

func (c *Client) markUp() {
	c.mu.Lock()
	c.downUntil = time.Time{}
	c.lastErr = ""
	c.mu.Unlock()
}

type wireReq struct {
	Statements []wireStmt `json:"statements"`
}
type wireStmt struct {
	Statement  string         `json:"statement"`
	Parameters map[string]any `json:"parameters,omitempty"`
}
type wireResp struct {
	Results []struct {
		Columns []string `json:"columns"`
		Data    []struct {
			Row []any `json:"row"`
		} `json:"data"`
	} `json:"results"`
	Errors []struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"errors"`
}

// Run executes the statements as one transaction: all of them take effect or none do.
func (c *Client) Run(ctx context.Context, stmts ...Stmt) ([]Result, error) {
	if len(stmts) == 0 {
		return nil, nil
	}
	for _, s := range stmts {
		if err := s.check(); err != nil {
			return nil, err
		}
	}
	if !c.Healthy() {
		return nil, fmt.Errorf("%w: %s", ErrUnavailable, strings.TrimPrefix(c.LastError(), ErrUnavailable.Error()+": "))
	}
	req := wireReq{Statements: make([]wireStmt, len(stmts))}
	for i, s := range stmts {
		req.Statements[i] = wireStmt{Statement: s.Q, Parameters: s.P}
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/tx/commit", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	hr.SetBasicAuth(c.cfg.User, c.cfg.Password)
	hr.Header.Set("Content-Type", "application/json")
	hr.Header.Set("Accept", "application/json")
	res, err := c.http.Do(hr)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		e := fmt.Errorf("%w: %v", ErrUnavailable, scrub(err))
		c.markDown(e)
		return nil, e
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 512<<20))
	if err != nil {
		e := fmt.Errorf("%w: %v", ErrUnavailable, err)
		c.markDown(e)
		return nil, e
	}
	if res.StatusCode == http.StatusUnauthorized {
		e := fmt.Errorf("%w: the database refused the user name or password", ErrUnavailable)
		c.markDown(e)
		return nil, e
	}
	if res.StatusCode >= 500 {
		e := fmt.Errorf("%w: the database answered %d", ErrUnavailable, res.StatusCode)
		c.markDown(e)
		return nil, e
	}
	var w wireResp
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&w); err != nil {
		return nil, fmt.Errorf("unreadable answer from the database (%d): %w", res.StatusCode, err)
	}
	c.markUp()
	if len(w.Errors) > 0 {
		return nil, fmt.Errorf("graph query failed: %s: %s", w.Errors[0].Code, w.Errors[0].Message)
	}
	out := make([]Result, len(w.Results))
	for i, r := range w.Results {
		out[i].Cols = r.Columns
		for _, d := range r.Data {
			out[i].Rows = append(out[i].Rows, d.Row)
		}
	}
	return out, nil
}

// scrub keeps a transport error from carrying the address with credentials.
func scrub(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

// Scope runs statements for exactly one tenant.
type Scope struct {
	c   *Client
	org string
}

// For returns the scope of one tenant. The tenant id is the only thing that decides which data a
// statement can see; it must come from the authenticated request, never from its body.
func (c *Client) For(org string) *Scope { return &Scope{c: c, org: org} }

// S builds a tenant statement: the scope, not the caller, supplies the $org parameter. It panics on a
// programming error (no tenant, or a query that does not use $org as a parameter) rather than build
// something that could read another tenant's data; Client.Run checks the same again before sending.
func (s *Scope) S(q string, p map[string]any) Stmt {
	if s.org == "" {
		panic("graph: a tenant statement needs a tenant")
	}
	if !usesParam(q, "org") {
		panic("graph: a tenant statement must use $org: " + firstLine(q))
	}
	if p == nil {
		p = map[string]any{}
	}
	p["org"] = s.org
	return Stmt{Q: q, P: p, kind: kindScoped, org: s.org}
}

// Run runs statements built by this scope; a statement built for another tenant is refused.
func (s *Scope) Run(ctx context.Context, stmts ...Stmt) ([]Result, error) {
	for _, st := range stmts {
		if st.kind != kindScoped || st.org != s.org {
			return nil, errors.New("graph: a scope runs only the statements it built")
		}
	}
	return s.c.Run(ctx, stmts...)
}

func firstLine(q string) string {
	q = strings.TrimSpace(q)
	if i := strings.IndexByte(q, '\n'); i >= 0 {
		q = q[:i]
	}
	return q
}

// ---- value helpers for reading rows ----

func str(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case nil:
		return ""
	default:
		return fmt.Sprint(x)
	}
}

func i64(v any) int64 {
	switch x := v.(type) {
	case json.Number:
		n, err := x.Int64()
		if err != nil {
			f, _ := x.Float64()
			return int64(f)
		}
		return n
	case float64:
		return int64(x)
	case int64:
		return x
	case int:
		return int64(x)
	}
	return 0
}

func tm(v any) time.Time {
	s := str(v)
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }
