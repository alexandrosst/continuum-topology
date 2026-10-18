package estate

import (
	"context"
	"encoding/json"
	"errors"

	"continuum/internal/kernel"
	"continuum/internal/store"
	"continuum/internal/workspace"
)

// The human layer's document (saved views, declared applications): what may be stored, and what a save does besides storing it.
// It sat in auth.go among the accounts, which it has nothing to do with, and called into the applications graph from there.

// MaxWorkspaceBytes bounds the stored document.
const MaxWorkspaceBytes = 8 << 20

// SaveWorkspace stores the document if expectRev is still current. On a conflict it returns the
// stored workspace with a KindConflict error.
func (c *Service) SaveWorkspace(ctx context.Context, actor string, expectRev int64, data []byte) (store.Workspace, error) {
	if len(data) > MaxWorkspaceBytes {
		return store.Workspace{}, kernel.Errf(kernel.KindInvalid, "workspace is larger than %d MB", MaxWorkspaceBytes>>20)
	}
	if err := checkWorkspaceJSON(data); err != nil {
		return store.Workspace{}, kernel.Errf(kernel.KindInvalid, "%v", err)
	}
	// Read before Put overwrites it: the only way recordApplicationsGraph, below, can tell what a save
	// actually changed rather than just versioning the result. Best-effort like everything downstream of
	// it - an error here just means every application in this save looks newly created to the graph.
	prev, _ := c.Store.GetWorkspace(ctx, c.OrgID)
	// Only what people declared is stored. Observed facts belong to the agents and are held apart from the workspace;
	// a document from an older client that still carries them is reduced to what is declared, with a note.
	data, rep, err := workspace.Declare(data)
	var newer workspace.ErrNewer
	if errors.As(err, &newer) {
		return store.Workspace{}, &kernel.Error{Kind: kernel.KindInvalid, Msg: err.Error(), Data: map[string]any{"supportedVersion": workspace.CurrentVersion, "documentVersion": newer.Have}}
	}
	if err != nil {
		return store.Workspace{}, kernel.Errf(kernel.KindInvalid, "%v", err)
	}
	w, err := c.Store.PutWorkspace(ctx, c.OrgID, expectRev, data, actor, c.Now())
	if errors.Is(err, store.ErrConflict) {
		return w, kernel.Errf(kernel.KindConflict, "the workspace was changed by someone else")
	}
	if err != nil {
		return store.Workspace{}, err
	}
	if rep.Changed() {
		w.Note = rep.Note()
	}
	if c.OnWorkspace != nil {
		c.OnWorkspace(w.Rev)
	}
	// The one moment this server can be sure an application was created, renamed, re-scoped or removed.
	c.recordApplicationsGraph(ctx, prev.Data, data)
	// One audit entry per save would drown everything else; the workspace has its own revision history in the row.
	return w, nil
}

// checkWorkspaceJSON accepts a JSON object that declares a numeric schemaVersion. The server does
// not interpret anything else: the UI owns the shape and its migrations.
func checkWorkspaceJSON(data []byte) error {
	var probe struct {
		SchemaVersion *int `json:"schemaVersion"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return errors.New("workspace must be a JSON object")
	}
	if probe.SchemaVersion == nil {
		return errors.New("workspace must declare a schemaVersion")
	}
	return nil
}
