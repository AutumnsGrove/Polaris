package cmd

import (
	"context"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"polaris/config"
	"polaris/gateway"
	"polaris/logger"
	"polaris/models"
	"polaris/store"
	"polaris/web"
)

var log = logger.WithPrefix("cmd")

var configPath string
var devMode bool

// eventRetentionDays mirrors the log files' own 90-day retention (see
// logger.rotatingWriter) so the events table's durable evidence trail
// doesn't grow forever on a long-running install.
const eventRetentionDays = 90

// attachmentMaxAge is generous on purpose — this only ever catches an
// upload that was written to disk but never actually sent in a message
// (see gateway.PruneOldAttachments), so there's no cost to erring toward
// "definitely abandoned" over risking a slow upload-then-send collision.
const attachmentMaxAge = 24 * time.Hour

// shutdownGrace bounds how long a SIGTERM/SIGINT (systemd's `systemctl
// restart`/`stop` — see procmgr/systemd.go, triggered by the self-update
// flow in gateway/update.go) waits for whatever's in flight — an agent
// turn streaming over /ws, or a synchronous POST /api/ask — to finish and
// persist before this process actually exits. It's a bound, not a
// promise: a deep-research turn can legitimately run past it, in which
// case it gets killed exactly as abruptly as it would have without any
// of this, just far less often in practice — see gateway.Server.
// BeginShutdown's doc comment for what this is actually protecting
// against. Keep this comfortably under the systemd unit's TimeoutStopSec
// (see procmgr/systemd.go's unitTemplate) so systemd never SIGKILLs
// mid-drain before this deadline has a chance to fire on its own.
const shutdownGrace = 25 * time.Second

var runCmd = &cobra.Command{
	Use:   "run",
	Short: "Start the Polaris server",
	RunE:  runRun,
}

func init() {
	runCmd.Flags().StringVar(&configPath, "config", "config.yaml", "path to config.yaml")
	runCmd.Flags().BoolVar(&devMode, "dev", false, "skip serving the embedded frontend (use `vite dev` instead, which proxies /api and /ws here)")
	rootCmd.AddCommand(runCmd)
}

func runRun(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load(configPath, models.Registry)
	if err != nil {
		return err
	}

	if err := logger.Init(cfg.Logging.Dir); err != nil {
		return fmt.Errorf("initializing logger: %w", err)
	}

	db, err := store.Open(cfg.Database.Path)
	if err != nil {
		return err
	}
	defer db.Close()

	if err := db.PruneEvents(eventRetentionDays); err != nil {
		log.Warn("pruning old events failed", "err", err)
	}
	if err := gateway.PruneOldAttachments(cfg.Attachments.Dir, attachmentMaxAge); err != nil {
		log.Warn("pruning old attachments failed", "err", err)
	}
	db.LogEvent("", "info", "startup", "server started", map[string]interface{}{"dev": devMode}, "")

	var staticFS fs.FS
	if !devMode {
		staticFS, err = fs.Sub(web.Assets, "build")
		if err != nil {
			return fmt.Errorf("mounting embedded frontend: %w", err)
		}
	}

	// AppVersion defaults to exactly "dev" (see version.go) when nothing
	// was injected via -ldflags -X, which is what a plain bare-metal
	// `go build` (updater.Run's self-update included) does — no ldflags
	// at all. Only pass through a real injected version (the Docker
	// release workflow sets one, formatted to match bare-metal's own
	// "rNNN.hash" via the Dockerfile's ARG VERSION); otherwise gateway
	// falls back to its own git-based computeVersion, preserving
	// bare-metal's existing version display exactly.
	version := AppVersion
	if version == "dev" {
		version = ""
	}
	srv := gateway.New(cfg, configPath, db, staticFS, version)

	addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
	httpServer := &http.Server{Addr: addr, Handler: srv.Handler()}

	serveErr := make(chan error, 1)
	go func() {
		log.Infof("listening on %s (dev=%v)", addr, devMode)
		serveErr <- httpServer.ListenAndServe()
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

	select {
	case err := <-serveErr:
		if err != nil && err != http.ErrServerClosed {
			return err
		}
		return nil
	case sig := <-sigCh:
		log.Infof("received %s — draining in-flight turns before exit (up to %s)", sig, shutdownGrace)
		db.LogEvent("", "info", "shutdown", "received signal, draining in-flight turns before restart/exit", map[string]interface{}{"signal": sig.String()}, "")
	}

	// Stop accepting new turns immediately — one started now would only
	// get killed mid-flight when this function returns below, which is
	// exactly the "thread silently reverts to an older state" failure
	// mode this whole shutdown sequence exists to avoid.
	srv.BeginShutdown()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Warn("http server did not shut down cleanly within the grace period", "err", err)
	}

	drainCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	if err := srv.WaitForActiveTurns(drainCtx); err != nil {
		log.Warn("timed out waiting for in-flight turns to finish — aborting them and exiting anyway", "err", err)
		// Whatever's still running past the deadline (a multi-step research
		// turn doing several sequential tool calls is the common case — see
		// AbortActiveTurns' doc comment) is about to die with this process
		// regardless. Telling the client explicitly, instead of letting the
		// WebSocket just go silent, is what turns "the app mysteriously
		// bumped me back to an old thread" into a normal, visible, retryable
		// error.
		srv.AbortActiveTurns("the server is restarting and this turn couldn't finish in time — please retry")
	} else {
		log.Info("all in-flight turns finished cleanly, exiting")
	}

	return nil
}
