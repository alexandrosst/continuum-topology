// Package workspace knows the shape of the one document a person authors: the declared side of the twin.
//
// The workspace holds what people said (sites, devices, declared links, policies, applications they accepted,
// the decisions they made, the values they overrode). It does NOT hold what agents observed: clusters, nodes,
// namespaces and services that discovery produced live on the server as observations and are referenced from
// the workspace by their stable id (the "refs" section) so that a person's overrides and assignments survive
// while the observation itself comes and goes.
//
// Format versions (the document's schemaVersion):
//
//	1-3  the document also carried discovered records (and their last observed values)
//	4    declared intent only: discovered records are replaced by refs {id -> overrides and assignments}
//
// Declare rewrites any older document into the current one. It is pure and idempotent: a version 4 document that
// is clean comes back unchanged (apart from key order), and one that still holds observed data (an old client)
// is cleaned. Nothing here needs the database, so the store, the server and tests all use it.
package workspace

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// CurrentVersion is the newest document format this build reads and writes.
const CurrentVersion = 4

// ErrNewer is returned for a document written by a newer version of the software.
type ErrNewer struct{ Have int }

func (e ErrNewer) Error() string {
	return fmt.Sprintf("this workspace uses format version %d, newer than the newest this server understands (%d). Nothing was changed: upgrade the server, or export the workspace from the version that wrote it", e.Have, CurrentVersion)
}

// Observed kinds are the collections that discovery fills. Everything a person says about one of them is kept as a ref.
var observedKinds = []struct{ Key, Kind string }{
	{"clusters", "cluster"},
	{"nodes", "node"},
	{"namespaces", "namespace"},
	{"services", "service"},
}

// Ref is what a person said about one observed record: the values they overrode and the assignments they made.
// It is keyed by the record's stable id in the document's "refs" section.
type Ref struct {
	Kind          string         `json:"kind"`
	Overrides     map[string]any `json:"overrides,omitempty"`
	ApplicationID string         `json:"applicationId,omitempty"`
	SiteID        string         `json:"siteId,omitempty"`
}

func (r Ref) empty() bool { return len(r.Overrides) == 0 && r.ApplicationID == "" && r.SiteID == "" }

// Report says what Declare changed, so the person can be told.
type Report struct {
	// From is the format version the document had (1 when it declared none).
	From int `json:"from"`
	// Stripped counts the observed records removed, by kind.
	Stripped map[string]int `json:"stripped,omitempty"`
	// Refs is how many records left a ref behind (overrides or assignments a person made).
	Refs int `json:"refs"`
	// Dropped counts other observed data removed: open suggestions, observed dependencies, measured links, agents.
	Dropped map[string]int `json:"dropped,omitempty"`
}

// Changed reports whether anything observed was found in the document.
func (r Report) Changed() bool { return len(r.Stripped) > 0 || len(r.Dropped) > 0 }

// Note is the sentence shown to a person when their document was cleaned; empty when nothing was.
func (r Report) Note() string {
	if !r.Changed() {
		return ""
	}
	var parts []string
	total := 0
	for _, k := range sortedKeys(r.Stripped) {
		parts = append(parts, fmt.Sprintf("%d %s", r.Stripped[k], plural(k, r.Stripped[k])))
		total += r.Stripped[k]
	}
	s := "Removed observed data from the workspace"
	if total > 0 {
		s = fmt.Sprintf("Removed %d discovered %s (%s) from the workspace", total, plural("record", total), strings.Join(parts, ", "))
	}
	s += ": observed data is kept apart from what people declare, and agents will report it again."
	if r.Refs > 0 {
		s += fmt.Sprintf(" Your %d %s and assignments on discovered records were kept.", r.Refs, plural("override", r.Refs))
	}
	if n := len(r.Dropped); n > 0 {
		var d []string
		for _, k := range sortedKeys(r.Dropped) {
			d = append(d, fmt.Sprintf("%d %s", r.Dropped[k], k))
		}
		s += " Also dropped: " + strings.Join(d, ", ") + "."
	}
	return s
}

