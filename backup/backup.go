// Package backup takes point-in-time SQLite backups of Polaris's
// database (via SQLite's own VACUUM INTO — a consistent single-file
// snapshot that doesn't block concurrent readers/writers), prunes
// anything past a configured retention window, and restores one back
// into place. Optionally mirrors each backup off-device to Cloudflare R2
// (Mirror/PruneRemote/Fetch, via the r2 package) so a backup survives the
// device itself failing, not just a bad database state. See cmd/backup.go
// for the `polaris backup` CLI and gateway/backup.go for the Docker-mode
// REST endpoints it talks to.
package backup

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"polaris/logger"
	"polaris/r2"
)

var log = logger.WithPrefix("backup")

const (
	filePrefix = "polaris-"
	fileExt    = ".db"
	nameLayout = "20060102-150405"
)

// DefaultRetentionDays matches config.Config's own default (see
// config.Load) — duplicated here too so a caller that skips config
// entirely (tests, or a future standalone use) still gets the same
// 30-day default without importing config, which would create an
// import cycle (config imports backup for its own default Dir/
// RetentionDays, not the other way around).
const DefaultRetentionDays = 30

// minInterval is how old the newest existing backup must be before
// RunScheduler takes another one — see its doc comment.
const minInterval = 24 * time.Hour

// Info describes one backup file.
type Info struct {
	Name      string    `json:"name"`
	Path      string    `json:"path"`
	SizeBytes int64     `json:"size_bytes"`
	CreatedAt time.Time `json:"created_at"`
}

// Dir is where backups for a database at dbPath live: a "backups"
// subdirectory right next to the database file itself. Under Docker
// this naturally lands inside the polaris-data named volume (the
// database already lives at /data/polaris.db there — see
// compose/polaris/config.yaml.example) with no extra bind mount or
// Dockerfile COPY needed — unlike prompt.md/blocked_sources.txt/etc,
// this is runtime-generated state, not a hand-editable resource, so
// CLAUDE.md's two-sided-sync checklist doesn't apply to it.
func Dir(dbPath string) string {
	return filepath.Join(filepath.Dir(dbPath), "backups")
}

func fileName(t time.Time) string {
	return filePrefix + t.UTC().Format(nameLayout) + fileExt
}

// parseTime recovers the timestamp encoded in a backup's own filename,
// rather than trusting the file's mtime — mtime wouldn't survive a
// copy/rsync, or a round trip through an
// object store that doesn't preserve it.
func parseTime(name string) (time.Time, bool) {
	if !strings.HasPrefix(name, filePrefix) || !strings.HasSuffix(name, fileExt) {
		return time.Time{}, false
	}
	stamp := strings.TrimSuffix(strings.TrimPrefix(name, filePrefix), fileExt)
	t, err := time.Parse(nameLayout, stamp)
	if err != nil {
		return time.Time{}, false
	}
	return t.UTC(), true
}

// Create takes a fresh backup of the database at dbPath into dir,
// returning info about the new file.
//
// This opens its own short-lived connection rather than reusing
// store.Store's — store.Open caps its pool at a single connection (see
// that function's doc comment) so every chat-turn write serializes
// behind whatever's using it, and VACUUM INTO rewriting the whole
// database can take real time on the potato's weak CPU. Stalling every
// other DB operation for that whole duration just to take a backup
// would be a regression. WAL mode lets this separate connection run
// alongside the app's live writer without blocking either side, and
// VACUUM INTO itself takes a consistent snapshot as of when it starts,
// so the backup is never a half-written mix of before/after a
// concurrent write — confirmed live against the pure-Go sqlite driver
// this project uses (modernc.org/sqlite), not just assumed from
// upstream SQLite's own docs.
func Create(dbPath, dir string) (Info, error) {
	// sql.Open + a write statement against a path that doesn't exist yet
	// silently creates an empty, schema-less SQLite file there (confirmed
	// live) — without this check, `polaris backup create` run before the
	// server has ever started once would fabricate a phantom polaris.db
	// out of thin air and "successfully" back up nothing. A backup
	// command should error, not invent data.
	if _, err := os.Stat(dbPath); err != nil {
		if os.IsNotExist(err) {
			return Info{}, fmt.Errorf("no database at %s yet — start polaris at least once first", dbPath)
		}
		return Info{}, fmt.Errorf("checking database: %w", err)
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Info{}, fmt.Errorf("creating backup directory: %w", err)
	}

	now := time.Now()
	name := fileName(now)
	dest := filepath.Join(dir, name)

	db, err := sql.Open("sqlite", dbPath+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return Info{}, fmt.Errorf("opening database: %w", err)
	}
	defer db.Close()

	if _, err := db.Exec("VACUUM INTO ?", dest); err != nil {
		os.Remove(dest) // don't leave a partial file behind on failure
		return Info{}, fmt.Errorf("backing up database: %w", err)
	}

	fi, err := os.Stat(dest)
	if err != nil {
		return Info{}, fmt.Errorf("stat'ing new backup: %w", err)
	}
	return Info{Name: name, Path: dest, SizeBytes: fi.Size(), CreatedAt: now.UTC()}, nil
}

