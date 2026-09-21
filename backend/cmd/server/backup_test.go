package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"continuum/internal/pki"
	"continuum/internal/store"
)

// liveDataDir makes a data directory the way a running server has one: a database that is open (so part of what
// it holds is still in the write-ahead log) and a certificate authority.
func liveDataDir(t *testing.T) (dir string, st *store.SQLite) {
	t.Helper()
	dir = t.TempDir()
	if _, err := pki.LoadOrCreate(filepath.Join(dir, "pki")); err != nil {
		t.Fatal(err)
	}
	st, err := store.OpenSQLite(filepath.Join(dir, "continuum.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	u := store.User{ID: "u1", Username: "owner", PasswordHash: "!", CreatedAt: time.Now()}
	if err := st.CreateUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateOrg(ctx, store.Org{ID: "org-1", Name: "One", CreatedAt: time.Now(), CreatedBy: u.ID}, u.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutWorkspace(ctx, "org-1", 0, []byte(`{"schemaVersion":4,"sites":[{"id":"s1","name":"Athens"}]}`), "owner", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := st.PutTombstones(ctx, "org-1", []store.Tombstone{{Kind: "service", ID: "sv-1", Name: "pay", GoneAt: time.Now(), Reason: "deleted"}}); err != nil {
		t.Fatal(err)
	}
	return dir, st
}

func TestBackupOfARunningServerRestoresEverythingItHeld(t *testing.T) {
	dir, _ := liveDataDir(t)
	file := filepath.Join(t.TempDir(), "b.tar.gz")
	var out, errw bytes.Buffer
	if code := backupCmd([]string{"--data-dir", dir, "--out", file}, &out, &errw); code != 0 {
		t.Fatalf("backup: %d %s", code, errw.String())
	}
	if fi, err := os.Stat(file); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("a backup holds the CA key and must be private: %v %v", fi, err)
	}
	// Refuses to overwrite a backup.
	if code := backupCmd([]string{"--data-dir", dir, "--out", file}, &out, &errw); code == 0 {
		t.Fatal("a backup was overwritten")
	}

	target := filepath.Join(t.TempDir(), "restored")
	out.Reset()
	if code := restoreCmd([]string{"--data-dir", target, "--from", file}, &out, &errw); code != 0 {
		t.Fatalf("restore: %d %s", code, errw.String())
	}
	// The restored server opens (and finds nothing to migrate), holds what the original held, and has the same CA.
	st2, err := store.OpenSQLite(filepath.Join(target, "continuum.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	ctx := context.Background()
	if o, err := st2.GetOrg(ctx, "org-1"); err != nil || o.Name != "One" {
		t.Fatalf("org: %+v %v", o, err)
	}
	if w, err := st2.GetWorkspace(ctx, "org-1"); err != nil || !strings.Contains(string(w.Data), "Athens") {
		t.Fatalf("workspace: %+v %v", w, err)
	}
	if ts, err := st2.ListTombstones(ctx, "org-1"); err != nil || len(ts) != 1 || ts[0].Name != "pay" {
		t.Fatalf("tombstones: %+v %v", ts, err)
	}
	for _, f := range []string{"ca.crt", "ca.key"} {
		a, _ := os.ReadFile(filepath.Join(dir, "pki", f))
		b, _ := os.ReadFile(filepath.Join(target, "pki", f))
		if len(a) == 0 || !bytes.Equal(a, b) {
			t.Fatalf("%s differs after the round trip", f)
		}
	}
	if fi, _ := os.Stat(filepath.Join(target, "pki", "ca.key")); fi.Mode().Perm() != 0o600 {
		t.Fatalf("restored ca.key is %v", fi.Mode())
	}
	if ca, err := pki.LoadOrCreate(filepath.Join(target, "pki")); err != nil || ca == nil {
		t.Fatalf("the restored CA does not load: %v", err)
	}
	// Restoring over an existing installation needs --force, and then moves what was there aside.
	errw.Reset()
	if code := restoreCmd([]string{"--data-dir", target, "--from", file}, &out, &errw); code == 0 || !strings.Contains(errw.String(), "--force") {
		t.Fatalf("restore over existing data without --force: %d %s", code, errw.String())
	}
	st2.Close()
	if code := restoreCmd([]string{"--data-dir", target, "--from", file, "--force"}, &out, &errw); code != 0 {
		t.Fatalf("forced restore: %d %s", code, errw.String())
	}
	matches, _ := filepath.Glob(filepath.Join(target, "pre-restore-*", "continuum.db"))
	if len(matches) != 1 {
		t.Fatalf("the replaced database was not kept aside: %v", matches)
	}
}

// pack builds a backup archive by hand so a test can damage or age it.
func pack(t *testing.T, files map[string][]byte, edit func(*backupManifest)) string {
	t.Helper()
	m := backupManifest{Format: backupFormat, CreatedAt: "2026-01-01T00:00:00Z", SchemaVersion: store.SchemaVersion}
	for n, b := range files {
		h := sha256.Sum256(b)
		m.Files = append(m.Files, backupFile{Name: n, Size: int64(len(b)), SHA256: hex.EncodeToString(h[:])})
	}
	if edit != nil {
		edit(&m)
	}
	raw, _ := json.Marshal(m)
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	all := map[string][]byte{"manifest.json": raw}
	for n, b := range files {
		all[n] = b
	}
	for n, b := range all {
		_ = writeTar(tw, n, b)
	}
	tw.Close()
	gz.Close()
	p := filepath.Join(t.TempDir(), "x.tar.gz")
	if err := os.WriteFile(p, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func goodFiles(t *testing.T) map[string][]byte {
	t.Helper()
	dir, _ := liveDataDir(t)
	f := filepath.Join(t.TempDir(), "b.tar.gz")
	var e bytes.Buffer
	if code := backupCmd([]string{"--data-dir", dir, "--out", f}, &e, &e); code != 0 {
		t.Fatal(e.String())
	}
	r, _ := os.Open(f)
	defer r.Close()
	gz, _ := gzip.NewReader(r)
	tr := tar.NewReader(gz)
	files := map[string][]byte{}
	for {
		h, err := tr.Next()
		if err != nil {
			break
		}
		var b bytes.Buffer
		b.ReadFrom(tr)
		if h.Name != "manifest.json" {
			files[h.Name] = b.Bytes()
		}
	}
	return files
}

func TestRestoreRefusesWhatItCannotTrust(t *testing.T) {
	files := goodFiles(t)
	try := func(name string, p string, want string) {
		t.Helper()
		target := filepath.Join(t.TempDir(), "t")
		var out, errw bytes.Buffer
		if code := restoreCmd([]string{"--data-dir", target, "--from", p}, &out, &errw); code == 0 || !strings.Contains(errw.String(), want) {
			t.Errorf("%s: code %d, message %q (want %q)", name, code, errw.String(), want)
		}
		if _, err := os.Stat(filepath.Join(target, "continuum.db")); err == nil {
			t.Errorf("%s: a refused restore left a database behind", name)
		}
	}
	if code := restoreCmd([]string{"--data-dir", t.TempDir(), "--from", pack(t, files, nil)}, &bytes.Buffer{}, &bytes.Buffer{}); code != 0 {
		t.Fatal("a valid hand-made archive was refused")
	}
	// A changed file no longer matches the checksum in the manifest.
	bad := map[string][]byte{}
	for k, v := range files {
		bad[k] = v
	}
	changed := append([]byte(nil), files["pki/ca.key"]...)
	changed[len(changed)/2] ^= 0xff
	bad["pki/ca.key"] = changed
	tampered := pack(t, bad, func(m *backupManifest) {
		h := sha256.Sum256(files["pki/ca.key"]) // the manifest still lists the original checksum
		for i := range m.Files {
			if m.Files[i].Name == "pki/ca.key" {
				m.Files[i].SHA256 = hex.EncodeToString(h[:])
			}
		}
	})
	try("tampered", tampered, "checksum")
	try("newer archive format", pack(t, files, func(m *backupManifest) { m.Format = 99 }), "newer Continuum")
	try("stray entry", pack(t, map[string][]byte{"continuum.db": files["continuum.db"], "pki/ca.crt": files["pki/ca.crt"], "pki/ca.key": files["pki/ca.key"], "../evil": []byte("x")}, nil), "unexpected entry")
	try("no CA", pack(t, map[string][]byte{"continuum.db": files["continuum.db"]}, nil), "lacks")

	// A database written by a newer server is refused with the reason, and nothing is put in place.
	newer := filepath.Join(t.TempDir(), "newer.db")
	if err := os.WriteFile(newer, files["continuum.db"], 0o600); err != nil {
		t.Fatal(err)
	}
	db, _ := sql.Open("sqlite", "file:"+newer)
	if _, err := db.Exec("PRAGMA user_version = 99"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	nb, _ := os.ReadFile(newer)
	nf := map[string][]byte{"continuum.db": nb, "pki/ca.crt": files["pki/ca.crt"], "pki/ca.key": files["pki/ca.key"]}
	try("newer database", pack(t, nf, nil), "newer Continuum")
	try("not a backup", filepath.Join(t.TempDir(), "missing"), "no such file")
}

func TestBackupOfANewerDatabaseIsRefused(t *testing.T) {
	dir, st := liveDataDir(t)
	st.Close()
	db, _ := sql.Open("sqlite", "file:"+filepath.Join(dir, "continuum.db"))
	db.Exec("PRAGMA user_version = 99")
	db.Close()
	file := filepath.Join(t.TempDir(), "b.tar.gz")
	var out, errw bytes.Buffer
	if code := backupCmd([]string{"--data-dir", dir, "--out", file}, &out, &errw); code == 0 || !strings.Contains(errw.String(), "newer Continuum") {
		t.Fatalf("%d %s", code, errw.String())
	}
	if _, err := os.Stat(file); err == nil {
		t.Fatal("a refused backup left a file")
	}
}

// `--out -` streams the archive, so a backup can be taken out of a container that has no shell and no tar. What
// comes out must restore like a file, and no working files may be left in the data directory.
func TestBackupToAStreamRestoresAndLeavesNothingBehind(t *testing.T) {
	dir, _ := liveDataDir(t)
	var buf bytes.Buffer
	if _, err := writeBackup(context.Background(), dir, dir, &buf); err != nil {
		t.Fatal(err)
	}
	if left, _ := filepath.Glob(filepath.Join(dir, ".backup-*")); len(left) != 0 {
		t.Fatalf("working files left behind: %v", left)
	}
	file := filepath.Join(t.TempDir(), "s.tar.gz")
	if err := os.WriteFile(file, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "restored")
	var out, errw bytes.Buffer
	if code := restoreCmd([]string{"--data-dir", target, "--from", file}, &out, &errw); code != 0 {
		t.Fatalf("restore of a streamed backup: %d %s", code, errw.String())
	}
	st2, err := store.OpenSQLite(filepath.Join(target, "continuum.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	if w, err := st2.GetWorkspace(context.Background(), "org-1"); err != nil || !strings.Contains(string(w.Data), "Athens") {
		t.Fatalf("workspace: %+v %v", w, err)
	}
}

// `--from -` reads the archive from standard input, which is how a backup is restored in a pod that has no shell.
func TestRestoreFromStandardInput(t *testing.T) {
	dir, _ := liveDataDir(t)
	file := filepath.Join(t.TempDir(), "b.tar.gz")
	var out, errw bytes.Buffer
	if code := backupCmd([]string{"--data-dir", dir, "--out", file}, &out, &errw); code != 0 {
		t.Fatalf("backup: %d %s", code, errw.String())
	}
	in, err := os.Open(file)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	old := os.Stdin
	os.Stdin = in
	defer func() { os.Stdin = old }()
	target := filepath.Join(t.TempDir(), "restored")
	if code := restoreCmd([]string{"--data-dir", target, "--from", "-"}, &out, &errw); code != 0 {
		t.Fatalf("restore from stdin: %d %s", code, errw.String())
	}
	if _, err := os.Stat(filepath.Join(target, "pki", "ca.key")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CheckDatabase(context.Background(), filepath.Join(target, "continuum.db")); err != nil {
		t.Fatal(err)
	}
}
