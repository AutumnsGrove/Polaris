package cmd

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"polaris/backup"
	"polaris/config"
	"polaris/models"
	"polaris/r2"
)

var backupCmd = &cobra.Command{
	Use:   "backup",
	Short: "Create, list, and restore database backups",
}

var backupRestoreYes bool
var backupListRemote bool

var backupCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Take a backup right now, outside the daily schedule",
	RunE:  runBackupCreate,
}

var backupListCmd = &cobra.Command{
	Use:   "list",
	Short: "List existing backups, newest first",
	RunE:  runBackupList,
}

var backupRestoreRemoteCmd = &cobra.Command{
	Use:   "restore-remote <name>",
	Short: "Download a backup from R2 and restore it",
	Long: `Disaster recovery for when the local backups/ folder itself is gone
along with the rest of the device — not just "restore an earlier state",
which ` + "`polaris backup restore`" + ` already covers when local backups still
exist. Downloads the named backup from R2 into backup.dir first (see
` + "`polaris backup list --remote`" + ` for names), then runs the exact same
verify/preserve/swap sequence ` + "`restore`" + ` does. Requires r2.* to be
configured in config.yaml on this (new) device — see config.yaml.example.`,
	Args: cobra.ExactArgs(1),
	RunE: runBackupRestoreRemote,
}

var backupRestoreCmd = &cobra.Command{
	Use:   "restore <name>",
	Short: "Restore the database from a backup",
	Long: `Replaces the live database with the contents of a backup (see
` + "`polaris backup list`" + ` for names). The database currently in place is
never discarded — it's copied alongside itself first as
"<path>.pre-restore-<timestamp>", so a restore is always itself undoable.

The server must not be running while this happens: swapping the database
file out from under a live connection risks corrupting whichever write
it's mid-way through. Bare-metal refuses if it detects polaris still
answering on its configured port; under Docker (detected the same way
every other command here does, from docker-compose.yml's presence) this
can't be checked the same way, so it refuses outright and prints the
exact commands to run instead.`,
	Args: cobra.ExactArgs(1),
	RunE: runBackupRestore,
}

func init() {
	rootCmd.AddCommand(backupCmd)
	backupCmd.AddCommand(backupCreateCmd, backupListCmd, backupRestoreCmd, backupRestoreRemoteCmd)

	backupCmd.PersistentFlags().StringVar(&configPath, "config", "config.yaml", "path to config.yaml (bare-metal only — a Docker install reaches the running container instead, except for restore)")
	backupRestoreCmd.Flags().BoolVarP(&backupRestoreYes, "yes", "y", false, "skip the confirmation prompt")
	backupRestoreRemoteCmd.Flags().BoolVarP(&backupRestoreYes, "yes", "y", false, "skip the confirmation prompt")
	backupListCmd.Flags().BoolVar(&backupListRemote, "remote", false, "list what's actually in R2 instead of the local backups/ folder")
}

func runBackupCreate(cmd *cobra.Command, args []string) error {
	if repoPath, err := os.Getwd(); err == nil && isDockerComposeInstall(repoPath) {
		return runDockerBackupCreate()
	}

	cfg, err := config.Load(configPath, models.Registry)
	if err != nil {
		return err
	}
	info, err := backup.Create(cfg.Database.Path, cfg.Backup.Dir)
	if err != nil {
		return err
	}
	fmt.Printf("created backup %s (%s)\n", info.Name, humanSize(info.SizeBytes))

	if err := backup.Mirror(info, cfg.R2Client()); err != nil {
		// Non-fatal: the local backup already succeeded and is what
		// actually matters for this command's exit status — R2 is an
		// off-device copy of it, not a replacement for it.
		fmt.Printf("warning: mirroring to r2 failed: %v\n", err)
	} else if cfg.R2Client() != nil {
		fmt.Println("mirrored to r2")
	}
	return nil
}

func runBackupList(cmd *cobra.Command, args []string) error {
	if repoPath, err := os.Getwd(); err == nil && isDockerComposeInstall(repoPath) {
		if backupListRemote {
			return runDockerBackupListRemote()
		}
		return runDockerBackupList()
	}

	cfg, err := config.Load(configPath, models.Registry)
	if err != nil {
		return err
	}

	if backupListRemote {
		client := cfg.R2Client()
		if client == nil {
			return fmt.Errorf("r2 is not configured — set r2.* in %s first (see config.yaml.example)", configPath)
		}
		objects, err := client.List(cmd.Context())
		if err != nil {
			return err
		}
		printRemoteBackupList(objects)
		return nil
	}

	infos, err := backup.List(cfg.Backup.Dir)
	if err != nil {
		return err
	}
	printBackupList(infos)
	return nil
}