// List returns every backup in dir, newest first. A missing dir is not
// an error (nothing's been backed up yet) — it just returns an empty
// list. Anything not matching polaris-YYYYMMDD-HHMMSS.db is silently
// skipped, same convention as logger's own pruneOld: a backups
// directory is assumed to hold only what this package itself wrote
// there.
func List(dir string) ([]Info, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading backup directory: %w", err)
	}

	var infos []Info
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		t, ok := parseTime(e.Name())
		if !ok {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		infos = append(infos, Info{
			Name:      e.Name(),
			Path:      filepath.Join(dir, e.Name()),
			SizeBytes: fi.Size(),
			CreatedAt: t,
		})
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].CreatedAt.After(infos[j].CreatedAt) })
	return infos, nil
}

// Prune deletes every backup in dir older than retain, returning the
// names it removed. Best-effort past the first failure: it returns
// immediately on an error rather than trying the rest, since a
// filesystem error on one file (permissions, a busy handle) is likely
// to repeat on every subsequent one too.
func Prune(dir string, retain time.Duration) ([]string, error) {
	infos, err := List(dir)
	if err != nil {
		return nil, err
	}
	cutoff := time.Now().Add(-retain)
	var deleted []string
	for _, info := range infos {
		if info.CreatedAt.Before(cutoff) {
			if err := os.Remove(info.Path); err != nil {
				return deleted, fmt.Errorf("removing expired backup %s: %w", info.Name, err)
			}
			deleted = append(deleted, info.Name)
		}
	}
	return deleted, nil
}

// RunScheduler backs up dbPath into dir roughly once a day, pruning
// anything older than retain each time, until done is closed. Meant to
// run as a background goroutine for the life of the server process —
// see cmd/run.go.
//
// It checks hourly rather than sleeping for a fixed 24h from process
// start, and only actually acts once the newest existing backup (if
// any) is at least minInterval old. That makes "daily" mean daily in
// practice even though this process restarts far more often than that
// — a self-update or plain `polaris restart` shouldn't create a fresh
// backup every time just because it happened to relaunch the scheduler,
// but a genuinely missed day should still catch up within the hour
// rather than waiting for a full 24h-since-this-launch timer that a
// frequently-restarted install might never reach.
//
// r2Client is optional (nil disables it entirely, see r2.NewClient) — when
// set, every backup this scheduler creates is also mirrored to R2 (Mirror)
// and R2 is pruned to the same retention window (PruneRemote). A mirror or
// remote-prune failure only logs a warning; it never blocks or undoes the
// local backup, since the whole point is that local backups keep working
// unchanged whether or not R2 is reachable.
func RunScheduler(done <-chan struct{}, dbPath, dir string, retain time.Duration, r2Client *r2.Client) {
	runOnce := func() {
		infos, err := List(dir)
		if err != nil {
			log.Warn("listing existing backups failed, skipping scheduled backup", "err", err)
			return
		}
		if len(infos) > 0 && time.Since(infos[0].CreatedAt) < minInterval {
			return
		}
		if info, err := Create(dbPath, dir); err != nil {
			log.Warn("scheduled backup failed", "err", err)
		} else {
			log.Info("created scheduled backup", "name", info.Name, "size_bytes", info.SizeBytes)
			if err := Mirror(info, r2Client); err != nil {
				log.Warn("mirroring backup to r2 failed", "name", info.Name, "err", err)
			} else if r2Client != nil {
				log.Info("mirrored backup to r2", "name", info.Name)
			}
		}
		if deleted, err := Prune(dir, retain); err != nil {
			log.Warn("pruning old backups failed", "err", err)
		} else if len(deleted) > 0 {
			log.Info("pruned expired backups", "count", len(deleted))
		}
		if deleted, err := PruneRemote(r2Client, retain); err != nil {
			log.Warn("pruning expired r2 backups failed", "err", err)
		} else if len(deleted) > 0 {
			log.Info("pruned expired r2 backups", "count", len(deleted))
		}
	}

	runOnce()
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			runOnce()
		case <-done:
			return
		}
	}
}

// Mirror uploads a backup to R2 under its own filename as the object key —
// the same name List/Prune already use locally — so PruneRemote can parse
// each object's timestamp straight from its key (parseTime) without R2
// needing any separate metadata to track it. A nil r2Client (R2 not
// configured) is a no-op, not an error, so every call site can call this
// unconditionally rather than branching on whether R2 is set up.
func Mirror(info Info, r2Client *r2.Client) error {
	if r2Client == nil {
		return nil
	}
	if err := r2Client.Upload(context.Background(), info.Name, info.Path); err != nil {
		return fmt.Errorf("uploading %s to r2: %w", info.Name, err)
	}
	return nil
}