func plural(word string, n int) string {
	if n == 1 {
		return word
	}
	switch word {
	case "service":
		return "services"
	case "cluster", "node", "namespace", "record", "override":
		return word + "s"
	}
	return word
}

func sortedKeys(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

type rec = map[string]json.RawMessage

// observedMeta are the provenance fields that describe an observation, not a decision. They are removed from
// records a person accepted (an application, an external endpoint, a device) so the workspace never carries them.
var observedMeta = []string{"lastSeen", "detectedAt", "revision", "stale", "evidence", "agentId"}

// Declare turns any supported document into the current, declared-only format. It refuses a newer version.
func Declare(data []byte) ([]byte, Report, error) {
	var doc map[string]json.RawMessage
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(&doc); err != nil || doc == nil {
		return nil, Report{}, errors.New("workspace must be a JSON object")
	}
	rep := Report{From: 1}
	if raw, ok := doc["schemaVersion"]; ok {
		var v json.Number
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil, rep, errors.New("workspace must declare a numeric schemaVersion")
		}
		n, err := v.Int64()
		if err != nil || n < 1 {
			return nil, rep, errors.New("workspace must declare a schemaVersion of 1 or more")
		}
		if n > CurrentVersion {
			return nil, rep, ErrNewer{Have: int(n)}
		}
		rep.From = int(n)
	} else {
		return nil, rep, errors.New("workspace must declare a schemaVersion")
	}
	rep.Stripped, rep.Dropped = map[string]int{}, map[string]int{}

	refs := map[string]Ref{}
	if raw, ok := doc["refs"]; ok {
		if err := json.Unmarshal(raw, &refs); err != nil || refs == nil {
			refs = map[string]Ref{}
		}
	}

	for _, k := range observedKinds {
		list := records(doc[k.Key])
		if list == nil {
			continue
		}
		kept := make([]rec, 0, len(list))
		for _, r := range list {
			if str(r["source"]) != "discovered" {
				kept = append(kept, r)
				continue
			}
			rep.Stripped[k.Kind]++
			id := str(r["id"])
			ref := Ref{Kind: k.Kind, ApplicationID: str(r["applicationId"])}
			if k.Kind == "cluster" {
				ref.SiteID = str(r["siteId"])
			}
			var ov map[string]any
			if raw, ok := r["overrides"]; ok && json.Unmarshal(raw, &ov) == nil && len(ov) > 0 {
				ref.Overrides = ov
			}
			if id != "" && !ref.empty() {
				refs[id] = ref
			}
		}
		doc[k.Key] = mustJSON(kept)
	}
	for id, r := range refs {
		if r.empty() || id == "" {
			delete(refs, id)
		}
	}
	rep.Refs = len(refs)

	// What a person accepted stays, without the observation stamps it arrived with.
	for _, key := range []string{"applications", "externalEndpoints", "devices"} {
		if list := records(doc[key]); list != nil {
			for _, r := range list {
				for _, m := range observedMeta {
					delete(r, m)
				}
			}
			doc[key] = mustJSON(list)
		}
	}
	// Open suggestions are worked out from what agents see; a decision a person made is theirs.
	if list := records(doc["suggestions"]); list != nil {
		kept := make([]rec, 0, len(list))
		for _, r := range list {
			if s := str(r["status"]); s == "accepted" || s == "dismissed" {
				kept = append(kept, r)
			} else {
				rep.Dropped["open suggestions"]++
			}
		}
		doc["suggestions"] = mustJSON(kept)
	}
	// Traffic that was seen on the wire is derived on every request.
	if list := records(doc["dependencies"]); list != nil {
		kept := make([]rec, 0, len(list))
		for _, r := range list {
			var sources []string
			_ = json.Unmarshal(r["sources"], &sources)
			var rest []string
			seen := false
			for _, s := range sources {
				if s == "observed" {
					seen = true
				} else {
					rest = append(rest, s)
				}
			}
			if seen && len(rest) == 0 {
				rep.Dropped["observed dependencies"]++
				continue
			}
			if seen {
				r["sources"] = mustJSON(rest)
			}
			for _, k := range []string{"stale", "noise", "crossCluster", "via", "note", "connections", "bytes"} {
				delete(r, k)
			}
			kept = append(kept, r)
		}
		doc["dependencies"] = mustJSON(kept)
	}
	// A link a person declared stays; a measured one is an observation (paths are measured and derived).
	if list := records(doc["siteLinks"]); list != nil {
		kept := make([]rec, 0, len(list))
		for _, r := range list {
			if str(r["source"]) == "measured" {
				rep.Dropped["measured links"]++
				continue
			}
			delete(r, "measuredAt")
			kept = append(kept, r)
		}
		doc["siteLinks"] = mustJSON(kept)
	}
	// Agents belong to the server.
	if list := records(doc["agents"]); len(list) > 0 {
		rep.Dropped["agents"] = len(list)
	}
	doc["agents"] = json.RawMessage("[]")
	// Server audit events come from the server on every read; only what people did here is theirs.
	if list := records(doc["auditLog"]); list != nil {
		kept := make([]rec, 0, len(list))
		for _, r := range list {
			if strings.HasPrefix(str(r["id"]), "ev-") {
				kept = append(kept, r)
			}
		}
		doc["auditLog"] = mustJSON(kept)
	}

	doc["refs"] = mustJSON(refs)
	doc["schemaVersion"] = mustJSON(CurrentVersion)
	if len(rep.Stripped) == 0 {
		rep.Stripped = nil
	}
	if len(rep.Dropped) == 0 {
		rep.Dropped = nil
	}
	out, err := marshalSorted(doc)
	return out, rep, err
}

