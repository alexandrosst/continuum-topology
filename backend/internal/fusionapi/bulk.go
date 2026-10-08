package fusionapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
)

// Bulk reads: many fused traces in one request without that request starving anyone else.
//
// Three limits work together. A request reads at most bulkParallel traces at once; every store call it makes waits in
// the Client's bulk lane (maxBulkUpstream of the maxUpstream slots) before the shared one, so bulk reads from any number
// of requests together leave the rest free for single reads; and a request is bounded by its own deadline, after which
// the traces not yet started are reported as such instead of the whole answer being lost.

const (
	// MaxBatch is the most traces one batch or fused search reads.
	MaxBatch     = 25
	bulkParallel = 3
)

type bulkKey struct{}

// WithBulk marks the store calls made under ctx as bulk reads.
func WithBulk(ctx context.Context) context.Context { return context.WithValue(ctx, bulkKey{}, true) }

func isBulk(ctx context.Context) bool { v, _ := ctx.Value(bulkKey{}).(bool); return v }

// BulkItem is the outcome for one trace of a bulk read: the fused trace, or why it could not be read.
type BulkItem struct {
	ID     string `json:"id"`
	Status int    `json:"status"`
	Error  string `json:"error,omitempty"`
	Trace  *Fused `json:"trace,omitempty"`
}

// NormalizeIDs trims and de-duplicates a list of trace ids, keeping the order. An id that is not a trace id is kept
// (the read of it reports the mistake against that id alone). More than MaxBatch is refused.
func NormalizeIDs(in []string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, id := range in {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		key := strings.ToLower(id)
		if n, err := NormalizeTraceID(id); err == nil {
			key = n
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, id)
	}
	if len(out) == 0 {
		return nil, badRequest("name at least one trace id")
	}
	if len(out) > MaxBatch {
		return nil, badRequest("a bulk read takes at most %d traces; split it", MaxBatch)
	}
	return out, nil
}

// FuseMany reads the traces with up to bulkParallel in flight. done, when set, is called as each finishes (in
// completion order, one call at a time); the returned slice is in the order of ids. A trace that fails does not stop the
// others. When ctx ends, the traces not yet started come back with status 504.
func (c *Client) FuseMany(ctx context.Context, s Scope, ids []string, opts FuseOptions, done func(BulkItem)) []BulkItem {
	ctx = WithBulk(ctx)
	out := make([]BulkItem, len(ids))
	if err := opts.defaults(); err != nil { // one answer for all, not one per trace
		st, msg := bulkFailure(err)
		for i, id := range ids {
			out[i] = BulkItem{ID: id, Status: st, Error: msg}
			if done != nil {
				done(out[i])
			}
		}
		return out
	}
	if opts.Extras != nil && (opts.Topology || opts.Changes) {
		opts.Extras = &topologyOnce{Extras: opts.Extras} // one topology for the whole batch, not one per trace
	}
	var mu sync.Mutex
	finish := func(i int, it BulkItem) {
		mu.Lock()
		defer mu.Unlock()
		out[i] = it
		if done != nil {
			done(it)
		}
	}
	work := make(chan int)
	var wg sync.WaitGroup
	workers := min(bulkParallel, len(ids))
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range work {
				f, err := c.FuseTrace(ctx, s, ids[i], opts)
				if err != nil {
					st, msg := bulkFailure(err)
					finish(i, BulkItem{ID: ids[i], Status: st, Error: msg})
					continue
				}
				finish(i, BulkItem{ID: ids[i], Status: http.StatusOK, Trace: f})
			}
		}()
	}
feed:
	for i := range ids {
		select {
		case work <- i:
		case <-ctx.Done():
			for j := i; j < len(ids); j++ {
				finish(j, BulkItem{ID: ids[j], Status: http.StatusGatewayTimeout, Error: "not read: the time limit ran out before this trace was started"})
			}
			break feed
		}
	}
	close(work)
	wg.Wait()
	return out
}

// topologyOnce reads the topology the first time a batch asks and gives every trace of the batch that same view, so the
// batch is joined to one consistent picture and the server builds it once. (A view is only read after it is built.)
type topologyOnce struct {
	Extras
	once sync.Once
	view *TopologyView
	err  error
}

func (t *topologyOnce) Topology(ctx context.Context) (*TopologyView, error) {
	t.once.Do(func() { t.view, t.err = t.Extras.Topology(ctx) })
	return t.view, t.err
}

// bulkFailure is the status and the words for one trace's failure.
func bulkFailure(err error) (int, string) {
	var fe *Error
	switch {
	case errors.As(err, &fe):
		return fe.Status, fe.Msg
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return http.StatusGatewayTimeout, "not finished: the time limit ran out while this trace was being read"
	}
	return http.StatusInternalServerError, "could not read this trace"
}

// BulkSummary counts what a bulk read came to.
type BulkSummary struct {
	Requested int `json:"requested"`
	OK        int `json:"ok"`
	Failed    int `json:"failed"`
}

// Summarise counts the items.
func Summarise(items []BulkItem) BulkSummary {
	s := BulkSummary{Requested: len(items)}
	for _, it := range items {
		if it.Status == http.StatusOK {
			s.OK++
		} else {
			s.Failed++
		}
	}
	return s
}
