package flow

import (
	"log/slog"
	"net/http"
	"regexp"
	"time"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/agent/collect"
	"continuum/internal/probe"

	"google.golang.org/protobuf/encoding/protojson"
)

const (
	PathReport = "/v1/flows"
	// MaxBody bounds one report; a collector sends every 30 seconds. Nothing beyond it is read.
	MaxBody = 512 << 10
	// MaxRawFlows bounds the observations in one report, and is chosen so that a report of this many
	// flows, with every field at its longest, still fits in MaxBody (a test holds that). The collector
	// keeps the busiest flows and counts the rest as lost.
	MaxRawFlows = 1500
)

var nodeName = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9.-]{0,251}[a-zA-Z0-9])?$`)

// Pipeline is the agent's side of traffic observation: it verifies collectors' reports (signed with a
// secret only the flow collectors hold, not the one the node probe uses), attributes them to workloads
// and gathers them into windows for the server.
type Pipeline struct {
	secret []byte
	// Window is how far a report's timestamp may be from this clock; zero means probe.MaxClockSkew (5 minutes).
	Window time.Duration
	replay *probe.ReplayCache
	log    *slog.Logger
	now    func() time.Time

	Resolver   *Resolver
	Aggregator *Aggregator
}

func NewPipeline(secret []byte, source func() *collect.Index, log *slog.Logger) *Pipeline {
	if log == nil {
		log = slog.Default()
	}
	return &Pipeline{secret: secret, replay: probe.NewReplayCache(0), log: log, now: time.Now, Resolver: NewResolver(source), Aggregator: NewAggregator()}
}

// Handler serves POST /v1/flows.
func (p *Pipeline) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+PathReport, func(w http.ResponseWriter, req *http.Request) {
		_, body, status, err := probe.ReadSigned(w, req, probe.Signed{Secret: p.secret, MaxBody: MaxBody, Window: p.Window, Replay: p.replay, NodeOK: nodeName.MatchString, Now: p.now()})
		if err != nil {
			if status == http.StatusUnauthorized || status == http.StatusConflict {
				p.log.Warn("rejected a flow report", "reason", err.Error(), "from", req.RemoteAddr)
			}
			http.Error(w, http.StatusText(status), status)
			return
		}
		var rep continuumv1.FlowReport
		if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(body, &rep); err != nil || len(rep.Flows) > MaxRawFlows {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if rep.Method != "ebpf" && rep.Method != "conntrack" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		p.Ingest(&rep)
		w.WriteHeader(http.StatusNoContent)
	})
	return mux
}

// SetPaused stops (or resumes) taking reports in. While paused a report is read, verified and thrown away, nothing
// is held and nothing is sent: this is what "the server asked the agent to stop observing traffic" does.
func (p *Pipeline) SetPaused(paused bool) {
	if p != nil {
		p.Aggregator.SetPaused(paused)
	}
}

// Ingest attributes one report and adds it to the current window.
func (p *Pipeline) Ingest(rep *continuumv1.FlowReport) {
	p.Aggregator.Seen(rep.Node, rep.Method, rep.BytesKnown)
	p.Aggregator.AddLost(rep.Lost)
	for _, raw := range rep.Flows {
		if f, ok := p.Resolver.Resolve(raw, rep.Method, rep.BytesKnown); ok {
			p.Aggregator.Add(f)
		}
	}
}