func runBackupRestoreRemote(cmd *cobra.Command, args []string) error {
	name := args[0]

	// Same reasoning as runBackupRestore's Docker branch: this CLI has no
	// direct filesystem access to the container's data volume from the
	// host, so it can't swap the database file in place itself. Unlike
	// plain restore, restore-remote *could* in principle run for real
	// inside the one-off container dockerRestoreInstructionsRemote
	// describes below — it has both R2 config (bind-mounted) and direct
	// volume access — so the printed instructions point at that instead
	// of just refusing outright.
	if repoPath, err := os.Getwd(); err == nil && isDockerComposeInstall(repoPath) {
		fmt.Println(dockerRestoreInstructionsRemote(name))
		return nil
	}

	cfg, err := config.Load(configPath, models.Registry)
	if err != nil {
		return err
	}
	client := cfg.R2Client()
	if client == nil {
		return fmt.Errorf("r2 is not configured — set r2.* in %s first (see config.yaml.example)", configPath)
	}

	if serverAppearsRunning(cfg.Server.Port) {
		return fmt.Errorf("polaris appears to be running on port %d — stop it first (e.g. `systemctl stop %s`, or your service manager) before restoring", cfg.Server.Port, cfg.Service.Label)
	}

	fmt.Printf("downloading %s from r2...\n", name)
	info, err := backup.Fetch(client, name, cfg.Backup.Dir)
	if err != nil {
		return err
	}
	fmt.Printf("downloaded %s (%s)\n", info.Name, humanSize(info.SizeBytes))

	if !backupRestoreYes {
		fmt.Printf("This will replace %s with the contents of %s.\n", cfg.Database.Path, name)
		fmt.Print("The current database will be preserved alongside it first, if one exists. Continue? [y/N] ")
		reader := bufio.NewReader(os.Stdin)
		answer, _ := reader.ReadString('\n')
		if strings.ToLower(strings.TrimSpace(answer)) != "y" {
			fmt.Println("aborted")
			return nil
		}
	}

	safetyCopy, err := backup.Restore(cfg.Database.Path, info.Path)
	if err != nil {
		return err
	}
	fmt.Printf("restored %s from %s\n", cfg.Database.Path, name)
	if safetyCopy != "" {
		fmt.Printf("previous database preserved at %s\n", safetyCopy)
	}
	fmt.Println("start the service to continue")
	return nil
}

func runBackupRestore(cmd *cobra.Command, args []string) error {
	name := args[0]

	// Docker mode: this CLI runs on the host, outside any container, and
	// has no direct filesystem access to the polaris-data volume the
	// real database lives in — there's no live-API-based restore either
	// (see backup.Restore's doc comment on why swapping the file under a
	// running connection is unsafe), so unlike create/list this can't be
	// automated as a plain HTTP call. Print the exact manual sequence
	// instead of doing something halfway.
	if repoPath, err := os.Getwd(); err == nil && isDockerComposeInstall(repoPath) {
		fmt.Println(dockerRestoreInstructions(name))
		return nil
	}

	cfg, err := config.Load(configPath, models.Registry)
	if err != nil {
		return err
	}

	if serverAppearsRunning(cfg.Server.Port) {
		return fmt.Errorf("polaris appears to be running on port %d — stop it first (e.g. `systemctl stop %s`, or your service manager) before restoring", cfg.Server.Port, cfg.Service.Label)
	}

	backupPath := filepath.Join(cfg.Backup.Dir, name)
	if _, err := os.Stat(backupPath); err != nil {
		return fmt.Errorf("backup %q not found in %s: %w", name, cfg.Backup.Dir, err)
	}

	if !backupRestoreYes {
		fmt.Printf("This will replace %s with the contents of %s.\n", cfg.Database.Path, name)
		fmt.Print("The current database will be preserved alongside it first. Continue? [y/N] ")
		reader := bufio.NewReader(os.Stdin)
		answer, _ := reader.ReadString('\n')
		if strings.ToLower(strings.TrimSpace(answer)) != "y" {
			fmt.Println("aborted")
			return nil
		}
	}

	safetyCopy, err := backup.Restore(cfg.Database.Path, backupPath)
	if err != nil {
		return err
	}
	fmt.Printf("restored %s from %s\n", cfg.Database.Path, name)
	if safetyCopy != "" {
		fmt.Printf("previous database preserved at %s\n", safetyCopy)
	}
	fmt.Println("start the service to continue")
	return nil
}

