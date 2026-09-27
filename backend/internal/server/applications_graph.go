package server

import (
	"context"
	"time"

	"continuum/internal/workspace"
)

// ---- versioning applications in the graph ----

// entityCloser is what a store adds when it keeps a graph: the ability to retire whichever entities of a
// kind have dropped out of a full picture the caller can enumerate (see graph.DB.CloseMissingEntities).
// A plain store does not implement it, the same way entityRecorder is optional.
type entityCloser interface {
	CloseMissingEntities(ctx context.Context, org string, at time.Time, kind string, keepIDs []string) ([]string, error)
}

// memberLinker is what a store adds when it keeps a graph: the ability to keep one entity's own edges of
// a relationship type in step with a set of member ids (see graph.DB.LinkEntities). A plain store does
// not implement it, the same way entityRecorder is optional.
type memberLinker interface {
	LinkEntities(ctx context.Context, org string, at time.Time, relType, kind, id, targetKind string, targetIDs []string) error
}

// applicationDoc is what the graph remembers about an application over time: everything a document
// declares about it, plus the services it currently contains - kept here rather than as an edge-only
// fact so Timeline can show "this application's membership changed" as an ordinary field diff, the same
// way an agent's discovery intent is a field on AgentSnapshot rather than something only visible by
// walking edges.
type applicationDoc struct {
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Origin      string   `json:"origin,omitempty"`
	Confidence  string   `json:"confidence,omitempty"`
	ServiceIDs  []string `json:"serviceIds,omitempty"`
}

// recordApplicationsGraph versions every application a just-saved workspace declares, and retires
// whichever ones dropped out of it - the one moment this server can be sure an application was created,
// renamed, re-scoped or removed, since nothing else ever changes one. data is the document's already-
// declared bytes (workspace.Declare's own return value), exactly as SaveWorkspace is about to persist it.
//
// Best-effort and silent on failure, same as recordAgentGraph: the workspace save itself is what is
// durable (its own revision history already holds this document in full), so a graph outage only narrows
// the timeline view of one application's history, never the save itself or what it changed.
func (c *Core) recordApplicationsGraph(ctx context.Context, data []byte) {
	er, ok := c.Store.(entityRecorder)
	if !ok {
		return
	}
	apps, err := workspace.Applications(data)
	if err != nil {
		c.Log.Warn("history: could not read applications from the saved workspace", "err", err)
		return
	}
	now := c.Now()

	ids := make([]string, len(apps))
	for i, app := range apps {
		ids[i] = app.ID
	}
	if closer, ok := c.Store.(entityCloser); ok {
		closed, err := closer.CloseMissingEntities(ctx, c.OrgID, now, "application", ids)
		if err != nil {
			c.Log.Warn("history: could not retire removed applications", "err", err)
		}
		if linker, ok := c.Store.(memberLinker); ok {
			for _, id := range closed {
				if err := linker.LinkEntities(ctx, c.OrgID, now, "CONTAINS", "application", id, "service", nil); err != nil {
					c.Log.Warn("history: could not retire a removed application's membership", "application", id, "err", err)
				}
			}
		}
	}

	for _, app := range apps {
		doc := applicationDoc{Name: app.Name, Description: app.Description, Origin: app.Origin, Confidence: app.Confidence, ServiceIDs: app.ServiceIDs}
		if err := er.RecordEntity(ctx, c.OrgID, now, "application", app.ID, app.Name, "", "", doc); err != nil {
			c.Log.Warn("history: could not record an application's state", "application", app.ID, "err", err)
			continue
		}
		if linker, ok := c.Store.(memberLinker); ok {
			if err := linker.LinkEntities(ctx, c.OrgID, now, "CONTAINS", "application", app.ID, "service", app.ServiceIDs); err != nil {
				c.Log.Warn("history: could not link an application to its services", "application", app.ID, "err", err)
			}
		}
	}
}
