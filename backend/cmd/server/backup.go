package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"continuum/internal/store"
)

// A backup is one .tar.gz file holding what the server cannot recreate:
//
//	manifest.json  what is in it, with a checksum of every file, the format version and the database schema version
//	continuum.db   a consistent copy of the database (accounts, agents, settings, workspace, audit, tombstones, ...)
//	pki/*          the certificate authority (ca.crt, ca.key): without it every enrolled agent must enrol again
//
// What is NOT in it, because it comes back by itself: what agents observe (they send a full picture when they
// reconnect). Neo4j, when used, holds the history and is backed up with Neo4j's own tools (see deploy/BACKUP.md).

const backupFormat = 1

type backupFile struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type backupManifest struct {
	Format        int          `json:"format"`
	CreatedAt     string       `json:"createdAt"`
	ServerVersion string       `json:"serverVersion"`
	SchemaVersion int          `json:"schemaVersion"`
	Files         []backupFile `json:"files"`
}

// backupCmd implements `server backup --data-dir DIR --out FILE`. Exit status: 0 done, 1 failed, 2 usage.
func backupCmd(args []string, out, errw io.Writer) int {
	fs := flag.NewFlagSet("backup", flag.ContinueOnError)
	fs.SetOutput(errw)
	dataDir := fs.String("data-dir", "./data", "the server's data directory")
	dest := fs.String("out", "", "the backup file to write (.tar.gz); it must not exist. \"-\" writes the archive to standard output")
	fs.Usage = func() { fmt.Fprintln(errw, "usage: server backup [--data-dir DIR] --out FILE") }
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 || *dest == "" {
		fs.Usage()
		return 2
	}
	m, err := makeBackup(context.Background(), *dataDir, *dest)
	if err != nil {
		fmt.Fprintf(errw, "backup: %v\n", err)
		return 1
	}
	msg := out
	if *dest == "-" {
		msg = errw // the archive is on stdout
	}
	fmt.Fprintf(msg, "Backed up %d files (database schema %d) to %s\nA backup holds the CA private key: keep it as private as the data directory.\n", len(m.Files), m.SchemaVersion, *dest)
	return 0
}

func makeBackup(ctx context.Context, dataDir, dest string) (backupManifest, error) {
	if dest == "-" {
		// To stdout, so a backup can be taken out of a container with no shell and no tar:
		//   kubectl exec deploy/continuum-server -- /server backup --out - > backup.tar.gz
		// The working files go in a private directory inside the data directory, and are removed afterwards.
		return writeBackup(ctx, dataDir, dataDir, os.Stdout)
	}
	if _, err := os.Stat(dest); err == nil {
		return backupManifest{}, fmt.Errorf("%s already exists; refusing to overwrite a backup", dest)
	}
	f, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return backupManifest{}, err
	}
	m, err := writeBackup(ctx, dataDir, filepath.Dir(dest), f)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(dest)
	}
	return m, err
}

// writeBackup builds the archive on w. Working files (the database copy) live in a temporary directory under work.
func writeBackup(ctx context.Context, dataDir, work string, w io.Writer) (backupManifest, error) {
	var m backupManifest
	tmp, err := os.MkdirTemp(work, ".backup-")
	if err != nil {
		return m, err
	}
	defer os.RemoveAll(tmp)
	dbCopy := filepath.Join(tmp, "continuum.db")
	schema, err := store.BackupDatabase(ctx, filepath.Join(dataDir, "continuum.db"), dbCopy)
	if err != nil {
		return m, err
	}
	files := map[string]string{"continuum.db": dbCopy}
	entries, err := os.ReadDir(filepath.Join(dataDir, "pki"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return m, err
	}
	for _, e := range entries {
		if e.Type().IsRegular() {
			files["pki/"+e.Name()] = filepath.Join(dataDir, "pki", e.Name())
		}
	}
	if _, ok := files["pki/ca.key"]; !ok {
		return m, fmt.Errorf("%s has no pki/ca.key: is that the data directory? (a backup without the CA would leave every agent unable to reconnect)", dataDir)
	}
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	m = backupManifest{Format: backupFormat, CreatedAt: time.Now().UTC().Format(time.RFC3339), ServerVersion: version, SchemaVersion: schema}
	for _, n := range names {
		sum, size, err := checksum(files[n])
		if err != nil {
			return m, err
		}
		m.Files = append(m.Files, backupFile{Name: n, Size: size, SHA256: sum})
	}
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	man, _ := json.MarshalIndent(m, "", "  ")
	if err := writeTar(tw, "manifest.json", man); err != nil {
		return m, err
	}
	for _, n := range names {
		data, err := os.ReadFile(files[n])
		if err != nil {
			return m, err
		}
		if err := writeTar(tw, n, data); err != nil {
			return m, err
		}
	}
	if err := tw.Close(); err != nil {
		return m, err
	}
	return m, gz.Close()
}

func writeTar(tw *tar.Writer, name string, data []byte) error {
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(data)), ModTime: time.Now()}); err != nil {
		return err
	}
	_, err := tw.Write(data)
	return err
}

func checksum(p string) (string, int64, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	return hex.EncodeToString(h.Sum(nil)), n, err
}

