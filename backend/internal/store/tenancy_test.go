package store

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestMigrationTurnsALegacySingleOrganisationDatabaseIntoATenant(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "c.db")
	s, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	// Rebuild what an older server left behind: users carrying an org id and a role, no organisations
	// or memberships, and the version marker not yet set.
	now := time.Now().Unix()
	for _, q := range []string{
		`INSERT INTO users(id, org_id, username, role, password_hash, created_at) VALUES ('u-1','default','first','admin','x',` + itoa(now) + `)`,
		`INSERT INTO users(id, org_id, username, role, password_hash, created_at) VALUES ('u-2','default','second','admin','x',` + itoa(now+10) + `)`,
		`INSERT INTO users(id, org_id, username, role, password_hash, created_at) VALUES ('u-3','default','third','viewer','x',` + itoa(now+20) + `)`,
		`INSERT INTO workspace(org_id, rev, data, updated_at, updated_by) VALUES ('default', 4, x'7b7d', ` + itoa(now) + `, 'first')`,
		`PRAGMA user_version = 0`,
	} {
		if _, err := s.db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	s.Close()

	s, err = OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if o, err := s.GetOrg(ctx, "default"); err != nil || o.Name != "default" {
		t.Fatalf("org = %+v %v", o, err)
	}
	for id, want := range map[string]string{"u-1": "owner", "u-2": "admin", "u-3": "viewer"} {
		if m, err := s.GetMembership(ctx, "default", id); err != nil || m.Role != want {
			t.Errorf("%s: %+v %v, want %s", id, m, err, want)
		}
	}
	if n, _ := s.CountOwners(ctx, "default"); n != 1 {
		t.Errorf("owners = %d", n)
	}
	if ws, _ := s.GetWorkspace(ctx, "default"); ws.Rev != 4 {
		t.Errorf("workspace lost: %+v", ws)
	}
	// Running it again changes nothing (a restart).
	s.Close()
	s, err = OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := s.CountOwners(ctx, "default"); n != 1 {
		t.Errorf("owners after a second start = %d", n)
	}
	if u, err := s.GetUserByName(ctx, "SECOND"); err != nil || u.ID != "u-2" {
		t.Errorf("global, case-insensitive lookup: %+v %v", u, err)
	}
}

func TestInviteIsAtomicAndOneTime(t *testing.T) {
	ctx := context.Background()
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "c.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now()
	mk := func(id string) {
		if err := s.CreateUser(ctx, User{ID: id, Username: id, PasswordHash: "x", CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	mk("u-a")
	mk("u-b")
	mk("u-c")
	if err := s.CreateOrg(ctx, Org{ID: "o1", Name: "One", CreatedAt: now, CreatedBy: "u-a"}, "u-a"); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateOrg(ctx, Org{ID: "o1", Name: "Again", CreatedAt: now}, "u-b"); !errors.Is(err, ErrExists) {
		t.Fatalf("duplicate org id: %v", err)
	}
	h := []byte("hash-1")
	if err := s.CreateInvite(ctx, Invite{ID: "i1", OrgID: "o1", Role: "editor", CreatedBy: "u-a", CreatedAt: now, ExpiresAt: now.Add(time.Hour)}, h); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PeekInvite(ctx, h, now.Add(2*time.Hour)); !errors.Is(err, ErrTokenInvalid) {
		t.Errorf("expired invite peeked: %v", err)
	}
	if _, err := s.UseInvite(ctx, h, "u-b", now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UseInvite(ctx, h, "u-c", now); !errors.Is(err, ErrTokenInvalid) {
		t.Errorf("used twice: %v", err)
	}
	if _, err := s.GetMembership(ctx, "o1", "u-c"); !errors.Is(err, ErrNotFound) {
		t.Errorf("a refused invite still added a member: %v", err)
	}
	// Deleting the organisation removes members and invitations but not the accounts.
	if err := s.DeleteOrg(ctx, "o1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetMembership(ctx, "o1", "u-b"); !errors.Is(err, ErrNotFound) {
		t.Errorf("membership survived: %v", err)
	}
	if _, err := s.GetUser(ctx, "u-b"); err != nil {
		t.Errorf("account lost with the organisation: %v", err)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// A member listing is shown to administrators and passed around by callers; it must not even load the
// password hashes.
func TestListMembersDoesNotLoadPasswordHashes(t *testing.T) {
	ctx := context.Background()
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now()
	for _, id := range []string{"u-a", "u-b"} {
		if err := s.CreateUser(ctx, User{ID: id, Username: id, PasswordHash: "$argon2id$secret-" + id, CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.CreateOrg(ctx, Org{ID: "o1", Name: "One", CreatedAt: now, CreatedBy: "u-a"}, "u-a"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddMember(ctx, Membership{OrgID: "o1", UserID: "u-b", Role: "viewer", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	ms, err := s.ListMembers(ctx, "o1")
	if err != nil || len(ms) != 2 {
		t.Fatalf("%v %v", ms, err)
	}
	for _, m := range ms {
		if m.PasswordHash != "" || strings.Contains(fmt.Sprintf("%+v", m), "secret") {
			t.Errorf("%s: the password hash was loaded: %+v", m.Username, m)
		}
		if m.Username == "" || m.Role == "" || m.ID == "" {
			t.Errorf("the rest of the row must still be there: %+v", m)
		}
	}
	// The account itself still has it, for sign-in.
	if u, err := s.GetUserByName(ctx, "u-a"); err != nil || u.PasswordHash == "" {
		t.Errorf("sign-in needs the hash: %+v %v", u, err)
	}
}
