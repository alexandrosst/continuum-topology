package store

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// Characterization tests: they pin what the account storage does today (users, second factors, passkeys,
// sessions, personal API tokens) so a later refactor of how the store is organised cannot change it unnoticed.
// Where behaviour is surprising it is pinned as it is and said so; changing it is a decision, not a side effect.

func openAccounts(t *testing.T) (*SQLite, context.Context, time.Time) {
	t.Helper()
	st, err := OpenSQLite(filepath.Join(t.TempDir(), "acc.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st, context.Background(), time.UnixMilli(1_700_000_000_000).UTC()
}

func mustUser(t *testing.T, st *SQLite, id, name string, now time.Time) User {
	t.Helper()
	u := User{ID: id, Username: name, PasswordHash: "hash-" + name, MustChange: true, CreatedAt: now}
	if err := st.CreateUser(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	return u
}

func TestUsersCreateGetCountPasswordDisableLoginDelete(t *testing.T) {
	st, ctx, now := openAccounts(t)
	u := mustUser(t, st, "u1", "quinn", now)

	if err := st.CreateUser(ctx, User{ID: "u2", Username: "quinn", CreatedAt: now}); !errors.Is(err, ErrExists) {
		t.Fatalf("a second account with the same username: %v, want ErrExists", err)
	}
	if err := st.CreateUser(ctx, User{ID: "u1", Username: "other", CreatedAt: now}); !errors.Is(err, ErrExists) {
		t.Fatalf("a second account with the same id: %v, want ErrExists", err)
	}

	got, err := st.GetUser(ctx, "u1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Username != u.Username || got.PasswordHash != u.PasswordHash || !got.MustChange || !got.CreatedAt.Equal(now) {
		t.Fatalf("GetUser = %+v", got)
	}
	if got.DisabledAt != nil || got.LastLogin != nil || got.TOTPEnabledAt != nil || got.EmailVerifiedAt != nil || len(got.WebAuthnCredentials) != 0 {
		t.Fatalf("a new account has no optional state: %+v", got)
	}
	if byName, err := st.GetUserByName(ctx, "quinn"); err != nil || byName.ID != "u1" {
		t.Fatalf("GetUserByName = %+v, %v", byName, err)
	}
	if _, err := st.GetUser(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown id: %v, want ErrNotFound", err)
	}
	if _, err := st.GetUserByName(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown name: %v, want ErrNotFound", err)
	}
	if n, err := st.CountUsers(ctx); err != nil || n != 1 {
		t.Fatalf("CountUsers = %d, %v", n, err)
	}

	if err := st.SetPassword(ctx, "u1", "new-hash", false); err != nil {
		t.Fatal(err)
	}
	got, _ = st.GetUser(ctx, "u1")
	if got.PasswordHash != "new-hash" || got.MustChange {
		t.Fatalf("after SetPassword: %+v", got)
	}

	off := now.Add(time.Hour)
	if err := st.SetDisabled(ctx, "u1", &off); err != nil {
		t.Fatal(err)
	}
	got, _ = st.GetUser(ctx, "u1")
	if got.DisabledAt == nil || !got.DisabledAt.Equal(off) {
		t.Fatalf("DisabledAt = %v, want %v", got.DisabledAt, off)
	}
	if err := st.SetDisabled(ctx, "u1", nil); err != nil {
		t.Fatal(err)
	}
	got, _ = st.GetUser(ctx, "u1")
	if got.DisabledAt != nil {
		t.Fatalf("re-enabling should clear DisabledAt, got %v", got.DisabledAt)
	}

	when := now.Add(2 * time.Hour)
	if err := st.MarkLogin(ctx, "u1", when); err != nil {
		t.Fatal(err)
	}
	got, _ = st.GetUser(ctx, "u1")
	if got.LastLogin == nil || !got.LastLogin.Equal(when) {
		t.Fatalf("LastLogin = %v, want %v", got.LastLogin, when)
	}

	if err := st.DeleteUser(ctx, "u1"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetUser(ctx, "u1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a deleted account: %v, want ErrNotFound", err)
	}
	if n, _ := st.CountUsers(ctx); n != 0 {
		t.Fatalf("CountUsers after delete = %d", n)
	}
}

func TestDeletingAUserEndsTheirSessions(t *testing.T) {
	st, ctx, now := openAccounts(t)
	mustUser(t, st, "u1", "quinn", now)
	if err := st.CreateSession(ctx, []byte("s1"), "u1", "203.0.113.1", now, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteUser(ctx, "u1"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.LookupSession(ctx, []byte("s1")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a deleted account's session: %v, want ErrNotFound", err)
	}
}

func TestSecondFactorStateIsReplacedWhole(t *testing.T) {
	st, ctx, now := openAccounts(t)
	mustUser(t, st, "u1", "quinn", now)

	// Pending setup: a secret, not yet enabled.
	if err := st.SetTOTP(ctx, "u1", "BASE32SECRET", nil, nil); err != nil {
		t.Fatal(err)
	}
	u, _ := st.GetUser(ctx, "u1")
	if u.TOTPSecret != "BASE32SECRET" || u.TOTPEnabledAt != nil || len(u.TOTPRecovery) != 0 {
		t.Fatalf("pending setup: %+v", u)
	}
	// Confirmed, with recovery codes.
	at := now.Add(time.Minute)
	if err := st.SetTOTP(ctx, "u1", "BASE32SECRET", &at, []string{"h1", "h2"}); err != nil {
		t.Fatal(err)
	}
	u, _ = st.GetUser(ctx, "u1")
	if u.TOTPEnabledAt == nil || !u.TOTPEnabledAt.Equal(at) || !reflect.DeepEqual(u.TOTPRecovery, []string{"h1", "h2"}) {
		t.Fatalf("enabled: %+v", u)
	}
	// Off again clears all of it.
	if err := st.SetTOTP(ctx, "u1", "", nil, nil); err != nil {
		t.Fatal(err)
	}
	u, _ = st.GetUser(ctx, "u1")
	if u.TOTPSecret != "" || u.TOTPEnabledAt != nil || len(u.TOTPRecovery) != 0 {
		t.Fatalf("turned off: %+v", u)
	}
	if err := st.SetTOTP(ctx, "ghost", "x", nil, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown account: %v, want ErrNotFound", err)
	}
}

func TestEmailChangeDropsEmailAsASecondFactorUnlessVerified(t *testing.T) {
	st, ctx, now := openAccounts(t)
	mustUser(t, st, "u1", "quinn", now)

	verified := now.Add(time.Minute)
	if err := st.SetEmail(ctx, "u1", "q@example.com", &verified); err != nil {
		t.Fatal(err)
	}
	if err := st.SetEmailOTPEnabled(ctx, "u1", &verified); err != nil {
		t.Fatal(err)
	}
	u, _ := st.GetUser(ctx, "u1")
	if u.Email != "q@example.com" || u.EmailVerifiedAt == nil || u.EmailOTPEnabledAt == nil {
		t.Fatalf("verified and enabled: %+v", u)
	}

	// Setting an address without proof it can be reached unverifies it and turns the second factor off.
	if err := st.SetEmail(ctx, "u1", "typo@example.com", nil); err != nil {
		t.Fatal(err)
	}
	u, _ = st.GetUser(ctx, "u1")
	if u.Email != "typo@example.com" || u.EmailVerifiedAt != nil || u.EmailOTPEnabledAt != nil {
		t.Fatalf("an unverified address must not back a second factor: %+v", u)
	}
	// Turning the factor off does not forget the address.
	if err := st.SetEmail(ctx, "u1", "q@example.com", &verified); err != nil {
		t.Fatal(err)
	}
	if err := st.SetEmailOTPEnabled(ctx, "u1", &verified); err != nil {
		t.Fatal(err)
	}
	if err := st.SetEmailOTPEnabled(ctx, "u1", nil); err != nil {
		t.Fatal(err)
	}
	u, _ = st.GetUser(ctx, "u1")
	if u.Email != "q@example.com" || u.EmailVerifiedAt == nil || u.EmailOTPEnabledAt != nil {
		t.Fatalf("factor off, address kept: %+v", u)
	}
}

func TestPasskeysAddRenameTouchRemove(t *testing.T) {
	st, ctx, now := openAccounts(t)
	mustUser(t, st, "u1", "quinn", now)

	c1 := WebAuthnCredential{CredentialID: []byte("cred-1"), PublicKey: []byte("pub-1"), Transports: []string{"internal"}, Flags: 0x1d, Name: "laptop", CreatedAt: now}
	c2 := WebAuthnCredential{CredentialID: []byte("cred-2"), PublicKey: []byte("pub-2"), Name: "key", CreatedAt: now.Add(time.Second)}
	for _, c := range []WebAuthnCredential{c1, c2} {
		if err := st.AddWebAuthnCredential(ctx, "u1", c); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.AddWebAuthnCredential(ctx, "u1", c1); !errors.Is(err, ErrExists) {
		t.Fatalf("the same credential twice: %v, want ErrExists", err)
	}
	if err := st.AddWebAuthnCredential(ctx, "ghost", c1); err == nil {
		t.Fatal("adding to an unknown account must fail")
	}

	u, _ := st.GetUser(ctx, "u1")
	if len(u.WebAuthnCredentials) != 2 || !bytes.Equal(u.WebAuthnCredentials[0].CredentialID, []byte("cred-1")) || !bytes.Equal(u.WebAuthnCredentials[1].CredentialID, []byte("cred-2")) {
		t.Fatalf("credentials are listed oldest first: %+v", u.WebAuthnCredentials)
	}
	got := u.WebAuthnCredentials[0]
	if got.Flags != 0x1d || got.Name != "laptop" || !bytes.Equal(got.PublicKey, []byte("pub-1")) || !reflect.DeepEqual(got.Transports, []string{"internal"}) {
		t.Fatalf("credential did not round-trip: %+v", got)
	}

	if err := st.RenameWebAuthnCredential(ctx, "u1", []byte("cred-2"), "yubikey"); err != nil {
		t.Fatal(err)
	}
	used := now.Add(time.Hour)
	if err := st.TouchWebAuthnCredential(ctx, "u1", []byte("cred-1"), 7, used); err != nil {
		t.Fatal(err)
	}
	if err := st.RenameWebAuthnCredential(ctx, "u1", []byte("nope"), "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("rename an unknown credential: %v, want ErrNotFound", err)
	}
	if err := st.TouchWebAuthnCredential(ctx, "u1", []byte("nope"), 1, used); !errors.Is(err, ErrNotFound) {
		t.Fatalf("touch an unknown credential: %v, want ErrNotFound", err)
	}
	u, _ = st.GetUser(ctx, "u1")
	if u.WebAuthnCredentials[1].Name != "yubikey" || u.WebAuthnCredentials[0].SignCount != 7 ||
		u.WebAuthnCredentials[0].LastUsedAt == nil || !u.WebAuthnCredentials[0].LastUsedAt.Equal(used) {
		t.Fatalf("after rename and touch: %+v", u.WebAuthnCredentials)
	}

	if err := st.RemoveWebAuthnCredential(ctx, "u1", []byte("cred-1")); err != nil {
		t.Fatal(err)
	}
	if err := st.RemoveWebAuthnCredential(ctx, "u1", []byte("cred-1")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("remove twice: %v, want ErrNotFound", err)
	}
	u, _ = st.GetUser(ctx, "u1")
	if len(u.WebAuthnCredentials) != 1 || !bytes.Equal(u.WebAuthnCredentials[0].CredentialID, []byte("cred-2")) {
		t.Fatalf("after remove: %+v", u.WebAuthnCredentials)
	}
}

func TestSessionsLookupTouchDeleteAndPurge(t *testing.T) {
	st, ctx, now := openAccounts(t)
	mustUser(t, st, "u1", "quinn", now)
	mustUser(t, st, "u2", "casey", now)

	exp := now.Add(8 * time.Hour)
	if err := st.CreateSession(ctx, []byte("a"), "u1", "203.0.113.1", now, exp); err != nil {
		t.Fatal(err)
	}
	se, u, err := st.LookupSession(ctx, []byte("a"))
	if err != nil {
		t.Fatal(err)
	}
	if u.ID != "u1" || se.UserID != "u1" || se.IP != "203.0.113.1" || !se.CreatedAt.Equal(now) || !se.LastUsed.Equal(now) || !se.ExpiresAt.Equal(exp) {
		t.Fatalf("session = %+v user = %+v", se, u)
	}
	// Expiry is the caller's decision: an expired session still resolves.
	if _, _, err := st.LookupSession(ctx, []byte("a")); err != nil {
		t.Fatal(err)
	}
	later := now.Add(time.Minute)
	if err := st.TouchSession(ctx, []byte("a"), later); err != nil {
		t.Fatal(err)
	}
	se, _, _ = st.LookupSession(ctx, []byte("a"))
	if !se.LastUsed.Equal(later) {
		t.Fatalf("LastUsed after touch = %v, want %v", se.LastUsed, later)
	}
	if _, _, err := st.LookupSession(ctx, []byte("unknown")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown session: %v, want ErrNotFound", err)
	}

	// DeleteUserSessions keeps exactly the excepted one, and only for that user.
	for _, h := range []string{"b", "c"} {
		if err := st.CreateSession(ctx, []byte(h), "u1", "ip", now, exp); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.CreateSession(ctx, []byte("other"), "u2", "ip", now, exp); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteUserSessions(ctx, "u1", []byte("b")); err != nil {
		t.Fatal(err)
	}
	for h, want := range map[string]bool{"a": false, "b": true, "c": false, "other": true} {
		_, _, err := st.LookupSession(ctx, []byte(h))
		if (err == nil) != want {
			t.Fatalf("session %q alive = %v, want %v", h, err == nil, want)
		}
	}
	if err := st.DeleteUserSessions(ctx, "u1", nil); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.LookupSession(ctx, []byte("b")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("no exception means every session goes: %v", err)
	}
	if err := st.DeleteSession(ctx, []byte("other")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.LookupSession(ctx, []byte("other")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted session: %v", err)
	}

	// PurgeSessions removes sessions that expired before the cutoff, or went unused for over a day before it.
	if err := st.CreateSession(ctx, []byte("old"), "u1", "ip", now.Add(-72*time.Hour), now.Add(-48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateSession(ctx, []byte("fresh"), "u1", "ip", now, now.Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := st.PurgeSessions(ctx, now); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.LookupSession(ctx, []byte("old")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an expired session survived the purge: %v", err)
	}
	if _, _, err := st.LookupSession(ctx, []byte("fresh")); err != nil {
		t.Fatalf("a live session was purged: %v", err)
	}
}

func TestPersonalAPITokensBelongToTheirOwner(t *testing.T) {
	st, ctx, now := openAccounts(t)
	mustUser(t, st, "u1", "quinn", now)
	mustUser(t, st, "u2", "casey", now)

	if err := st.CreateAPIToken(ctx, "t1", []byte("h1"), "u1", "ci", now); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateAPIToken(ctx, "t2", []byte("h2"), "u1", "laptop", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateAPIToken(ctx, "t3", []byte("h3"), "u2", "ci", now); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateAPIToken(ctx, "t1", []byte("h9"), "u1", "dup id", now); err == nil {
		t.Fatal("a token id is unique")
	}

	list, err := st.ListAPITokens(ctx, "u1")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].ID != "t2" || list[1].ID != "t1" {
		t.Fatalf("a person's tokens are listed newest first, only their own: %+v", list)
	}
	if list[0].LastUsed != nil {
		t.Fatalf("never-used token has LastUsed %v", list[0].LastUsed)
	}

	tok, u, err := st.LookupAPIToken(ctx, []byte("h1"))
	if err != nil || tok.ID != "t1" || tok.Name != "ci" || u.ID != "u1" {
		t.Fatalf("LookupAPIToken = %+v %+v %v", tok, u, err)
	}
	if _, _, err := st.LookupAPIToken(ctx, []byte("nope")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown token: %v, want ErrNotFound", err)
	}
	used := now.Add(time.Hour)
	if err := st.TouchAPIToken(ctx, []byte("h1"), used); err != nil {
		t.Fatal(err)
	}
	tok, _, _ = st.LookupAPIToken(ctx, []byte("h1"))
	if tok.LastUsed == nil || !tok.LastUsed.Equal(used) {
		t.Fatalf("LastUsed after touch = %v", tok.LastUsed)
	}

	// One account can never revoke another's token by guessing its id.
	if err := st.RevokeAPIToken(ctx, "u2", "t1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoking someone else's token: %v, want ErrNotFound", err)
	}
	if _, _, err := st.LookupAPIToken(ctx, []byte("h1")); err != nil {
		t.Fatalf("the token must have survived: %v", err)
	}
	if err := st.RevokeAPIToken(ctx, "u1", "t1"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.LookupAPIToken(ctx, []byte("h1")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked token: %v, want ErrNotFound", err)
	}
}