// serverAppearsRunning is restore's best-effort safety check on
// bare-metal: a real /healthz response means something almost certainly
// still has the database open, so refuse rather than risk corrupting a
// live file. Not foolproof (a server hung between accepting the
// connection and answering wouldn't be caught, nor would a process that
// has the file open but isn't listening on this port at all) — it's a
// guard against the ordinary "forgot to stop the service" mistake, not a
// distributed lock.
func serverAppearsRunning(port int) bool {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/healthz", port))
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// dockerRestoreInstructions prints the manual sequence for restoring
// under Docker: stop the running service, restore inside a fresh
// one-off container that mounts the exact same data volume (so it has
// direct file access without ever giving the *running* container
// control over Docker — see docker-compose.yml's comment on why that
// boundary matters), then bring the service back up.
func dockerRestoreInstructions(name string) string {
	return fmt.Sprintf(`This is a Docker install — restoring needs the polaris container stopped
first, so nothing else has the database open while the file is swapped:

  docker compose stop polaris
  docker compose run --rm --no-deps polaris backup restore %s --config /data/config.yaml --yes
  docker compose up -d polaris

The middle command runs this exact restore logic inside a fresh, one-off
container sharing the same data volume — it has no server listening on
its own port, so the usual bare-metal healthz safety check is skipped
there too; that's why stopping the real service first (the first line)
is what actually matters, not the check.`, name)
}

// dockerRestoreInstructionsRemote mirrors dockerRestoreInstructions, but
// for restore-remote: the one-off container it describes runs with the
// same bind-mounted config.yaml (see docker-compose.yml) as the real
// service, so it has R2 credentials and direct volume access — unlike
// plain restore, this actually can run for real there, not just print
// what to do.
func dockerRestoreInstructionsRemote(name string) string {
	return fmt.Sprintf(`This is a Docker install — restoring from R2 needs the polaris container
stopped first, so nothing else has the database open while the file is
swapped:

  docker compose stop polaris
  docker compose run --rm --no-deps polaris backup restore-remote %s --config /data/config.yaml --yes
  docker compose up -d polaris

The middle command runs this exact restore-remote logic inside a fresh,
one-off container sharing the same data volume and the same bind-mounted
config.yaml — it has R2 credentials and direct filesystem access despite
running on the host, same as the plain restore command described in
"polaris backup restore --help".`, name)
}

func runDockerBackupCreate() error {
	url := dockerLocalBaseURL() + "/api/backup"
	client := &http.Client{Timeout: 2 * time.Minute}
	resp, err := client.Post(url, "application/json", nil)
	if err != nil {
		return fmt.Errorf("reaching the local polaris server at %s: %w (is the container running? try `docker compose ps`)", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("backup failed: %s", strings.TrimSpace(string(body)))
	}
	var info backup.Info
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return fmt.Errorf("decoding response from %s: %w", url, err)
	}
	fmt.Printf("created backup %s (%s)\n", info.Name, humanSize(info.SizeBytes))
	return nil
}

func runDockerBackupList() error {
	url := dockerLocalBaseURL() + "/api/backup"
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return fmt.Errorf("reaching the local polaris server at %s: %w (is the container running? try `docker compose ps`)", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("listing backups failed: %s", strings.TrimSpace(string(body)))
	}
	var infos []backup.Info
	if err := json.NewDecoder(resp.Body).Decode(&infos); err != nil {
		return fmt.Errorf("decoding response from %s: %w", url, err)
	}
	printBackupList(infos)
	return nil
}

// runDockerBackupListRemote hits the same GET /api/backup endpoint as
// runDockerBackupList with ?remote=1 — gateway/backup.go's
// handleListBackups branches on that to list R2 instead of the local
// backups/ folder, since the Docker CLI has no direct way to reach R2's
// API from the host any more than it has direct filesystem access.
func runDockerBackupListRemote() error {
	url := dockerLocalBaseURL() + "/api/backup?remote=1"
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return fmt.Errorf("reaching the local polaris server at %s: %w (is the container running? try `docker compose ps`)", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("listing r2 backups failed: %s", strings.TrimSpace(string(body)))
	}
	var objects []r2.Object
	if err := json.NewDecoder(resp.Body).Decode(&objects); err != nil {
		return fmt.Errorf("decoding response from %s: %w", url, err)
	}
	printRemoteBackupList(objects)
	return nil
}

func printBackupList(infos []backup.Info) {
	if len(infos) == 0 {
		fmt.Println("no backups yet")
		return
	}
	for _, info := range infos {
		fmt.Printf("%-28s %8s  %s\n", info.Name, humanSize(info.SizeBytes), info.CreatedAt.Local().Format("2006-01-02 15:04:05"))
	}
}

// printRemoteBackupList mirrors printBackupList's layout for R2 objects —
// same columns, but reports what's actually recoverable from R2 rather
// than what's on local disk, which is the whole point of `--remote`.
func printRemoteBackupList(objects []r2.Object) {
	if len(objects) == 0 {
		fmt.Println("no backups in r2 yet")
		return
	}
	for _, obj := range objects {
		fmt.Printf("%-28s %8s  %s\n", obj.Key, humanSize(obj.SizeBytes), obj.LastModified.Local().Format("2006-01-02 15:04:05"))
	}
}

// humanSize renders a byte count the way `ls -lh`/`du -h` do — KiB/MiB/
// GiB, one decimal place. The classic Go-wiki ByteSize idiom; not worth
// a dependency for something this small.
func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
