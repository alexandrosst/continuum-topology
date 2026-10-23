package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"

	"ikhnos/internal/sealing"
	"ikhnos/internal/server"
	"ikhnos/internal/store"
)

// loadSealer reads the sealing key file: one key per line, the first seals. No path is no sealing, and says so; a path that cannot be read is an
// error, never a silent fall back to storing secrets in the clear.
func loadSealer(log *slog.Logger, path string) (*sealing.Sealer, error) {
	if path == "" {
		return nil, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("--sealing-key-file: %w", err)
	}
	if runtime.GOOS != "windows" {
		if st, err := os.Stat(path); err == nil && st.Mode().Perm()&0o007 != 0 {
			log.Warn("the sealing key file is readable by every user on this machine; restrict it (chmod 600)", "file", path, "mode", fmt.Sprintf("%04o", st.Mode().Perm()))
		}
	}
	keys, err := sealing.ParseKeys(b)
	if err != nil {
		return nil, fmt.Errorf("--sealing-key-file: %w", err)
	}
	sl, err := sealing.New(keys...)
	if err != nil {
		return nil, fmt.Errorf("--sealing-key-file: %w", err)
	}
	return sl, nil
}

// openSealedStore opens the database for a command that reads accounts (reset-password, create-org): with the key when it is given, since a user's
// second-factor secret cannot be read without it.
func openSealedStore(ctx context.Context, log *slog.Logger, dataDir, keyFile string) (*store.SQLite, error) {
	sl, err := loadSealer(log, keyFile)
	if err != nil {
		return nil, err
	}
	st, err := store.OpenSQLite(filepath.Join(dataDir, "ikhnos.db"))
	if err != nil {
		return nil, err
	}
	if err := st.UseSealer(ctx, sl); err != nil {
		st.Close()
		return nil, err
	}
	return st, nil
}

// resetSealing is the way out of a lost sealing key. The secrets sealed under it cannot be read again by anyone; this removes them (second
// factors off, the mail password and the deciders' secrets cleared) and lets the server start without a key, or with a new one, from there.
// It does nothing without --yes, and says first what it would remove.
func resetSealing(args []string) {
	f := flag.NewFlagSet("reset-sealing", flag.ExitOnError)
	dataDir := f.String("data-dir", "./data", "the server's data directory")
	yes := f.Bool("yes", false, "do it; without it, only say what would be removed")
	f.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: server reset-sealing [--data-dir DIR] [--yes]\nOnly for a sealing key that is lost. With the key, nothing here is needed: the server opens its secrets and, with a new key at the top of the file, seals them again.")
	}
	_ = f.Parse(args)
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	st, err := store.OpenSQLite(filepath.Join(*dataDir, "ikhnos.db"))
	if err != nil {
		fatal(log, err)
	}
	defer st.Close()
	if err := discardSealed(context.Background(), log, st, *yes, os.Stdout); err != nil {
		fatal(log, err)
	}
}

func discardSealed(ctx context.Context, log *slog.Logger, st *store.SQLite, yes bool, out interface{ Write([]byte) (int, error) }) error {
	check, err := st.GetSettings(ctx, "_sealing.check")
	if err != nil {
		return err
	}
	if check == nil {
		fmt.Fprintln(out, "This database holds no sealed secrets; nothing to do.")
		return nil
	}
	if !yes {
		fmt.Fprintf(out, "This database was sealed under key %s. Without that key, running again with --yes will remove the secrets sealed under it:\n  the second factor of every user who has one (they set it up again),\n  the mail password (it is entered again in Settings),\n  the external decider of every organisation that has one (it is set up again).\nNothing else is removed. If the key can be found, find it instead.\n", sealing.SealedUnder(string(check)))
		return nil
	}
	core := server.NewCore(st, nil, "", log)
	names, err := st.DiscardSealedTOTP(ctx)
	if err != nil {
		return err
	}
	mail, err := core.Accounts.DiscardSealedMail(ctx)
	if err != nil {
		return err
	}
	orgs, err := st.ListOrgs(ctx)
	if err != nil {
		return err
	}
	var deciders []string
	for _, o := range orgs {
		cleared, err := core.ForOrg(o.ID).DiscardSealedSettings(ctx)
		if err != nil {
			return err
		}
		if cleared {
			deciders = append(deciders, o.ID)
		}
	}
	// Last: an interrupted run leaves a database that still asks for its key.
	if err := st.DropSealingCheck(ctx); err != nil {
		return err
	}
	fmt.Fprintf(out, "Second factor removed for %d user(s): %v\nMail password removed: %v\nDecider removed for organisation(s): %v\nThe database no longer asks for a sealing key.\n", len(names), names, mail, deciders)
	return nil
}