// Peek returns the format version a document declares, or 0 when it declares none.
func Peek(data []byte) int {
	var probe struct {
		V *int `json:"schemaVersion"`
	}
	if json.Unmarshal(data, &probe) != nil || probe.V == nil {
		return 0
	}
	return *probe.V
}

func records(raw json.RawMessage) []rec {
	if len(raw) == 0 {
		return nil
	}
	var out []rec
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return out
}

func str(raw json.RawMessage) string {
	var s string
	_ = json.Unmarshal(raw, &s)
	return s
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage("null")
	}
	return b
}

// marshalSorted writes the document with its top-level keys in a fixed order, so equal documents give equal bytes.
func marshalSorted(doc map[string]json.RawMessage) ([]byte, error) {
	keys := make([]string, 0, len(doc))
	for k := range doc {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b bytes.Buffer
	b.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		kb, _ := json.Marshal(k)
		b.Write(kb)
		b.WriteByte(':')
		b.Write(doc[k])
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// Declared is what a person authored, read from a workspace document: the records they typed, and what they said
// about records agents observe.
type Declared struct {
	// Refs are the overrides and assignments made on observed records, by record id.
	Refs map[string]Ref
	// Records are the declared entities by kind: cluster, node, namespace, service, device, site, application.
	Records map[string][]map[string]any
}

var declaredKinds = []struct{ Key, Kind string }{
	{"clusters", "cluster"}, {"nodes", "node"}, {"namespaces", "namespace"}, {"services", "service"},
	{"devices", "device"}, {"sites", "site"}, {"applications", "application"},
}

// Parse reads a workspace document (of any supported version) into its declared part.
func Parse(data []byte) (Declared, error) {
	d := Declared{Refs: map[string]Ref{}, Records: map[string][]map[string]any{}}
	if len(data) == 0 {
		return d, nil
	}
	clean, _, err := Declare(data)
	if err != nil {
		return d, err
	}
	var doc struct {
		Refs map[string]Ref `json:"refs"`
	}
	if err := json.Unmarshal(clean, &doc); err == nil && doc.Refs != nil {
		d.Refs = doc.Refs
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(clean, &raw); err != nil {
		return d, err
	}
	for _, k := range declaredKinds {
		var list []map[string]any
		if json.Unmarshal(raw[k.Key], &list) == nil {
			d.Records[k.Kind] = list
		}
	}
	return d, nil
}