// restoreCmd implements `server restore --data-dir DIR --from FILE [--force]`. The server must be stopped.
func restoreCmd(args []string, out, errw io.Writer) int {
	fs := flag.NewFlagSet("restore", flag.ContinueOnError)
	fs.SetOutput(errw)
	dataDir := fs.String("data-dir", "./data", "the data directory to restore into")
	from := fs.String("from", "", "the backup file made by `server backup` (\"-\" reads it from standard input)")
	force := fs.Bool("force", false, "replace an existing database and CA (they are moved aside into <data-dir>/pre-restore-<time>, not deleted)")
	fs.Usage = func() {
		fmt.Fprintln(errw, "usage: server restore [--data-dir DIR] --from FILE [--force]   (stop the server first)")
	}
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 || *from == "" {
		fs.Usage()
		return 2
	}
	m, err := restoreBackup(context.Background(), *from, *dataDir, *force)
	if err != nil {
		fmt.Fprintf(errw, "restore: %v\n", err)
		return 1
	}
	fmt.Fprintf(out, "Restored %d files (database schema %d, backed up %s) into %s. Start the server; agents reconnect on their own and send a fresh picture.\n", len(m.Files), m.SchemaVersion, m.CreatedAt, *dataDir)
	return 0
}

const maxBackupFile = 4 << 30

func restoreBackup(ctx context.Context, from, dataDir string, force bool) (backupManifest, error) {
	var m backupManifest
	var f io.Reader = os.Stdin // "-": the archive is piped in, as `kubectl run -i` does in a pod that has no shell
	if from != "-" {
		file, err := os.Open(from)
		if err != nil {
			return m, err
		}
		defer file.Close()
		f = file
	}
	gz, err := gzip.NewReader(f)
	if err != nil {
		return m, fmt.Errorf("%s is not a backup made by `server backup`: %w", from, err)
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return m, err
	}
	stage, err := os.MkdirTemp(dataDir, ".restore-")
	if err != nil {
		return m, err
	}
	defer os.RemoveAll(stage)

	// Unpack into a staging directory, accepting only the names a backup contains (no paths that lead elsewhere).
	tr := tar.NewReader(gz)
	got := map[string]bool{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return m, fmt.Errorf("the backup is damaged: %w", err)
		}
		name := path.Clean(h.Name)
		if h.Typeflag != tar.TypeReg || !(name == "manifest.json" || name == "continuum.db" || (path.Dir(name) == "pki" && path.Base(name) != "." && !strings.HasPrefix(path.Base(name), "."))) {
			return m, fmt.Errorf("the backup contains an unexpected entry %q", h.Name)
		}
		dst := filepath.Join(stage, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return m, err
		}
		out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return m, err
		}
		_, err = io.Copy(out, io.LimitReader(tr, maxBackupFile))
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return m, err
		}
		got[name] = true
	}
	raw, err := os.ReadFile(filepath.Join(stage, "manifest.json"))
	if err != nil {
		return m, errors.New("the backup has no manifest.json: it is not a backup made by `server backup`")
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return m, fmt.Errorf("the manifest is unreadable: %w", err)
	}
	if m.Format > backupFormat {
		return m, fmt.Errorf("this backup has format %d, but this server only reads up to format %d: it was made by a newer Continuum. Restore it with that version", m.Format, backupFormat)
	}
	// Every listed file is present and unchanged; nothing unlisted is present.
	listed := map[string]bool{}
	for _, bf := range m.Files {
		listed[bf.Name] = true
		if !got[bf.Name] {
			return m, fmt.Errorf("the backup is missing %s", bf.Name)
		}
		sum, size, err := checksum(filepath.Join(stage, filepath.FromSlash(bf.Name)))
		if err != nil {
			return m, err
		}
		if sum != bf.SHA256 || size != bf.Size {
			return m, fmt.Errorf("%s does not match its checksum: the backup file is damaged or was changed", bf.Name)
		}
	}
	for n := range got {
		if n != "manifest.json" && !listed[n] {
			return m, fmt.Errorf("the backup contains %s, which its manifest does not list", n)
		}
	}
	if !listed["continuum.db"] || !listed["pki/ca.key"] || !listed["pki/ca.crt"] {
		return m, errors.New("the backup lacks the database or the CA")
	}
	if _, err := store.CheckDatabase(ctx, filepath.Join(stage, "continuum.db")); err != nil {
		return m, err // damaged, or written by a newer server (the message says so)
	}

	// Put it in place. Anything already there is moved aside, never deleted.
	existing := []string{}
	for _, n := range []string{"continuum.db", "continuum.db-wal", "continuum.db-shm", "pki"} {
		if _, err := os.Lstat(filepath.Join(dataDir, n)); err == nil {
			existing = append(existing, n)
		}
	}
	if len(existing) > 0 && !force {
		return m, fmt.Errorf("%s already holds %s. Restoring over it would replace it; run with --force to move it aside into pre-restore-<time> and restore", dataDir, strings.Join(existing, ", "))
	}
	if len(existing) > 0 {
		aside := filepath.Join(dataDir, "pre-restore-"+time.Now().UTC().Format("20060102T150405Z"))
		if err := os.Mkdir(aside, 0o700); err != nil {
			return m, err
		}
		for _, n := range existing {
			if err := os.Rename(filepath.Join(dataDir, n), filepath.Join(aside, n)); err != nil {
				return m, err
			}
		}
	}
	if err := os.Rename(filepath.Join(stage, "pki"), filepath.Join(dataDir, "pki")); err != nil {
		return m, err
	}
	if err := os.Rename(filepath.Join(stage, "continuum.db"), filepath.Join(dataDir, "continuum.db")); err != nil {
		return m, err
	}
	_ = os.Chmod(filepath.Join(dataDir, "pki"), 0o700)
	return m, nil
}
