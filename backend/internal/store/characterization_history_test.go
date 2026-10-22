package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// Characterization tests for history snapshots, events, settings, mail configuration and database backup.
// They pin today's behaviour, including the parts a reader might not expect.

func TestHistorySnapshotsAreWholeSecondAndPerOrganisation(t *testing.T) {
	st, ctx, now := openAccounts(t)

	// Sub-second parts are dropped, so every listed time can be used to fetch its own snapshot.
	if err := st.AddHistory(ctx, "o1", now.Add(250*time.Millisecond), []byte("first")); err != nil {
		t.Fatal(err)
	}
	// The same second again replaces the data.
	if err := st.AddHistory(ctx, "o1", now.Add(900*time.Millisecond), []byte("replaced")); err != nil {
		t.Fatal(err)
	}
	if err := st.AddHistory(ctx, "o1", now.Add(time.Minute), []byte("second")); err != nil {
		t.Fatal(err)
	}
	if err := st.AddHistory(ctx, "o2", now, []byte("another org")); err != nil {
		t.Fatal(err)
	}

	pts, err := st.ListHistory(ctx, "o1", time.Time{}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(pts) != 2 || !pts[0].At.Equal(now.Truncate(time.Second)) || pts[0].Bytes != len("replaced") || pts[1].Bytes != len("second") {
		t.Fatalf("ListHistory = %+v", pts)
	}
	// since and until are inclusive.
	pts, _ = st.ListHistory(ctx, "o1", now.Add(time.Minute), time.Time{})
	if len(pts) != 1 || pts[0].Bytes != len("second") {
		t.Fatalf("since: %+v", pts)
	}
	pts, _ = st.ListHistory(ctx, "o1", time.Time{}, now.Truncate(time.Second))
	if len(pts) != 1 || pts[0].Bytes != len("replaced") {
		t.Fatalf("until: %+v", pts)
	}
	if pts, err := st.ListHistory(ctx, "nobody", time.Time{}, time.Time{}); err != nil || pts == nil || len(pts) != 0 {
		t.Fatalf("an organisation with no history lists as an empty, non-nil slice: %#v %v", pts, err)
	}

	// GetHistory returns the newest snapshot at or before the asked time.
	p, data, err := st.GetHistory(ctx, "o1", now.Add(30*time.Second))
	if err != nil || string(data) != "replaced" || !p.At.Equal(now.Truncate(time.Second)) {
		t.Fatalf("GetHistory in between = %+v %q %v", p, data, err)
	}
	if _, data, _ := st.GetHistory(ctx, "o1", now.Add(time.Hour)); string(data) != "second" {
		t.Fatalf("GetHistory after the last = %q", data)
	}
	if _, _, err := st.GetHistory(ctx, "o1", now.Add(-time.Hour)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("before the first snapshot: %v, want ErrNotFound", err)
	}

	if err := st.DeleteHistory(ctx, "o1", nil); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteHistory(ctx, "o1", []time.Time{now.Truncate(time.Second)}); err != nil {
		t.Fatal(err)
	}
	pts, _ = st.ListHistory(ctx, "o1", time.Time{}, time.Time{})
	if len(pts) != 1 || pts[0].Bytes != len("second") {
		t.Fatalf("after delete: %+v", pts)
	}
	if other, _ := st.ListHistory(ctx, "o2", time.Time{}, time.Time{}); len(other) != 1 {
		t.Fatalf("deleting one organisation's snapshot touched another's: %+v", other)
	}
}

func ev(at time.Time, kind, target, cluster string) Event {
	return Event{At: at, Kind: kind, TargetKind: TargetService, TargetID: target, Name: target, ClusterID: cluster, ClusterName: cluster + "-name", Detail: "d", Cause: "c"}
}

func TestEventsAreStoredFilteredAndOrderedNewestFirst(t *testing.T) {
	st, ctx, now := openAccounts(t)

	if err := st.AddEvents(ctx, "o1", nil); err != nil {
		t.Fatal(err)
	}
	batch := []Event{
		ev(now, "service-scaled", "cart", "cl-a"),
		ev(now.Add(time.Minute), "node-status", "n1", "cl-a"),
		ev(now.Add(2*time.Minute), "service-scaled", "pay", "cl-b"),
		{At: now.Add(3 * time.Minute), Kind: "drift", Severity: "warning", TargetID: "cart", ClusterID: "cl-a"},
	}
	if err := st.AddEvents(ctx, "o1", batch); err != nil {
		t.Fatal(err)
	}
	if err := st.AddEvents(ctx, "o2", []Event{ev(now, "service-scaled", "cart", "cl-a")}); err != nil {
		t.Fatal(err)
	}

	all, err := st.ListEvents(ctx, "o1", EventQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 4 || all[0].Kind != "drift" || all[3].TargetID != "cart" {
		t.Fatalf("newest first, own organisation only: %+v", all)
	}
	// An empty severity is stored as info; an explicit one is kept.
	if all[0].Severity != "warning" || all[1].Severity != "info" {
		t.Fatalf("severities = %q %q", all[0].Severity, all[1].Severity)
	}
	if all[1].ID == 0 || all[1].Name != "pay" || all[1].ClusterName != "cl-b-name" || all[1].Cause != "c" {
		t.Fatalf("event did not round-trip: %+v", all[1])
	}

	kinds := func(q EventQuery) []string {
		evs, err := st.ListEvents(ctx, "o1", q)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, e := range evs {
			out = append(out, e.Kind+"/"+e.TargetID)
		}
		return out
	}
	for name, c := range map[string]struct {
		q    EventQuery
		want []string
	}{
		"kind":      {EventQuery{Kind: "service-scaled"}, []string{"service-scaled/pay", "service-scaled/cart"}},
		"cluster":   {EventQuery{ClusterID: "cl-b"}, []string{"service-scaled/pay"}},
		"target":    {EventQuery{TargetID: "cart"}, []string{"drift/cart", "service-scaled/cart"}},
		"targets":   {EventQuery{TargetIDs: []string{"n1", "pay"}}, []string{"service-scaled/pay", "node-status/n1"}},
		"since":     {EventQuery{Since: now.Add(2 * time.Minute)}, []string{"drift/cart", "service-scaled/pay"}},
		"until":     {EventQuery{Until: now.Add(time.Minute)}, []string{"node-status/n1", "service-scaled/cart"}},
		"limit":     {EventQuery{Limit: 1}, []string{"drift/cart"}},
		"no match":  {EventQuery{Kind: "nothing"}, nil},
		"combined":  {EventQuery{Kind: "service-scaled", ClusterID: "cl-a"}, []string{"service-scaled/cart"}},
		"time rows": {EventQuery{Since: now.Add(time.Minute), Until: now.Add(2 * time.Minute)}, []string{"service-scaled/pay", "node-status/n1"}},
	} {
		if got := kinds(c.q); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %v, want %v", name, got, c.want)
		}
	}
}

func TestEventLimitsAreCappedAtTwoThousand(t *testing.T) {
	st, ctx, now := openAccounts(t)
	var batch []Event
	for i := 0; i < 2100; i++ {
		batch = append(batch, ev(now.Add(time.Duration(i)*time.Second), "k", "t", "c"))
	}
	if err := st.AddEvents(ctx, "o1", batch); err != nil {
		t.Fatal(err)
	}
	if evs, _ := st.ListEvents(ctx, "o1", EventQuery{}); len(evs) != 200 {
		t.Fatalf("default limit = %d, want 200", len(evs))
	}
	if evs, _ := st.ListEvents(ctx, "o1", EventQuery{Limit: 99999}); len(evs) != 2000 {
		t.Fatalf("the cap = %d, want 2000", len(evs))
	}
}

func TestPruneAndDeleteEvents(t *testing.T) {
	st, ctx, now := openAccounts(t)
	var batch []Event
	for i := 0; i < 6; i++ {
		batch = append(batch, ev(now.Add(time.Duration(i)*time.Minute), "k", "t", "c"))
	}
	if err := st.AddEvents(ctx, "o1", batch); err != nil {
		t.Fatal(err)
	}
	if err := st.AddEvents(ctx, "o2", []Event{ev(now, "k", "t", "c")}); err != nil {
		t.Fatal(err)
	}

	// Pruning by age drops what is strictly older than the cutoff.
	if err := st.PruneEvents(ctx, "o1", now.Add(2*time.Minute), 0); err != nil {
		t.Fatal(err)
	}
	if evs, _ := st.ListEvents(ctx, "o1", EventQuery{}); len(evs) != 4 {
		t.Fatalf("after pruning by age: %d events, want 4", len(evs))
	}
	// keepNewest then caps the count, keeping the newest.
	if err := st.PruneEvents(ctx, "o1", time.Time{}, 2); err != nil {
		t.Fatal(err)
	}
	evs, _ := st.ListEvents(ctx, "o1", EventQuery{})
	if len(evs) != 2 || !evs[0].At.Equal(now.Add(5*time.Minute)) || !evs[1].At.Equal(now.Add(4*time.Minute)) {
		t.Fatalf("after capping the count: %+v", evs)
	}
	if other, _ := st.ListEvents(ctx, "o2", EventQuery{}); len(other) != 1 {
		t.Fatalf("pruning one organisation touched another's events: %+v", other)
	}

	if err := st.DeleteEvents(ctx, "o1", nil); err != nil {
		t.Fatal(err)
	}
	// An id from another organisation deletes nothing.
	other, _ := st.ListEvents(ctx, "o2", EventQuery{})
	if err := st.DeleteEvents(ctx, "o1", []int64{other[0].ID}); err != nil {
		t.Fatal(err)
	}
	if again, _ := st.ListEvents(ctx, "o2", EventQuery{}); len(again) != 1 {
		t.Fatal("DeleteEvents removed another organisation's event")
	}
	if err := st.DeleteEvents(ctx, "o1", []int64{evs[0].ID}); err != nil {
		t.Fatal(err)
	}
	if left, _ := st.ListEvents(ctx, "o1", EventQuery{}); len(left) != 1 || left[0].ID != evs[1].ID {
		t.Fatalf("after deleting one: %+v", left)
	}
}

func TestSettingsAndMailConfigAreOpaqueDocuments(t *testing.T) {
	st, ctx, now := openAccounts(t)

	if d, err := st.GetSettings(ctx, "o1"); err != nil || d != nil {
		t.Fatalf("settings never saved: %q %v, want nil nil", d, err)
	}
	if err := st.PutSettings(ctx, "o1", []byte(`{"a":1}`), now); err != nil {
		t.Fatal(err)
	}
	if err := st.PutSettings(ctx, "o1", []byte(`{"a":2}`), now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := st.PutSettings(ctx, "o2", []byte(`{"b":1}`), now); err != nil {
		t.Fatal(err)
	}
	if d, _ := st.GetSettings(ctx, "o1"); string(d) != `{"a":2}` {
		t.Fatalf("settings = %q, want the latest write", d)
	}
	if d, _ := st.GetSettings(ctx, "o2"); string(d) != `{"b":1}` {
		t.Fatalf("another organisation's settings = %q", d)
	}

	if d, err := st.GetMailConfig(ctx); err != nil || d != nil {
		t.Fatalf("mail never configured: %q %v, want nil nil", d, err)
	}
	if err := st.PutMailConfig(ctx, []byte("one"), now); err != nil {
		t.Fatal(err)
	}
	if err := st.PutMailConfig(ctx, []byte("two"), now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if d, _ := st.GetMailConfig(ctx); string(d) != "two" {
		t.Fatalf("mail config = %q, want the latest write", d)
	}
}

func TestBackupCopiesTheDatabaseAndRefusesToOverwrite(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "live.db")
	st, err := OpenSQLite(src)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := time.UnixMilli(1_700_000_000_000).UTC()
	if err := st.CreateUser(ctx, User{ID: "u1", Username: "quinn", PasswordHash: "h", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	dst := filepath.Join(dir, "copy.db")
	v, err := BackupDatabase(ctx, src, dst)
	if err != nil {
		t.Fatal(err)
	}
	if v != SchemaVersion {
		t.Fatalf("backup schema version = %d, want %d", v, SchemaVersion)
	}
	if fi, err := os.Stat(dst); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("backup file: %v %v, want mode 0600", fi, err)
	}
	// The copy is a working database holding what the live one held at that moment.
	cp, err := OpenSQLite(dst)
	if err != nil {
		t.Fatal(err)
	}
	defer cp.Close()
	if u, err := cp.GetUserByName(ctx, "quinn"); err != nil || u.ID != "u1" {
		t.Fatalf("the copy lacks the user: %+v %v", u, err)
	}

	if _, err := BackupDatabase(ctx, src, dst); err == nil {
		t.Fatal("backing up over an existing file must be refused")
	}
	if _, err := BackupDatabase(ctx, filepath.Join(dir, "missing.db"), filepath.Join(dir, "x.db")); err == nil {
		t.Fatal("backing up a missing database must fail")
	}
	if _, err := os.Stat(filepath.Join(dir, "x.db")); !os.IsNotExist(err) {
		t.Fatal("a failed backup must not leave a file behind")
	}
}

func TestCheckDatabaseReportsDamageAndNewerSchemas(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	good := filepath.Join(dir, "good.db")
	st, err := OpenSQLite(good)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	if v, err := CheckDatabase(ctx, good); err != nil || v != SchemaVersion {
		t.Fatalf("a healthy database: %d %v", v, err)
	}

	notDB := filepath.Join(dir, "garbage.db")
	if err := os.WriteFile(notDB, []byte("this is not a sqlite database at all, just some text padding it out to look like a file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := CheckDatabase(ctx, notDB); err == nil {
		t.Fatal("a file that is not a database must be refused")
	}

	// A database written by a newer server is reported, not opened for change.
	newer := filepath.Join(dir, "newer.db")
	raw, err := sql.Open("sqlite", "file:"+newer)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`CREATE TABLE t(x)`); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`PRAGMA user_version = 9999`); err != nil {
		t.Fatal(err)
	}
	raw.Close()
	v, err := CheckDatabase(ctx, newer)
	if v != 9999 || !IsSchemaNewer(err) {
		t.Fatalf("a newer schema: version %d err %v, want 9999 and a SchemaNewerError", v, err)
	}
	var sn SchemaNewerError
	if !errors.As(err, &sn) || sn.Have != 9999 || sn.Know != SchemaVersion {
		t.Fatalf("SchemaNewerError = %+v", sn)
	}
	if _, err := OpenSQLite(newer); !IsSchemaNewer(err) {
		t.Fatalf("OpenSQLite on a newer database: %v, want ErrSchemaNewer", err)
	}
	if IsSchemaNewer(errors.New("unrelated")) || IsSchemaNewer(nil) {
		t.Fatal("IsSchemaNewer must be false for other errors")
	}
}
