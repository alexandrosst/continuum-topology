package server

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"continuum/internal/store"
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
// declared bytes (workspace.Declare's own return value), exactly as SaveWorkspace is about to persist it;
// prevData is the same document as it stood immediately before this save (SaveWorkspace's own read,
// before its PutWorkspace overwrote it), used only to work out what changed - nil or unreadable is treated
// as "nothing existed before", so every application in data reads as newly created rather than blocking
// the save's own recording.
//
// Alongside the version, this now writes one Event per fact that changed - created, renamed, its
// description or discovery fields, its member services, or removed - in the same one-event-per-fact
// vocabulary history.Diff already uses for the polled kinds (service-added, service-scaled, ...), and
// links them to the version they explain the same way (see graph.DB.LinkEventChanges). Before this,
// RecordEntity alone gave Timeline the "what" (a new doc, differing from the last) but never the "why";
// an application's own Timeline entry looked unlike every other kind's for exactly that reason.
//
// Best-effort and silent on failure, same as recordAgentGraph: the workspace save itself is what is
// durable (its own revision history already holds this document in full), so a graph outage only narrows
// the timeline view of one application's history, never the save itself or what it changed.
func (c *Core) recordApplicationsGraph(ctx context.Context, prevData, data []byte) {
	er, ok := c.Store.(entityRecorder)
	if !ok {
		return
	}
	apps, err := workspace.Applications(data)
	if err != nil {
		c.Log.Warn("history: could not read applications from the saved workspace", "err", err)
		return
	}
	prevApps, _ := workspace.Applications(prevData) // best-effort: nothing before, or unreadable, just means every app below looks newly created
	prevByID := make(map[string]workspace.ApplicationDoc, len(prevApps))
	for _, p := range prevApps {
		prevByID[p.ID] = p
	}
	now := c.Now()

	ids := make([]string, len(apps))
	for i, app := range apps {
		ids[i] = app.ID
	}
	var evs []store.Event
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
		for _, id := range closed {
			name := id
			if p, had := prevByID[id]; had {
				name = p.Name
			}
			evs = append(evs, store.Event{At: now, Kind: "application-removed", TargetKind: "application", TargetID: id, Name: name,
				Detail: fmt.Sprintf("%q was removed", name), Severity: "notice"})
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
		if prev, had := prevByID[app.ID]; had {
			evs = append(evs, diffApplicationDoc(prev, app, now)...)
		} else {
			evs = append(evs, store.Event{At: now, Kind: "application-added", TargetKind: "application", TargetID: app.ID, Name: app.Name,
				Detail: fmt.Sprintf("%q was created with %d service(s)", app.Name, len(app.ServiceIDs)), Severity: "notice"})
		}
	}

	if len(evs) == 0 {
		return
	}
	if err := c.Store.AddEvents(ctx, c.OrgID, evs); err != nil {
		c.Log.Warn("history: could not record why an application's state changed", "err", err)
		return
	}
	if el, ok := c.Store.(eventLinker); ok {
		if err := el.LinkEventChanges(ctx, c.OrgID, now, evs); err != nil {
			c.Log.Warn("history: could not link an application's events to what they explain", "err", err)
		}
	}
}

// diffApplicationDoc compares one application's previous declared shape against its new one and reports,
// field by field, what changed - the same discipline history.Diff already follows for the seven polled
// kinds (one event per fact, not one "something changed" event per entity), applied to the one kind that
// only ever changes through a workspace save rather than a poll.
func diffApplicationDoc(prev, cur workspace.ApplicationDoc, now time.Time) []store.Event {
	base := store.Event{At: now, TargetKind: "application", TargetID: cur.ID, Name: cur.Name}
	var evs []store.Event
	if prev.Name != cur.Name {
		e := base
		e.Kind = "application-renamed"
		e.Detail = fmt.Sprintf("%s → %s", prev.Name, cur.Name)
		evs = append(evs, e)
	}
	if prev.Description != cur.Description {
		e := base
		e.Kind = "application-description"
		switch {
		case prev.Description == "":
			e.Detail = "a description was added"
		case cur.Description == "":
			e.Detail = "its description was cleared"
		default:
			e.Detail = "its description changed"
		}
		evs = append(evs, e)
	}
	if prev.Origin != cur.Origin || prev.Confidence != cur.Confidence {
		e := base
		e.Kind = "application-discovery"
		e.Detail = fmt.Sprintf("origin %s (%s) → %s (%s)", orDash(prev.Origin), orDash(prev.Confidence), orDash(cur.Origin), orDash(cur.Confidence))
		evs = append(evs, e)
	}
	added, removed := stringsAdded(cur.ServiceIDs, prev.ServiceIDs), stringsAdded(prev.ServiceIDs, cur.ServiceIDs)
	if len(added) > 0 || len(removed) > 0 {
		e := base
		e.Kind, e.Severity = "application-membership", "notice"
		switch {
		case len(added) > 0 && len(removed) > 0:
			e.Detail = fmt.Sprintf("%d service(s) added (%s), %d removed (%s)", len(added), shortList(added), len(removed), shortList(removed))
		case len(added) > 0:
			e.Detail = fmt.Sprintf("%d service(s) added: %s", len(added), shortList(added))
		default:
			e.Detail = fmt.Sprintf("%d service(s) removed: %s", len(removed), shortList(removed))
		}
		evs = append(evs, e)
	}
	return evs
}

// stringsAdded returns what is in a but not in b, sorted - used both directions to turn two member lists
// into "added" and "removed".
func stringsAdded(a, b []string) []string {
	in := make(map[string]bool, len(b))
	for _, x := range b {
		in[x] = true
	}
	var out []string
	for _, x := range a {
		if !in[x] {
			out = append(out, x)
		}
	}
	sort.Strings(out)
	return out
}

// shortList renders up to 3 ids and says how many more, the same shape history.Diff's own names() gives
// for a list of nodes - member service ids are opaque, so there is no name to look up, but the same
// "don't dump a hundred ids into one line" discipline still applies.
func shortList(ids []string) string {
	if len(ids) > 3 {
		return strings.Join(ids[:3], ", ") + fmt.Sprintf(" and %d more", len(ids)-3)
	}
	return strings.Join(ids, ", ")
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