// PruneRemote deletes every R2 object past retain, mirroring Prune's local
// retention policy so R2 doesn't accumulate forever once mirroring is on.
// A nil r2Client is a no-op, matching Mirror.
func PruneRemote(r2Client *r2.Client, retain time.Duration) ([]string, error) {
	if r2Client == nil {
		return nil, nil
	}
	objects, err := r2Client.List(context.Background())
	if err != nil {
		return nil, fmt.Errorf("listing r2 backups: %w", err)
	}
	cutoff := time.Now().Add(-retain)
	var deleted []string
	for _, obj := range objects {
		t, ok := parseTime(obj.Key)
		if !ok {
			// Not one of this package's own backup filenames — e.g. an
			// object a human put in the bucket by hand. Same "assume the
			// directory/bucket holds only what we wrote" convention as
			// List's local equivalent: skip it rather than risk deleting
			// something this feature doesn't own.
			continue
		}
		if t.Before(cutoff) {
			if err := r2Client.Delete(context.Background(), obj.Key); err != nil {
				return deleted, fmt.Errorf("removing expired r2 backup %s: %w", obj.Key, err)
			}
			deleted = append(deleted, obj.Key)
		}
	}
	return deleted, nil
}

// Fetch downloads a backup object from R2 into dir (creating it if
// needed), for disaster recovery when the local backups directory itself
// is gone — the exact scenario R2 mirroring exists to protect against. See
// cmd/backup.go's `restore-remote` command, which calls this and then
// Restore with the result.
func Fetch(r2Client *r2.Client, name, dir string) (Info, error) {
	if r2Client == nil {
		return Info{}, fmt.Errorf("r2 is not configured")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Info{}, fmt.Errorf("creating backup directory: %w", err)
	}
	dest := filepath.Join(dir, name)
	if err := r2Client.Download(context.Background(), name, dest); err != nil {
		return Info{}, fmt.Errorf("downloading %s from r2: %w", name, err)
	}
	fi, err := os.Stat(dest)
	if err != nil {
		return Info{}, fmt.Errorf("stat'ing downloaded backup: %w", err)
	}
	t, _ := parseTime(name)
	return Info{Name: name, Path: dest, SizeBytes: fi.Size(), CreatedAt: t}, nil
}

// Restore replaces the live database at dbPath with the contents of
// backupPath, after verifying the backup opens cleanly and passes
// SQLite's own integrity check. Whatever's currently at dbPath (if
// anything) is preserved, not deleted — copied alongside it first as
// "<dbPath>.pre-restore-<timestamp>" — so a restore is itself always
// undoable. Returns that safety copy's path, or "" if there was nothing
// at dbPath to preserve.
//
// Callers are responsible for making sure nothing else has dbPath open
// while this runs — swapping the file out from under a live SQLite
// connection risks corrupting whichever half-finished write it's mid-way
// through. See cmd/backup.go's restore command for the healthz-based
// check this uses on bare-metal, and the refusal it prints under Docker
// instead, where a live container might hold the file open in a way
// this process has no way to see at all.
func Restore(dbPath, backupPath string) (safetyCopyPath string, err error) {
	if err := verify(backupPath); err != nil {
		return "", fmt.Errorf("backup file failed verification, not restoring: %w", err)
	}

	if _, statErr := os.Stat(dbPath); statErr == nil {
		safetyCopyPath = fmt.Sprintf("%s.pre-restore-%s", dbPath, time.Now().UTC().Format(nameLayout))
		if err := copyFile(dbPath, safetyCopyPath); err != nil {
			return "", fmt.Errorf("safety-copying the current database before restoring: %w", err)
		}
	} else if !os.IsNotExist(statErr) {
		return "", fmt.Errorf("checking current database: %w", statErr)
	}

	// Copy into a temp file in the same directory, then rename over the
	// live path — rename within one directory is atomic on both Linux
	// and macOS, so the live path is never observably a half-written
	// file even if this process is killed mid-copy.
	tmp := dbPath + ".restoring"
	if err := copyFile(backupPath, tmp); err != nil {
		os.Remove(tmp)
		return safetyCopyPath, fmt.Errorf("copying backup into place: %w", err)
	}
	if err := os.Rename(tmp, dbPath); err != nil {
		return safetyCopyPath, fmt.Errorf("finalizing restore: %w", err)
	}

	// A stale -wal/-shm left over from the database that used to be at
	// dbPath describes that old file's page layout, not the one that
	// just landed — leaving them in place would make the next open
	// replay the wrong WAL frames against the restored file. VACUUM INTO
	// backups never carry their own sidecars (confirmed live — see
	// backup_test.go), so there's nothing of the backup's own to bring
	// across.
	for _, suffix := range []string{"-wal", "-shm"} {
		_ = os.Remove(dbPath + suffix)
	}

	return safetyCopyPath, nil
}

func verify(path string) error {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return fmt.Errorf("opening: %w", err)
	}
	defer db.Close()
	var result string
	if err := db.QueryRow("PRAGMA integrity_check").Scan(&result); err != nil {
		return fmt.Errorf("running integrity check: %w", err)
	}
	if result != "ok" {
		return fmt.Errorf("integrity check reported: %s", result)
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}
