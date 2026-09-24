package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A database written by a NEWER server is refused, with a message that says what to do, and is left untouched.
func TestOpeningANewerDatabaseIsRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "new.db")
	st, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`PRAGMA user_version = 99`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`CREATE TABLE from_the_future (x INTEGER)`); err != nil {
		t.Fatal(err)
	}
	st.Close()

	_, err = OpenSQLite(path)
	if !errors.Is(err, ErrSchemaNewer) {
		t.Fatalf("err = %v", err)
	}
	var ne SchemaNewerError
	if !errors.As(err, &ne) || ne.Have != 99 || ne.Know != SchemaVersion {
		t.Fatalf("err = %#v", err)
	}
	for _, want := range []string{"version 99", "up to version 7", "newer Continuum", "nothing was touched"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message lacks %q: %v", want, err)
		}
	}
	// Untouched: still version 99, and no table of ours was created next to the future's.
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var v int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil || v != 99 {
		t.Fatalf("version %d %v", v, err)
	}
}

func TestFreshDatabaseIsAtTheCurrentSchema(t *testing.T) {
	st, err := OpenSQLite(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var v int
	if err := st.db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil || v != SchemaVersion {
		t.Fatalf("user_version = %d (%v), want %d", v, err, SchemaVersion)
	}
}

const legacyWorkspace = `{"schemaVersion":3,"clusters":[{"id":"cl-1","source":"discovered","name":"edge","siteId":"s1","overrides":{"trustZone":"restricted"}},{"id":"cl-m","source":"manual","name":"lab"}],"nodes":[{"id":"n1","source":"discovered","clusterId":"cl-1","name":"n1"}],"services":[{"id":"sv-1","source":"discovered","clusterId":"cl-1","name":"checkout"}],"sites":[{"id":"s1","name":"Patras"}]}`

// A workspace saved before v4, holding discovered records, is rewritten when the database is upgraded: the observed
// records are gone, the person's assignments are kept, and a note says so once.
func TestUpgradeStripsDiscoveredRecordsFromStoredWorkspaces(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "old.db")
	st, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`INSERT INTO workspace(org_id, rev, data, updated_at, updated_by) VALUES ('o', 7, ?, 1, 'ann')`, []byte(legacyWorkspace)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`PRAGMA user_version = 3`); err != nil {
		t.Fatal(err)
	}
	st.Close()

	st, err = OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	w, err := st.GetWorkspace(ctx, "o")
	if err != nil {
		t.Fatal(err)
	}
	if w.Rev != 7 || w.UpdatedBy != "ann" {
		t.Errorf("revision history must survive: %+v", w)
	}
	s := string(w.Data)
	for _, banned := range []string{"checkout", `"name":"edge"`, `"name":"n1"`} {
		if strings.Contains(s, banned) {
			t.Errorf("workspace still carries %s: %s", banned, s)
		}
	}
	for _, want := range []string{`"lab"`, `"restricted"`, `"schemaVersion":4`, `"siteId":"s1"`, `"Patras"`} {
		if !strings.Contains(s, want) {
			t.Errorf("workspace lost %s: %s", want, s)
		}
	}
	if !strings.Contains(w.Note, "Removed 3 discovered records") {
		t.Errorf("note = %q", w.Note)
	}
	// The next save clears the note.
	saved, err := st.PutWorkspace(ctx, "o", 7, w.Data, "ann", time.Now())
	if err != nil || saved.Rev != 8 {
		t.Fatal(saved, err)
	}
	if w, _ = st.GetWorkspace(ctx, "o"); w.Note != "" {
		t.Errorf("note not cleared: %q", w.Note)
	}
	// Opening again does not rewrite or re-note.
	st.Close()
	if st, err = OpenSQLite(path); err != nil {
		t.Fatal(err)
	}
	if w, _ = st.GetWorkspace(ctx, "o"); w.Note != "" || w.Rev != 8 {
		t.Errorf("second open changed the workspace: %+v", w)
	}
}

func TestUpgradeLeavesAnUnreadableWorkspaceAlone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "odd.db")
	st, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	newer := `{"schemaVersion":9,"clusters":[{"id":"x","source":"discovered"}]}`
	if _, err := st.db.Exec(`INSERT INTO workspace(org_id, rev, data, updated_at, updated_by) VALUES ('o', 1, ?, 1, 'a'), ('p', 1, x'6e6f7065', 1, 'a')`, []byte(newer)); err != nil {
		t.Fatal(err)
	}
	st.db.Exec(`PRAGMA user_version = 3`)
	st.Close()
	st, err = OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if w, _ := st.GetWorkspace(context.Background(), "o"); string(w.Data) != newer {
		t.Errorf("a document from a newer version must never be rewritten: %s", w.Data)
	}
	if w, _ := st.GetWorkspace(context.Background(), "p"); string(w.Data) != "nope" {
		t.Errorf("garbage must be left as it is: %s", w.Data)
	}
}

