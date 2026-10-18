package estate

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"continuum/internal/accounts"
	"continuum/internal/httpkit"
	"continuum/internal/kernel"
	"continuum/internal/workspace"
)

// getWorkspace returns the stored document. "?meta=1" returns only the revision, which is what the
// UI polls to notice that someone else saved.
func (h *API) getWorkspace(w http.ResponseWriter, r *http.Request) {
	ws, err := h.For(r).C.Store.GetWorkspace(r.Context(), h.For(r).C.OrgID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := map[string]any{"rev": ws.Rev, "updatedAt": rfcNZ(ws.UpdatedAt), "updatedBy": ws.UpdatedBy, "formatVersion": workspace.CurrentVersion}
	if r.URL.Query().Get("meta") == "" && ws.Rev > 0 {
		data, note := ws.Data, ws.Note
		if workspace.Peek(data) < workspace.CurrentVersion {
			// A document saved before observed facts were held apart from the workspace: shown as it would be saved now.
			if d, rep, err := workspace.Declare(data); err == nil {
				data = d
				if note == "" && rep.Changed() {
					note = rep.Note()
				}
			}
		}
		out["data"] = json.RawMessage(data)
		if note != "" {
			out["note"] = note
		}
	}
	httpkit.WriteJSON(w, 200, out)
}

func rfcNZ(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return kernel.RFC(t)
}

// putWorkspace saves the document. The If-Match header carries the revision the client last saw
// (0 for the first save); a mismatch answers 409 with the current revision so the UI can offer
// "load theirs" or "overwrite".
func (h *API) putWorkspace(w http.ResponseWriter, r *http.Request) {
	rev, err := strconv.ParseInt(strings.Trim(r.Header.Get("If-Match"), `"`), 10, 64)
	if err != nil || rev < 0 {
		httpkit.WriteErr(w, http.StatusPreconditionRequired, "send the revision you last saw in If-Match")
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		httpkit.WriteErr(w, http.StatusRequestEntityTooLarge, "workspace is too large")
		return
	}
	ws, err := h.For(r).C.SaveWorkspace(r.Context(), httpkit.Actor(r), rev, body)
	var e *kernel.Error
	if errors.As(err, &e) && e.Kind == kernel.KindConflict {
		httpkit.WriteJSON(w, http.StatusConflict, map[string]any{"error": e.Msg, "rev": ws.Rev, "updatedAt": rfcNZ(ws.UpdatedAt), "updatedBy": ws.UpdatedBy})
		return
	}
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := map[string]any{"rev": ws.Rev, "updatedAt": kernel.RFC(ws.UpdatedAt), "updatedBy": ws.UpdatedBy}
	if ws.Note != "" {
		out["note"] = ws.Note
	}
	httpkit.WriteJSON(w, 200, out)
}

// state is what the UI polls. The audit trail is part of it only for administrators and owners: it names
// people, and it is what they review; everyone else gets the topology and an empty list.
func (h *API) state(w http.ResponseWriter, r *http.Request) {
	doc, err := h.For(r).StateFor(r.Context(), accounts.Allows(httpkit.Principal(r), accounts.ActAdminister))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if accounts.Allows(httpkit.Principal(r), accounts.ActEdit) {
		img := h.Settings.Images(h.For(r).C.Core)
		for i, ag := range doc.Agents {
			if ag.AccessTier < ag.InstalledTier {
				cmd, err := UpgradeCommand(h.Settings, img, ag.AccessTier, ag.Namespace, ag.ReleaseName)
				if err != nil {
					h.installConfigError(r, err) // logged; the agent is shown without the command
				}
				doc.Agents[i].HardenHelm = cmd
			}
		}
	} else {
		withoutDiagnostics(&doc)
	}
	httpkit.WriteJSON(w, 200, doc)
}

// model serves the effective model: GET /api/v1/orgs/{org}/model. It carries an entity tag that changes exactly when
// the model's content does (not with the clock), so a poller sends If-None-Match and gets 304 while nothing changed.
func (h *API) model(w http.ResponseWriter, r *http.Request) {
	m, etag, err := h.For(r).Model(r.Context())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "no-cache")
	if etagMatches(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	httpkit.WriteJSON(w, 200, m)
}

// etagMatches implements If-None-Match for a strong tag: "*" or any listed tag (a weak validator W/ counts).
func etagMatches(header, etag string) bool {
	for _, p := range strings.Split(header, ",") {
		p = strings.TrimSpace(p)
		if p == "*" || strings.TrimPrefix(p, "W/") == etag {
			return true
		}
	}
	return false
}

// withoutDiagnostics removes what only people who can change an agent's access may see.
func withoutDiagnostics(doc *StateDoc) {
	for i := range doc.Agents {
		doc.Agents[i].Diagnostics, doc.Agents[i].Consent, doc.Agents[i].Teardown = nil, nil, nil
	}
}
