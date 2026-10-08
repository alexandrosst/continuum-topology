package fusionapi

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"regexp"
	"strings"
)

// The native mirror: what Prometheus, Loki and Tempo answer on their own, served under FUSION's umbrella with FUSION's
// credentials, rate limit and timeout, and nothing else of theirs - no write, no admin, no push. A client written for a
// backend (a Grafana datasource, promtool, logcli) works against it unchanged apart from the address and the token.
//
// It is a read-only passthrough: the request is forwarded as the client wrote it and the answer comes back in the
// backend's own format. Because a query written by hand cannot be narrowed to a namespace or a cluster without
// understanding the query, only a caller whose access has no such limit may use it (the same rule as a raw query).

// Mirrored stores.
const (
	MirrorPrometheus = "prometheus"
	MirrorLoki       = "loki"
	MirrorTempo      = "tempo"
)

const (
	maxMirrorQuery = 16 << 10 // the query string of a forwarded request
	maxMirrorBody  = 1 << 20  // the form of a forwarded POST (a long query)
)

// MirrorResponse is a backend's answer, ready to be written back.
type MirrorResponse struct {
	Status      int
	ContentType string
	Body        []byte
}

var (
	// A name that goes into a forwarded path: a label (`__name__`, `k8s.pod.name`), a Tempo tag (`resource.service.name`,
	// `.http.method`) or a trace id. No slash, no dot-dot, nothing a path could be built from.
	mirrorSegment = regexp.MustCompile(`^[A-Za-z0-9_.:\-]{1,256}$`)
	mirrorHeaders = []string{"Accept", "Content-Type", "X-Loki-Response-Encoding-Flags"}
)

// MirrorSegment checks a value that is about to become one path segment of a forwarded request.
func MirrorSegment(name, v string) error {
	if !mirrorSegment.MatchString(v) || strings.Contains(v, "..") {
		return badRequest("%s is not a valid name here", name)
	}
	return nil
}

// Mirror forwards a request to one store and returns its answer. upstreamPath is the backend's own path
// ("/api/v1/query", "/loki/api/v1/labels", "/api/search"); the caller has already matched it against the allowed list.
// The query string and, for a POST, the form are forwarded as written.
func (c *Client) Mirror(ctx context.Context, s Scope, store, upstreamPath string, in *http.Request) (*MirrorResponse, error) {
	var base, name, signal string
	switch store {
	case MirrorPrometheus:
		base, name, signal = c.Prometheus, storeProm, SignalMetrics
	case MirrorLoki:
		base, name, signal = c.Loki, storeLoki, SignalLogs
	case MirrorTempo:
		base, name, signal = c.Tempo, storeTempo, SignalTraces
	default:
		return nil, badRequest("unknown store")
	}
	if err := s.needSignal(signal); err != nil {
		return nil, err
	}
	if err := s.needUnrestricted("the native " + name + " API"); err != nil {
		return nil, err
	}
	if base == "" {
		return nil, errf(http.StatusServiceUnavailable, "%s is not configured on this server", name)
	}
	if len(in.URL.RawQuery) > maxMirrorQuery {
		return nil, badRequest("the query string is longer than %d characters", maxMirrorQuery)
	}
	var body io.Reader
	switch in.Method {
	case http.MethodGet:
	case http.MethodPost:
		b, err := io.ReadAll(io.LimitReader(in.Body, maxMirrorBody+1))
		if err != nil {
			return nil, badRequest("the request body could not be read")
		}
		if len(b) > maxMirrorBody {
			return nil, errf(http.StatusRequestEntityTooLarge, "the request body is larger than %d bytes", maxMirrorBody)
		}
		if ct := in.Header.Get("Content-Type"); ct != "" && !strings.HasPrefix(ct, "application/x-www-form-urlencoded") {
			return nil, errf(http.StatusUnsupportedMediaType, "a POST here takes a form (application/x-www-form-urlencoded), as the backend does")
		}
		body = bytes.NewReader(b)
	default:
		return nil, errf(http.StatusMethodNotAllowed, "only GET and POST are served")
	}

	c.lanes()
	release, err := acquire(ctx, c.sem)
	if err != nil {
		return nil, err
	}
	defer release()
	u := base + upstreamPath
	if in.URL.RawQuery != "" {
		u += "?" + in.URL.RawQuery
	}
	req, err := http.NewRequestWithContext(ctx, in.Method, u, body)
	if err != nil {
		return nil, errf(http.StatusInternalServerError, "could not build the %s request", name)
	}
	req.Header.Set("Accept", "application/json")
	for _, h := range mirrorHeaders {
		if v := in.Header.Get(h); v != "" {
			req.Header.Set(h, v)
		}
	}
	resp, err := c.http().Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errf(http.StatusServiceUnavailable, "%s is not reachable. FUSION is off or still starting", name)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, c.maxBytes()+1))
	if err != nil {
		return nil, errf(http.StatusBadGateway, "%s: the answer was cut off", name)
	}
	if int64(len(data)) > c.maxBytes() {
		return nil, errf(http.StatusUnprocessableEntity, "%s: the answer is larger than this API returns (%d MiB). Narrow the time range or the query", name, c.maxBytes()>>20)
	}
	switch {
	case resp.StatusCode/100 == 2, resp.StatusCode == http.StatusNotFound, resp.StatusCode == http.StatusBadRequest, resp.StatusCode == http.StatusUnprocessableEntity:
		// What the backend says about the client's own query or id, in its own words and format: that is what a client
		// written for it parses.
		return &MirrorResponse{Status: resp.StatusCode, ContentType: mirrorContentType(resp.Header.Get("Content-Type")), Body: data}, nil
	case resp.StatusCode == http.StatusTooManyRequests:
		return nil, errf(http.StatusTooManyRequests, "%s is busy; try again shortly", name)
	case resp.StatusCode/100 == 4:
		return nil, errf(http.StatusBadRequest, "%s refused the request: %s", name, upstreamMessage(data))
	}
	// A store's own error text can name hosts, paths and internals; the caller gets the fact, not the text.
	return nil, errf(http.StatusBadGateway, "%s had a problem answering (status %d)", name, resp.StatusCode)
}

// mirrorContentType keeps a content type the mirror is willing to repeat.
func mirrorContentType(ct string) string {
	for _, ok := range []string{"application/json", "application/protobuf", "text/plain", "application/x-protobuf"} {
		if strings.HasPrefix(ct, ok) {
			return ct
		}
	}
	return "application/json"
}
