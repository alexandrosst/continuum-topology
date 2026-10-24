package store

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ikhnos/internal/sealing"
)

var testNow = time.UnixMilli(1_700_000_000_000).UTC()

const (
	sealKeyA = "an-example-key-that-is-long-enough-A"
	sealKeyB = "an-example-key-that-is-long-enough-B"
	secretA  = "JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP"
	secretB  = "MFRGGZDFMZTWQ2LKNNWG23TPOBYXE43U"
)

func sealerOf(t *testing.T, keys ...string) *sealing.Sealer {
	t.Helper()
	var ks [][]byte
	for _, k := range keys {
		ks = append(ks, []byte(k))
	}
	s, err := sealing.New(ks...)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// filesHold is whether any file of the database (the main file, the write-ahead log, the shared memory) holds the text.
func filesHold(t *testing.T, dir, text string) bool {
	t.Helper()
	found := false
	entries, _ := os.ReadDir(dir)
	if len(entries) == 0 {
		t.Fatal("no database files to read")
	}
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(b, []byte(text)) {
			found = true
		}
	}
	return found
}

func reopen(t *testing.T, dir string) *SQLite {
	t.Helper()
	st, err := OpenSQLite(filepath.Join(dir, "acc.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestASecondFactorSecretIsInNoFileOfTheDatabaseOnceSealingIsOn(t *testing.T) {
	dir := t.TempDir()
	st, err := OpenSQLite(filepath.Join(dir, "acc.db"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := st.UseSealer(ctx, sealerOf(t, sealKeyA)); err != nil {
		t.Fatal(err)
	}
	mustUser(t, st, "u1", "quinn", testNow)
	if err := st.SetTOTP(ctx, "u1", secretA, nil, nil); err != nil {
		t.Fatal(err)
	}
	// The caller still sees the secret: sealing is not something the rest of the server notices.
	if u, err := st.GetUser(ctx, "u1"); err != nil || u.TOTPSecret != secretA {
		t.Fatalf("GetUser = %q, %v", u.TOTPSecret, err)
	}
	if u, err := st.GetUserByName(ctx, "quinn"); err != nil || u.TOTPSecret != secretA {
		t.Fatalf("GetUserByName = %q, %v", u.TOTPSecret, err)
	}
	var raw string
	if err := st.db.QueryRow(`SELECT totp_secret FROM users WHERE id='u1'`).Scan(&raw); err != nil || !sealing.IsSealed(raw) {
		t.Fatalf("the column holds %q, %v", raw, err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if filesHold(t, dir, secretA) {
		t.Fatal("the secret is readable in a file of the database")
	}
}

// The control for the test above: without sealing the same scan finds the secret, so a pass there means something.
func TestTheScanForASecretFindsItWhenNothingIsSealed(t *testing.T) {
	dir := t.TempDir()
	st, err := OpenSQLite(filepath.Join(dir, "acc.db"))
	if err != nil {
		t.Fatal(err)
	}
	mustUser(t, st, "u1", "quinn", testNow)
	if err := st.SetTOTP(context.Background(), "u1", secretA, nil, nil); err != nil {
		t.Fatal(err)
	}
	st.Close()
	if !filesHold(t, dir, secretA) {
		t.Fatal("the scan does not see a secret that is there")
	}
}

func TestSecretsStoredBeforeSealingAreSealedWhenItIsTurnedOn(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	st, _ := OpenSQLite(filepath.Join(dir, "acc.db"))
	mustUser(t, st, "u1", "quinn", testNow)
	mustUser(t, st, "u2", "rae", testNow)
	mustUser(t, st, "u3", "sam", testNow) // no second factor at all
	for id, s := range map[string]string{"u1": secretA, "u2": secretB} {
		if err := st.SetTOTP(ctx, id, s, nil, []string{"h1"}); err != nil {
			t.Fatal(err)
		}
	}
	st.Close()

	st = reopen(t, dir)
	if err := st.UseSealer(ctx, sealerOf(t, sealKeyA)); err != nil {
		t.Fatal(err)
	}
	if err := st.ScrubPlaintext(ctx); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]string{"u1": secretA, "u2": secretB, "u3": ""} {
		u, err := st.GetUser(ctx, id)
		if err != nil || u.TOTPSecret != want {
			t.Fatalf("%s: %q, %v", id, u.TOTPSecret, err)
		}
	}
	if u, _ := st.GetUser(ctx, "u1"); len(u.TOTPRecovery) != 1 {
		t.Fatalf("the recovery hashes were disturbed: %v", u.TOTPRecovery)
	}
	var raw string
	st.db.QueryRow(`SELECT totp_secret FROM users WHERE id='u3'`).Scan(&raw)
	if raw != "" {
		t.Fatalf("a user with no secret got %q", raw)
	}
	st.Close()
	if filesHold(t, dir, secretA) || filesHold(t, dir, secretB) {
		t.Fatal("a secret that was stored in the clear is still readable in the files after sealing was turned on")
	}
}

func TestADatabaseThatWasSealedDoesNotStartWithoutItsKey(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	st, _ := OpenSQLite(filepath.Join(dir, "acc.db"))
	if err := st.UseSealer(ctx, sealerOf(t, sealKeyA)); err != nil {
		t.Fatal(err)
	}
	st.Close()

	for _, sl := range []*sealing.Sealer{nil, {}} {
		st = reopen(t, dir)
		err := st.UseSealer(ctx, sl)
		if !errors.Is(err, ErrSealingKeyNeeded) || !strings.Contains(err.Error(), sealerOf(t, sealKeyA).KeyID()) {
			t.Fatalf("without a key: %v", err)
		}
	}
}

func TestAnotherKeyIsRefusedAtStartAndNamesTheKeyThatIsMissing(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	st, _ := OpenSQLite(filepath.Join(dir, "acc.db"))
	if err := st.UseSealer(ctx, sealerOf(t, sealKeyA)); err != nil {
		t.Fatal(err)
	}
	st.Close()
	st = reopen(t, dir)
	err := st.UseSealer(ctx, sealerOf(t, sealKeyB))
	if err == nil || !strings.Contains(err.Error(), sealerOf(t, sealKeyA).KeyID()) || !strings.Contains(err.Error(), sealerOf(t, sealKeyB).KeyID()) {
		t.Fatalf("another key: %v", err)
	}
	if st.Sealer().Enabled() {
		t.Fatal("a refused key was kept")
	}
}

func TestAKeyIsReplacedByPuttingTheNewOneOnTopAndTheOldOneCanLeaveAfterOneStart(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	st, _ := OpenSQLite(filepath.Join(dir, "acc.db"))
	st.UseSealer(ctx, sealerOf(t, sealKeyA))
	mustUser(t, st, "u1", "quinn", testNow)
	st.SetTOTP(ctx, "u1", secretA, nil, nil)
	st.Close()

	// Both keys, the new one first: everything opens, and is sealed again under the new one.
	st = reopen(t, dir)
	if err := st.UseSealer(ctx, sealerOf(t, sealKeyB, sealKeyA)); err != nil {
		t.Fatal(err)
	}
	var raw string
	st.db.QueryRow(`SELECT totp_secret FROM users WHERE id='u1'`).Scan(&raw)
	if sealing.SealedUnder(raw) != sealerOf(t, sealKeyB).KeyID() {
		t.Fatalf("the secret is sealed under %q, not the new key", sealing.SealedUnder(raw))
	}
	st.Close()

	// The old key taken away: the database starts, and the secret is still there.
	st = reopen(t, dir)
	if err := st.UseSealer(ctx, sealerOf(t, sealKeyB)); err != nil {
		t.Fatal(err)
	}
	if u, err := st.GetUser(ctx, "u1"); err != nil || u.TOTPSecret != secretA {
		t.Fatalf("after the old key left: %q, %v", u.TOTPSecret, err)
	}
	st.Close()

	// And the old key alone no longer starts it.
	st = reopen(t, dir)
	if err := st.UseSealer(ctx, sealerOf(t, sealKeyA)); err == nil {
		t.Fatal("the old key alone started a database that was moved to the new one")
	}
}

func TestASealedSecretCopiedToAnotherUserDoesNotOpenThere(t *testing.T) {
	st, ctx, now := openAccounts(t)
	st.UseSealer(ctx, sealerOf(t, sealKeyA))
	mustUser(t, st, "u1", "quinn", now)
	mustUser(t, st, "u2", "rae", now)
	st.SetTOTP(ctx, "u1", secretA, nil, nil)
	if _, err := st.db.Exec(`UPDATE users SET totp_secret=(SELECT totp_secret FROM users WHERE id='u1') WHERE id='u2'`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetUser(ctx, "u2"); !errors.Is(err, sealing.ErrCorrupt) {
		t.Fatalf("a secret sealed for another user opened: %v", err)
	}
}

func TestResealingTouchesNothingButTheSecretsThatNeedIt(t *testing.T) {
	st, ctx, now := openAccounts(t)
	st.UseSealer(ctx, sealerOf(t, sealKeyA))
	mustUser(t, st, "u1", "quinn", now)
	st.SetTOTP(ctx, "u1", secretA, &now, []string{"h1"})
	var before string
	st.db.QueryRow(`SELECT totp_secret FROM users WHERE id='u1'`).Scan(&before)
	if err := st.resealTOTP(ctx); err != nil {
		t.Fatal(err)
	}
	var after string
	st.db.QueryRow(`SELECT totp_secret FROM users WHERE id='u1'`).Scan(&after)
	if before != after {
		t.Fatal("a secret that was current was sealed again")
	}
}

func TestALostKeyCanBeLeftBehindAndOnlyTheSealedSecretsGo(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	st, _ := OpenSQLite(filepath.Join(dir, "acc.db"))
	st.UseSealer(ctx, sealerOf(t, sealKeyA))
	mustUser(t, st, "u1", "quinn", testNow)
	mustUser(t, st, "u2", "rae", testNow)
	at := testNow
	st.SetTOTP(ctx, "u1", secretA, &at, []string{"h1"})
	st.Close()

	st = reopen(t, dir)
	names, err := st.DiscardSealedTOTP(ctx)
	if err != nil || len(names) != 1 || names[0] != "quinn" {
		t.Fatalf("DiscardSealedTOTP = %v, %v", names, err)
	}
	// Still asks for a key until the check row is dropped: an interrupted recovery leaves nothing half done.
	if err := st.UseSealer(ctx, nil); !errors.Is(err, ErrSealingKeyNeeded) {
		t.Fatalf("before the check row is dropped: %v", err)
	}
	if err := st.DropSealingCheck(ctx); err != nil {
		t.Fatal(err)
	}
	if err := st.UseSealer(ctx, nil); err != nil {
		t.Fatalf("after: %v", err)
	}
	u, err := st.GetUser(ctx, "u1")
	if err != nil || u.TOTPSecret != "" || u.TOTPEnabledAt != nil || len(u.TOTPRecovery) != 0 {
		t.Fatalf("the user after: %+v, %v", u, err)
	}
	// A new key then seals from a clean start.
	if err := st.UseSealer(ctx, sealerOf(t, sealKeyB)); err != nil {
		t.Fatal(err)
	}
}

// The mark that says the file still has to be rewritten stays in the database until the rewrite is done. A start that is killed
// in the middle of it (or whose rewrite fails) then finds the mark and does the work again; with the mark gone first, it would
// find nothing to do and the old text would stay in the free pages for good.
func TestTheMarkToScrubStaysUntilTheFileHasBeenRewritten(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	st, _ := OpenSQLite(filepath.Join(dir, "acc.db"))
	mustUser(t, st, "u1", "quinn", testNow)
	if err := st.SetTOTP(ctx, "u1", secretA, nil, nil); err != nil {
		t.Fatal(err)
	}
	st.Close()
	st = reopen(t, dir)
	if err := st.UseSealer(ctx, sealerOf(t, sealKeyA)); err != nil {
		t.Fatal(err)
	}
	real := vacuumFile
	defer func() { vacuumFile = real }()
	var markDuringRewrite bool
	vacuumFile = func(c context.Context, db *sql.DB) error {
		m, err := st.GetSettings(c, sealingScrubKey)
		markDuringRewrite = err == nil && m != nil
		return errors.New("killed during the rewrite")
	}
	if err := st.ScrubPlaintext(ctx); err == nil {
		t.Fatal("a rewrite that failed was reported as done")
	}
	if !markDuringRewrite {
		t.Error("the mark was already gone when the file was rewritten")
	}
	if m, _ := st.GetSettings(ctx, sealingScrubKey); m == nil {
		t.Fatal("a failed rewrite left no mark, so the next start would not try again")
	}
	vacuumFile = real
	if err := st.ScrubPlaintext(ctx); err != nil {
		t.Fatal(err)
	}
	if m, _ := st.GetSettings(ctx, sealingScrubKey); m != nil {
		t.Error("the mark is still there after the rewrite")
	}
	st.Close()
	if filesHold(t, dir, secretA) {
		t.Fatal("the secret is still readable in the files")
	}
}
