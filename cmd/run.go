package cmd

import (
	"fmt"
	"io/fs"
	"net/http"

	"github.com/spf13/cobra"

	"polaris/config"
	"polaris/gateway"
	"polaris/logger"
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
	cfg, err := config.Load(configPath)
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
	db.LogEvent("", "info", "startup", "server started", map[string]interface{}{"dev": devMode})

	var staticFS fs.FS
	if !devMode {
		staticFS, err = fs.Sub(web.Assets, "build")
		if err != nil {
			return fmt.Errorf("mounting embedded frontend: %w", err)
		}
	}

	srv := gateway.New(cfg, configPath, db, staticFS)

	addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
	log.Infof("listening on %s (dev=%v)", addr, devMode)
	return http.ListenAndServe(addr, srv.Handler())
}