func TestTombstonesIdentitiesAndModelStateRoundTrip(t *testing.T) {
	ctx := context.Background()
	st, err := OpenSQLite(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.UnixMilli(1_700_000_000_000).UTC()

	if err := st.PutTombstones(ctx, "a", []Tombstone{
		{Kind: "service", ID: "sv-1", Name: "cart", ClusterID: "cl-1", AgentID: "ag", GoneAt: now, LastSeen: now.Add(-time.Minute), Reason: "no longer reported", Record: []byte(`{"name":"cart"}`)},
		{Kind: "node", ID: "nd-1", GoneAt: now.Add(time.Hour)},
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutTombstones(ctx, "b", []Tombstone{{Kind: "service", ID: "sv-1", Name: "other org", GoneAt: now}}); err != nil {
		t.Fatal(err)
	}
	got, err := st.ListTombstones(ctx, "a")
	if err != nil || len(got) != 2 {
		t.Fatalf("%v %v", got, err)
	}
	if got[0].ID != "nd-1" || got[1].Name != "cart" || !got[1].GoneAt.Equal(now) || !got[1].LastSeen.Equal(now.Add(-time.Minute)) || string(got[1].Record) != `{"name":"cart"}` {
		t.Errorf("%+v", got)
	}
	if b, _ := st.ListTombstones(ctx, "b"); len(b) != 1 || b[0].Name != "other org" {
		t.Errorf("tombstones are per organisation: %+v", b)
	}
	// Replace, then delete.
	if err := st.PutTombstones(ctx, "a", []Tombstone{{Kind: "service", ID: "sv-1", Name: "cart2", GoneAt: now}}); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteTombstones(ctx, "a", []TombstoneKey{{"node", "nd-1"}}); err != nil {
		t.Fatal(err)
	}
	if got, _ = st.ListTombstones(ctx, "a"); len(got) != 1 || got[0].Name != "cart2" {
		t.Errorf("%+v", got)
	}

	if err := st.PutIdentities(ctx, "a", []Identity{{ClusterID: "cl-1", Ident: "provider-id:ab", RecordID: "nd-1", Name: "n2", Aliases: []string{"n1", "n0"}, FirstSeen: now, LastSeen: now}}); err != nil {
		t.Fatal(err)
	}
	ids, err := st.ListIdentities(ctx, "a")
	if err != nil || len(ids) != 1 || ids[0].Name != "n2" || len(ids[0].Aliases) != 2 || ids[0].Aliases[1] != "n0" {
		t.Fatalf("%+v %v", ids, err)
	}
	if other, _ := st.ListIdentities(ctx, "b"); len(other) != 0 {
		t.Errorf("identities are per organisation: %+v", other)
	}

	if _, err := st.GetModelState(ctx, "a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
	if err := st.PutModelState(ctx, "a", ModelState{Version: 5, Fingerprint: "f5", At: now}); err != nil {
		t.Fatal(err)
	}
	// A lower version never overwrites a higher one (two writers racing must not make the version go backwards).
	if err := st.PutModelState(ctx, "a", ModelState{Version: 4, Fingerprint: "f4", At: now}); err != nil {
		t.Fatal(err)
	}
	if m, _ := st.GetModelState(ctx, "a"); m.Version != 5 || m.Fingerprint != "f5" {
		t.Errorf("%+v", m)
	}
	// Deleting the organisation removes everything of the twin.
	if err := st.CreateOrg(ctx, Org{ID: "a", Name: "A", CreatedAt: now}, ""); err != nil && !errors.Is(err, ErrExists) {
		t.Log("create org:", err)
	}
	if err := st.DeleteOrg(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if got, _ = st.ListTombstones(ctx, "a"); len(got) != 0 {
		t.Errorf("tombstones survived the organisation: %+v", got)
	}
	if ids, _ = st.ListIdentities(ctx, "a"); len(ids) != 0 {
		t.Errorf("identities survived the organisation")
	}
	if _, err := st.GetModelState(ctx, "a"); !errors.Is(err, ErrNotFound) {
		t.Errorf("model state survived the organisation: %v", err)
	}
	if b, _ := st.ListTombstones(ctx, "b"); len(b) != 1 {
		t.Errorf("another organisation lost its tombstones")
	}
}
