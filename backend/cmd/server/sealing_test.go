package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"continuum/internal/accounts"
	"continuum/internal/sealing"
	"continuum/internal/server"
	"continuum/internal/store"
)

const (
	cmdKeyA = "an-example-key-that-is-long-enough-A"
	cmdKeyB = "an-example-key-that-is-long-enough-B"
)

func quietLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func writeKeyFile(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "sealing.key")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestTheKeyFileIsReadOrRefusedNeverSkipped(t *testing.T) {
	log := quietLog()
	if sl, err := loadSealer(log, ""); err != nil || sl.Enabled() {
		t.Fatalf("no file: %v, %v", sl, err)
	}
	if _, err := loadSealer(log, filepath.Join(t.TempDir(), "absent")); err == nil || !strings.Contains(err.Error(), "--sealing-key-file") {
		t.Fatalf("a file that is not there must stop the server, not turn sealing off: %v", err)
	}
	if _, err := loadSealer(log, writeKeyFile(t, "short\n")); err == nil || !strings.Contains(err.Error(), "at least 32") {
		t.Fatalf("a short key: %v", err)
	}
	if _, err := loadSealer(log, writeKeyFile(t, "\n# nothing\n")); err == nil {
		t.Fatal("a file with no key was accepted")
	}
	sl, err := loadSealer(log, writeKeyFile(t, "# current\n"+cmdKeyB+"\n# before\n"+cmdKeyA+"\n"))
	if err != nil || len(sl.KeyIDs()) != 2 {
		t.Fatalf("two keys: %v, %v", sl, err)
	}
	only, _ := loadSealer(log, writeKeyFile(t, cmdKeyB))
	if sl.KeyID() != only.KeyID() {
		t.Fatal("the first line is not the key that seals")
	}
}

// sealedWorld is a database that was sealed under key A and holds one of each secret.
func sealedWorld(t *testing.T) (st *store.SQLite, dir string) {
	t.Helper()
	dir = t.TempDir()
	ctx := context.Background()
	st, err := store.OpenSQLite(filepath.Join(dir, "continuum.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	sl, _ := sealing.New([]byte(cmdKeyA))
	if err := st.UseSealer(ctx, sl); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := st.CreateUser(ctx, store.User{ID: "u1", Username: "quinn", PasswordHash: "x", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateUser(ctx, store.User{ID: "u2", Username: "rae", PasswordHash: "x", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetTOTP(ctx, "u1", "JBSWY3DPEHPK3PXP", &now, []string{"h"}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateOrg(ctx, store.Org{ID: "org-1", Name: "one", CreatedAt: now, CreatedBy: "u2"}, "u2"); err != nil {
		t.Fatal(err)
	}
	core := server.NewCore(st, nil, "", quietLog())
	core.Sealer = sl
	if _, err := core.Accounts.SaveMailConfig(ctx, "t", accounts.MailConfig{Host: "smtp.example.com", Port: "587", Username: "u", Password: "a-mail-password-that-is-sealed", From: "x@example.com"}); err != nil {
		t.Fatal(err)
	}
	oc := core.ForOrg("org-1")
	sealedDoc, _ := sl.Seal("decider-secret", "org-1", "decider-secret-long-enough-for-validation")
	body, _ := json.Marshal(map[string]any{"deciderUrl": "https://decider.example.com/x", "deciderSecret": sealedDoc, "snapshotMinutes": 7, "_rev": 3})
	if err := st.PutSettings(ctx, "org-1", body, now); err != nil {
		t.Fatal(err)
	}
	_ = oc
	return st, dir
}

func TestResetSealingSaysWhatItWouldRemoveAndRemovesNothingWithoutYes(t *testing.T) {
	st, _ := sealedWorld(t)
	var out bytes.Buffer
	if err := discardSealed(context.Background(), quietLog(), st, false, &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"second factor", "mail password", "decider", "--yes"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the description does not mention %q:\n%s", want, out.String())
		}
	}
	if u, _ := st.GetUser(context.Background(), "u1"); u.TOTPSecret != "JBSWY3DPEHPK3PXP" {
		t.Fatalf("something was removed without --yes: %+v", u)
	}
}

func TestResetSealingRemovesOnlyWhatCannotBeReadAndLeavesTheServerStartable(t *testing.T) {
	st, dir := sealedWorld(t)
	ctx := context.Background()
	st.Close()
	// A fresh start with no key: refused.
	st, err := store.OpenSQLite(filepath.Join(dir, "continuum.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.UseSealer(ctx, nil); err == nil {
		t.Fatal("a sealed database started without its key")
	}
	var out bytes.Buffer
	if err := discardSealed(ctx, quietLog(), st, true, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "quinn") || !strings.Contains(out.String(), "org-1") {
		t.Fatalf("the report does not name what was removed:\n%s", out.String())
	}
	// Now it starts without a key, and then with a new one.
	if err := st.UseSealer(ctx, nil); err != nil {
		t.Fatalf("after the recovery: %v", err)
	}
	u, err := st.GetUser(ctx, "u1")
	if err != nil || u.TOTPSecret != "" || u.TOTPEnabledAt != nil {
		t.Fatalf("quinn after: %+v, %v", u, err)
	}
	raw, _ := st.GetMailConfig(ctx)
	if bytes.Contains(raw, []byte("sealed:v1:")) {
		t.Fatalf("the sealed mail password is still there: %s", raw)
	}
	var kept map[string]any
	rawSettings, _ := st.GetSettings(ctx, "org-1")
	if err := json.Unmarshal(rawSettings, &kept); err != nil {
		t.Fatal(err)
	}
	if kept["deciderUrl"] != nil && kept["deciderUrl"] != "" || kept["deciderSecret"] != nil && kept["deciderSecret"] != "" {
		t.Fatalf("the decider is still configured, and would send unsigned requests: %v", kept)
	}
	if kept["snapshotMinutes"] != float64(7) {
		t.Fatalf("a setting that had nothing to do with it was changed: %v", kept["snapshotMinutes"])
	}
	other, _ := sealing.New([]byte(cmdKeyB))
	if err := st.UseSealer(ctx, other); err != nil {
		t.Fatalf("a new key after the recovery: %v", err)
	}
}

func TestResetSealingOnADatabaseThatWasNeverSealedDoesNothing(t *testing.T) {
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "continuum.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var out bytes.Buffer
	if err := discardSealed(context.Background(), quietLog(), st, true, &out); err != nil || !strings.Contains(out.String(), "no sealed secrets") {
		t.Fatalf("%v: %s", err, out.String())
	}
}
