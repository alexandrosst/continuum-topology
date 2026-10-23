package fusionhttp

import (
	"time"

	"ikhnos/internal/fusion"
	"ikhnos/internal/fusionapi"
)

// The answers of the data API that were maps at the handler. The fields are in the alphabetical order of their JSON names, which is the
// order a map was encoded in: the bytes on the wire are the ones they were before. From and To are the window the answer covers.

// logsDoc answers the log search: the lines found, and whether there were more than Limit.
type logsDoc struct {
	Entries   []fusionapi.LogEntry `json:"entries"`
	From      time.Time            `json:"from"`
	Limit     int                  `json:"limit"`
	To        time.Time            `json:"to"`
	Truncated bool                 `json:"truncated"`
}

// metricNamesDoc answers the list of metric names, with the category each belongs to.
type metricNamesDoc struct {
	Categories map[string]string `json:"categories"`
	From       time.Time         `json:"from"`
	Limit      int               `json:"limit"`
	Names      []string          `json:"names"`
	To         time.Time         `json:"to"`
	Truncated  bool              `json:"truncated"`
}

// metricSeriesDoc answers the list of series (each the labels that tell it apart).
type metricSeriesDoc struct {
	From      time.Time           `json:"from"`
	Limit     int                 `json:"limit"`
	Series    []map[string]string `json:"series"`
	To        time.Time           `json:"to"`
	Truncated bool                `json:"truncated"`
}

// metricRangeDoc answers a metric read over time, at a step of StepSeconds.
type metricRangeDoc struct {
	From        time.Time                `json:"from"`
	Limit       int                      `json:"limit"`
	Series      []fusionapi.MetricSeries `json:"series"`
	StepSeconds float64                  `json:"stepSeconds"`
	To          time.Time                `json:"to"`
	Truncated   bool                     `json:"truncated"`
}

// callerAccessDoc says what the caller is and may read, as the status answer tells it back.
type callerAccessDoc struct {
	Clusters   []string           `json:"clusters"`
	ExpiresAt  string             `json:"expiresAt,omitempty"`
	Kind       string             `json:"kind"`
	Name       string             `json:"name"`
	Namespaces []string           `json:"namespaces"`
	RawQueries bool               `json:"rawQueries"`
	Signals    []fusionapi.Signal `json:"signals"`
}

// statusDoc answers the data API's status: how FUSION is, and what the caller may read of it.
type statusDoc struct {
	Access callerAccessDoc     `json:"access"`
	Fusion fusion.FusionStatus `json:"fusion"`
}

// applicationsDoc answers the list of applications, with which stores could be read (Sources) and what went wrong in part (Warnings).
type applicationsDoc struct {
	Applications []fusionapi.AppView `json:"applications"`
	From         time.Time           `json:"from"`
	Sources      map[string]string   `json:"sources"`
	To           time.Time           `json:"to"`
	Warnings     []string            `json:"warnings"`
}

// serviceDoc is one service that has telemetry, with the Ikhnos applications it is in.
type serviceDoc struct {
	fusionapi.Application
	Applications []string `json:"applications"`
}

// servicesDoc answers the list of services that have telemetry.
type servicesDoc struct {
	From     time.Time         `json:"from"`
	Services []serviceDoc      `json:"services"`
	Sources  map[string]string `json:"sources"`
	To       time.Time         `json:"to"`
	Warnings []string          `json:"warnings"`
}

// tracesDoc answers a trace search. A fused search (fused=true) adds Results and Summary, read in full for each hit; asked to stream, the
// same document, as Type "traces" without them, is the first line, and the results follow a line each. Results is absent unless the search
// was fused (an empty list is a fused search that found nothing).
type tracesDoc struct {
	From      time.Time                `json:"from"`
	Limit     int                      `json:"limit"`
	Results   []fusionapi.BulkItem     `json:"results,omitzero"`
	Summary   *fusionapi.BulkSummary   `json:"summary,omitempty"`
	To        time.Time                `json:"to"`
	Traces    []fusionapi.TraceSummary `json:"traces"`
	Truncated bool                     `json:"truncated"`
	Type      string                   `json:"type,omitempty"`
}

// traceBatchDoc answers a batch read of traces: what each id gave, and the count of those that worked.
type traceBatchDoc struct {
	Results []fusionapi.BulkItem  `json:"results"`
	Summary fusionapi.BulkSummary `json:"summary"`
}
