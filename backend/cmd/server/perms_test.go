package main

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"continuum/internal/pki"
	"continuum/internal/store"
)

func testLog() (*slog.Logger, *bytes.Buffer) {
	var b bytes.Buffer
	return slog.New(slog.NewTextHandler(&b, nil)), &b
}

func privateTemp(t *testing.T) string {
	d := t.TempDir()
	if err := os.Chmod(d, 0o700); err != nil {
		t.Fatal(err)
	}
	return d
}

func skipOnWindows(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix modes")
	}
}

func TestFilesTheServerCreatesArePrivate(t *testing.T) {
	skipOnWindows(t)
	dir := filepath.Join(privateTemp(t), "data")
	if err := ensurePrivateDir(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := pki.LoadOrCreate(filepath.Join(dir, "pki")); err != nil {
		t.Fatal(err)
	}
	st, err := store.OpenSQLite(filepath.Join(dir, "continuum.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	// Force the -wal and -shm files to exist.
	if _, err := st.ListMembers(t.Context(), ""); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"", "pki", "pki/ca.key", "continuum.db", "continuum.db-wal", "continuum.db-shm"} {
		fi, err := os.Stat(filepath.Join(dir, p))
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		if fi.Mode().Perm()&0o077 != 0 {
			t.Errorf("%s has mode %v, want no group/other access", p, fi.Mode().Perm())
		}
	}
	log, _ := testLog()
	if err := checkPermissions(log, dir, false); err != nil {
		t.Fatalf("a directory the server made must pass its own check: %v", err)
	}
}

func TestLooseModesAreRefusedWithTheFix(t *testing.T) {
	skipOnWindows(t)
	dir := privateTemp(t)
	if err := os.MkdirAll(filepath.Join(dir, "pki"), 0o700); err != nil {
		t.Fatal(err)
	}
	key := filepath.Join(dir, "pki", "ca.key")
	dbf := filepath.Join(dir, "continuum.db")
	for _, f := range []string{key, dbf} {
		if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	log, logs := testLog()
	if err := checkPermissions(log, dir, false); err != nil {
		t.Fatalf("private files refused: %v", err)
	}
	for _, c := range []struct {
		path string
		mode os.FileMode
		fix  string
	}{
		{dir, 0o755, "chmod 700 " + dir},
		{dir, 0o770, "chmod 700 " + dir},
		{key, 0o640, "chmod 600 " + key},
		{key, 0o604, "chmod 600 " + key},
		{dbf, 0o644, "chmod 600 " + dbf},
		{dbf + "-wal", 0o664, "chmod 600 " + dbf + "-wal"},
	} {
		orig := os.FileMode(0o600)
		if c.path == dir {
			orig = 0o700
		}
		if c.path == dbf+"-wal" {
			if err := os.WriteFile(c.path, nil, c.mode); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.Chmod(c.path, c.mode); err != nil {
			t.Fatal(err)
		}
		err := checkPermissions(log, dir, false)
		if err == nil || !strings.Contains(err.Error(), c.fix) {
			t.Errorf("%s at %v: want a refusal telling %q, got %v", c.path, c.mode, c.fix, err)
		}
		// The escape hatch downgrades to warnings.
		logs.Reset()
		if err := checkPermissions(log, dir, true); err != nil || !strings.Contains(logs.String(), "allow-loose-permissions") {
			t.Errorf("--allow-loose-permissions: err=%v log=%q", err, logs.String())
		}
		if c.path == dbf+"-wal" {
			os.Remove(c.path)
		} else if err := os.Chmod(c.path, orig); err != nil {
			t.Fatal(err)
		}
	}
	if err := checkPermissions(log, dir, false); err != nil {
		t.Fatalf("after restoring modes: %v", err)
	}
}

func TestMissingFilesPassAndPassphraseFileIsRead(t *testing.T) {
	skipOnWindows(t)
	log, logs := testLog()
	if err := checkPermissions(log, privateTemp(t), false); err != nil {
		t.Fatal(err)
	}
	dir := privateTemp(t)
	f := filepath.Join(dir, "pass")
	os.WriteFile(f, []byte("a long enough passphrase\n"), 0o600)
	b, err := readPassphraseFile(log, f)
	if err != nil || string(b) != "a long enough passphrase" {
		t.Fatalf("%q %v", b, err)
	}
	if strings.Contains(logs.String(), "readable by every user") {
		t.Fatal("warned about a private file")
	}
	os.Chmod(f, 0o644)
	if _, err := readPassphraseFile(log, f); err != nil || !strings.Contains(logs.String(), "readable by every user") {
		t.Fatalf("expected a warning only: %v %q", err, logs.String())
	}
	os.WriteFile(f, []byte("\n"), 0o600)
	if _, err := readPassphraseFile(log, f); err == nil {
		t.Fatal("empty passphrase file accepted")
	}
	if _, err := readPassphraseFile(log, filepath.Join(dir, "nope")); err == nil {
		t.Fatal("missing file accepted")
	}
}
