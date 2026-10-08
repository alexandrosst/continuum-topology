// Package fusionapi reads what FUSION saved - metrics from Prometheus, logs from Loki, traces from Tempo - on behalf of a
// caller whose rights are a Scope, and joins the three into one object around a trace.
//
// It knows nothing about tokens, organisations or HTTP routes: the server (internal/server/fusion_data.go) decides who
// the caller is and hands this package a Scope; everything here is about turning that Scope into restrictions the
// stores themselves enforce (a matcher added to every query it builds), and then checking the answers once more.
//
// A Scope that limits namespaces or clusters is only ever applied to queries this package builds itself from
// structured filters. Arbitrary PromQL, LogQL or TraceQL cannot be restricted safely without parsing it, so a limited
// Scope may not send one (see Scope.Unrestricted and the Raw* methods).
package fusionapi

import (
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

// The three signal types FUSION keeps, one store each.
const (
	SignalMetrics = "metrics"
	SignalLogs    = "logs"
	SignalTraces  = "traces"
)

// Signals is every signal type, in the order the stores are listed everywhere.
var Signals = []string{SignalMetrics, SignalLogs, SignalTraces}

// The resource attributes a Scope is matched against. They are the same ones the FUSION chart promotes to labels in
// Prometheus (prometheus.promoteResourceAttributes) and Loki keeps as an index label or structured metadata.
const (
	attrNamespace = "k8s.namespace.name"
	attrCluster   = "continuum.cluster.id"
	attrService   = "service.name"
	attrPod       = "k8s.pod.name"
	attrNode      = "k8s.node.name"
	attrInstance  = "service.instance.id"
)

// Scope is what a caller may read. The zero value is a caller who may read nothing; use AllSignals for the full
// read.
type Scope struct {
	// Signals the caller may read, from Signals. Empty means none (a token with "all" is stored with all three listed
	// by the server; see NormalizeSignals).
	Signals []string
	// Namespaces and Clusters limit what is visible to telemetry whose k8s.namespace.name / continuum.cluster.id is
	// one of them. Empty means no limit on that dimension. Telemetry without the attribute is invisible to a Scope
	// that limits it.
	Namespaces []string
	Clusters   []string
	// Services narrows the lists, searches and metric reads this package builds to telemetry whose service.name is one of these. It is
	// not a right the caller lacks but a choice the caller made (an Ikhnos application, resolved to its services by the server), so it
	// is not part of Unrestricted: it is applied only to the queries built from structured filters, and never to reading one trace.
	Services []string
}

// AllSignals is the Scope of a caller who may read everything: a person signed in as an administrator.
func AllSignals() Scope { return Scope{Signals: slices.Clone(Signals)} }

// Allows reports whether the caller may read a signal type at all.
func (s Scope) Allows(signal string) bool { return slices.Contains(s.Signals, signal) }

// Unrestricted is whether the caller has no namespace or cluster limit, which is what lets it send a raw query.
func (s Scope) Unrestricted() bool { return len(s.Namespaces) == 0 && len(s.Clusters) == 0 }

// NamespaceVisible and ClusterVisible check a result's own attributes against the Scope; they are the second line
// behind the matchers the queries carry.
func (s Scope) NamespaceVisible(ns string) bool {
	return len(s.Namespaces) == 0 || (ns != "" && slices.Contains(s.Namespaces, ns))
}

func (s Scope) ClusterVisible(cl string) bool {
	return len(s.Clusters) == 0 || (cl != "" && slices.Contains(s.Clusters, cl))
}

// NormalizeSignals validates a requested list of signal types and returns it in the canonical order, deduplicated.
// An empty request means all three.
func NormalizeSignals(in []string) ([]string, error) {
	if len(in) == 0 {
		return slices.Clone(Signals), nil
	}
	for _, s := range in {
		if !slices.Contains(Signals, s) {
			return nil, fmt.Errorf("%q is not a signal type (metrics, logs or traces)", s)
		}
	}
	var out []string
	for _, s := range Signals {
		if slices.Contains(in, s) {
			out = append(out, s)
		}
	}
	return out, nil
}

// Error is a failure this package wants reported with a particular HTTP status.
type Error struct {
	Status int
	Msg    string
}

func (e *Error) Error() string { return e.Msg }

func errf(status int, format string, a ...any) *Error {
	return &Error{Status: status, Msg: fmt.Sprintf(format, a...)}
}

func badRequest(format string, a ...any) *Error { return errf(http.StatusBadRequest, format, a...) }

// denied is what a Scope that does not allow something gets back.
func denied(format string, a ...any) *Error { return errf(http.StatusForbidden, format, a...) }

// needSignal is the error for a signal type the caller's Scope does not include.
func (s Scope) needSignal(signal string) error {
	if s.Allows(signal) {
		return nil
	}
	return denied("this token cannot read %s", signal)
}

// needUnrestricted is the error for a raw query sent with a limited Scope.
func (s Scope) needUnrestricted(what string) error {
	if s.Unrestricted() {
		return nil
	}
	return denied("%s can only be used by a token with no namespace or cluster limit; use the structured filters instead", what)
}

// maxValue bounds one filter value (a service name, a namespace, a trace id, a text to look for).
const maxValue = 253

// checkValue accepts a value that is safe to quote into a query: not empty, not too long, valid UTF-8, and no control
// characters.
func checkValue(name, v string) error {
	if v == "" || len(v) > maxValue || !utf8.ValidString(v) {
		return badRequest("%s must be 1 to %d characters of text", name, maxValue)
	}
	for _, r := range v {
		if r < 0x20 || r == 0x7f {
			return badRequest("%s must not contain control characters", name)
		}
	}
	return nil
}

var hexID = regexp.MustCompile(`^[0-9a-fA-F]+$`)

// NormalizeTraceID returns a trace id as 32 lower-case hex digits, padding the zeros some tools trim.
func NormalizeTraceID(id string) (string, error) { return normHex("trace id", id, 32) }

// NormalizeSpanID returns a span id as 16 lower-case hex digits.
func NormalizeSpanID(id string) (string, error) { return normHex("span id", id, 16) }

func normHex(what, id string, width int) (string, error) {
	id = strings.TrimSpace(id)
	if id == "" || len(id) > width || !hexID.MatchString(id) {
		return "", badRequest("%s must be up to %d hexadecimal digits", what, width)
	}
	id = strings.ToLower(id)
	return strings.Repeat("0", width-len(id)) + id, nil
}

// quote renders a string for a PromQL, LogQL or TraceQL string literal: all three read the double-quoted form with
// Go-style escapes.
func quote(s string) string { return strconv.Quote(s) }

// regexAny is a regular expression matching exactly one of the values (all three stores anchor a regex).
func regexAny(vals []string) string {
	q := make([]string, len(vals))
	for i, v := range vals {
		q[i] = regexp.QuoteMeta(v)
	}
	return strings.Join(q, "|")
}
