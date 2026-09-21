package main

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// The data directory holds the CA key, the database (accounts, password hashes, session and enrollment
// tokens) and the audit trail. Anyone who can read them can impersonate the server or its users, so the
// server refuses to run when they are open to other local users, and says how to fix it.
//
// Files and directories the server creates itself are private from the start (0700 / 0600). Ones that
// already exist are only checked, never silently changed: an operator may have set them up that way on
// purpose, and changing modes behind their back is how backups and sidecars break.

// permProblems lists the paths under dataDir that are readable or writable by group or others. It is empty
// on Windows, where Unix modes do not describe access.
func permProblems(dataDir string) []string {
	if runtime.GOOS == "windows" {
		return nil
	}
	type item struct {
		path string
		dir  bool
	}
	items := []item{
		{dataDir, true},
		{filepath.Join(dataDir, "pki"), true},
		{filepath.Join(dataDir, "pki", "ca.key"), false},
	}
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		items = append(items, item{filepath.Join(dataDir, "continuum.db"+suffix), false})
	}
	var out []string
	for _, it := range items {
		st, err := os.Stat(it.path)
		if err != nil {
			continue // absent: the server will create it private
		}
		if it.dir != st.IsDir() {
			continue
		}
		if m := st.Mode().Perm(); m&0o077 != 0 {
			want := "600"
			if it.dir {
				want = "700"
			}
			out = append(out, fmt.Sprintf("%s has mode %04o, readable or writable by other users; run: chmod %s %s", it.path, m, want, it.path))
		}
	}
	sort.Strings(out)
	return out
}

// checkPermissions refuses to continue when permProblems finds any, unless allowLoose is set, in which case
// it logs them as warnings.
func checkPermissions(log *slog.Logger, dataDir string, allowLoose bool) error {
	probs := permProblems(dataDir)
	if len(probs) == 0 {
		return nil
	}
	if allowLoose {
		for _, p := range probs {
			log.Warn("permissive file mode accepted because --allow-loose-permissions is set", "problem", p)
		}
		return nil
	}
	return fmt.Errorf("the data directory is not private enough:\n  %s\nThe CA key and the database let whoever reads them act as this server. Fix the modes as shown. "+
		"If a platform sets them (a Kubernetes fsGroup volume, for example) and only this pod's user can reach the volume, start with --allow-loose-permissions (env CONTINUUM_ALLOW_LOOSE_PERMISSIONS=true) to accept them",
		strings.Join(probs, "\n  "))
}

// readPassphraseFile reads the CA key passphrase: the file's content without its trailing newline. It
// warns, without refusing, when the file is readable by other users.
func readPassphraseFile(log *slog.Logger, path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("--ca-key-passphrase-file: %w", err)
	}
	if runtime.GOOS != "windows" {
		if st, err := os.Stat(path); err == nil && st.Mode().Perm()&0o007 != 0 {
			log.Warn("the CA key passphrase file is readable by every user on this machine; restrict it (chmod 600)", "file", path, "mode", fmt.Sprintf("%04o", st.Mode().Perm()))
		}
	}
	b = []byte(strings.TrimSuffix(strings.TrimSuffix(string(b), "\n"), "\r"))
	if len(b) == 0 {
		return nil, errors.New("--ca-key-passphrase-file: the file is empty")
	}
	return b, nil
}

// ensurePrivateDir creates dir (and parents) private when it does not exist yet.
func ensurePrivateDir(dir string) error {
	if _, err := os.Stat(dir); errors.Is(err, fs.ErrNotExist) {
		return os.MkdirAll(dir, 0o700)
	}
	return nil
}
