package cmd

import (
	"fmt"
	"io/fs"
	"net/http"

	"github.com/spf13/cobra"

	"localassistant/config"
	"localassistant/gateway"
	"localassistant/logger"
	"localassistant/store"
	"localassistant/web"
)

var log = logger.WithPrefix("cmd")

var configPath string
var devMode bool

var runCmd = &cobra.Command{
	Use:   "run",
	Short: "Start the LocalAssistant server",
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

	db, err := store.Open(cfg.Database.Path)
	if err != nil {
		return err
	}
	defer db.Close()

	var staticFS fs.FS
	if !devMode {
		staticFS, err = fs.Sub(web.Assets, "build")
		if err != nil {
			return fmt.Errorf("mounting embedded frontend: %w", err)
		}
	}

	srv := gateway.New(cfg, db, staticFS)

	addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
	log.Infof("listening on %s (dev=%v)", addr, devMode)
	return http.ListenAndServe(addr, srv.Handler())
}
