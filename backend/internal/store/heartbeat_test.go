package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func hbOp(id string) Operator {
	return Operator{
		ID: id, OrgID: "o", Name: id, Status: OperatorActive,
		SourceClusterIDs: []string{"cl-a"}, Destination: Destination{Kind: DestinationExternal, Endpoint: "c:4317"},
		CreatedBy: "alex", CreatedAt: time.Now().UTC().Truncate(time.Millisecond),
	}
}

// TestOperatorCreatedUnderSchemaEightReadsBackWithNoHeartbeat is the 8 -> 9 upgrade: a database written by
// the previous release (user_version 8, an operators table without the heartbeat columns) opens, keeps its
// operator, and reads it back as one that never opted in and was never seen.
func TestOperatorCreatedUnderSchemaEightReadsBackWithNoHeartbeat(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "s.db")
	ctx := context.Background()
	st, err := OpenSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	// Put the file back the way version 8 left it: no heartbeat columns, no index, user_version 8, and an
	// operator row written by that release.
	for _, q := range []string{
		`DROP INDEX operators_heartbeat_hash`,
		`ALTER TABLE operators DROP COLUMN heartbeat_hash`,
		`ALTER TABLE operators DROP COLUMN heartbeat_enabled_at`,
		`ALTER TABLE operators DROP COLUMN last_seen_at`,
		`INSERT INTO operators(id, org_id, name, site_id, status, source_cluster_ids, destination, accepted_modalities, receiver_auth_token_hash, created_by, created_at, reason)
		 VALUES('op-v8','o','from v8','','active','["cl-a"]','{"Kind":"external","Endpoint":"c:4317"}','[]',x'0102','alex',1700000000000,'')`,
		`PRAGMA user_version = 8`,
	} {
		if _, err := st.db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	up, err := OpenSQLite(dbPath)
	if err != nil {
		t.Fatalf("opening a version 8 database: %v", err)
	}
	defer up.Close()
	var v int
	if err := up.db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil || v != 9 {
		t.Fatalf("user_version = %d (%v), want 9", v, err)
	}
	got, err := up.GetOperator(ctx, "op-v8")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "from v8" || got.HeartbeatHash != nil || got.HeartbeatEnabledAt != nil || got.LastSeenAt != nil {
		t.Fatalf("a v8 operator reads back with heartbeat state: %+v", got)
	}
	if _, err := up.GetOperatorByHeartbeatHash(ctx, []byte("anything")); err != ErrNotFound {
		t.Fatalf("lookup of an unknown hash = %v, want ErrNotFound", err)
	}
	// And the upgraded operator can opt in afterwards.
	if err := up.SetOperatorHeartbeat(ctx, "op-v8", []byte("h1"), time.Now()); err != nil {
		t.Fatal(err)
	}
	if got, _ = up.GetOperator(ctx, "op-v8"); string(got.HeartbeatHash) != "h1" || got.HeartbeatEnabledAt == nil {
		t.Fatalf("%+v", got)
	}
	// The migration is idempotent: reopening a v9 database changes nothing.
	up.Close()
	again, err := OpenSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	if got, _ = again.GetOperator(ctx, "op-v8"); string(got.HeartbeatHash) != "h1" {
		t.Fatalf("reopen lost the heartbeat hash: %+v", got)
	}
}

func TestOperatorHeartbeatStore(t *testing.T) {
	ctx := context.Background()
	st, err := OpenSQLite(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Now().UTC().Truncate(time.Millisecond)

	// Created with a heartbeat already on.
	a := hbOp("op-a")
	a.HeartbeatHash, a.HeartbeatEnabledAt = []byte("hash-a"), &now
	if err := st.CreateOperator(ctx, a, []byte("recv-a")); err != nil {
		t.Fatal(err)
	}
	// Created without one.
	if err := st.CreateOperator(ctx, hbOp("op-b"), []byte("recv-b")); err != nil {
		t.Fatal(err)
	}

	got, err := st.GetOperatorByHeartbeatHash(ctx, []byte("hash-a"))
	if err != nil || got.ID != "op-a" || got.HeartbeatEnabledAt == nil || !got.HeartbeatEnabledAt.Equal(now) || got.LastSeenAt != nil {
		t.Fatalf("by hash: %v %+v", err, got)
	}
	if _, err := st.GetOperatorByHeartbeatHash(ctx, []byte("hash-zzz")); err != ErrNotFound {
		t.Fatalf("unknown hash: %v", err)
	}
	if _, err := st.GetOperatorByHeartbeatHash(ctx, nil); err != ErrNotFound {
		t.Fatalf("empty hash must never match an operator without one: %v", err)
	}
	if b, _ := st.GetOperator(ctx, "op-b"); b.HeartbeatHash != nil {
		t.Fatalf("op-b has a heartbeat hash: %+v", b)
	}
	// The receiver token hash is not a heartbeat hash.
	if _, err := st.GetOperatorByHeartbeatHash(ctx, []byte("recv-a")); err != ErrNotFound {
		t.Fatalf("the receiver token hash opened the heartbeat lookup: %v", err)
	}

	seen := now.Add(time.Minute)
	if err := st.TouchOperatorSeen(ctx, "op-a", seen); err != nil {
		t.Fatal(err)
	}
	if got, _ = st.GetOperator(ctx, "op-a"); got.LastSeenAt == nil || !got.LastSeenAt.Equal(seen) {
		t.Fatalf("last seen: %+v", got)
	}

	// Rotation replaces the hash (the old one stops matching) and keeps last seen.
	if err := st.SetOperatorHeartbeat(ctx, "op-a", []byte("hash-a2"), now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetOperatorByHeartbeatHash(ctx, []byte("hash-a")); err != ErrNotFound {
		t.Fatalf("the replaced hash still matches: %v", err)
	}
	if got, err = st.GetOperatorByHeartbeatHash(ctx, []byte("hash-a2")); err != nil || got.LastSeenAt == nil {
		t.Fatalf("after rotation: %v %+v", err, got)
	}

	// Revoked: cannot be given one; unknown: not found.
	if err := st.RevokeOperator(ctx, "op-b", "x", now); err != nil {
		t.Fatal(err)
	}
	if err := st.SetOperatorHeartbeat(ctx, "op-b", []byte("h"), now); err != ErrBadState {
		t.Fatalf("revoked operator: %v", err)
	}
	if err := st.SetOperatorHeartbeat(ctx, "op-nope", []byte("h"), now); err != ErrNotFound {
		t.Fatalf("unknown operator: %v", err)
	}
	if err := st.DeleteOperator(ctx, "op-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetOperatorByHeartbeatHash(ctx, []byte("hash-a2")); err != ErrNotFound {
		t.Fatalf("a deleted operator's hash still matches: %v", err)
	}
}
