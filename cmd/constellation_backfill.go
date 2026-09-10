// constellation_backfill.go implements `polaris constellation backfill` —
// the one-time command that runs Weaver over every eligible thread that
// predates enabling Constellation (see docs/plans/constellation.md's
// "Backfill"). Not a settings-panel affordance: this happens once per
// install, ever.
package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/spf13/cobra"

	"polaris/config"
	"polaris/gateway"
	"polaris/models"
	"polaris/store"
)

var constellationBackfillLimit int

var constellationCmd = &cobra.Command{
	Use:   "constellation",
	Short: "Constellation admin commands",
}

var constellationBackfillCmd = &cobra.Command{
	Use:   "backfill",
	Short: "Run Weaver over every eligible thread that predates enabling Constellation",
	Args:  cobra.NoArgs,
	RunE:  runConstellationBackfill,
}

func init() {
	constellationBackfillCmd.Flags().StringVar(&configPath, "config", "config.yaml", "path to config.yaml (bare-metal only — a Docker install proxies through the running container instead)")
	constellationBackfillCmd.Flags().IntVarP(&constellationBackfillLimit, "n", "n", 0, "process only the N most recently active eligible threads, instead of the full backlog — useful for testing before committing to a full run")
	constellationCmd.AddCommand(constellationBackfillCmd)
	rootCmd.AddCommand(constellationCmd)
}

func runConstellationBackfill(cmd *cobra.Command, args []string) error {
	// Docker mode: the CLI binary outside the container has no access to
	// the container's polaris.db (a named Docker volume, not a plain host
	// file — see CLAUDE.md) or its config.yaml env passthrough, so this
	// proxies through the running container's own REST endpoint instead
	// of trying to open state it can't reach. Same isDockerComposeInstall
	// gate cmd/search.go and cmd/stats.go already use.
	if repoPath, err := os.Getwd(); err == nil && isDockerComposeInstall(repoPath) {
		return runDockerConstellationBackfill(constellationBackfillLimit)
	}

	cfg, err := config.Load(configPath, models.Registry)
	if err != nil {
		log.Warn("loading config failed", "path", configPath, "err", err)
		return err
	}
	db, err := store.Open(cfg.Database.Path)
	if err != nil {
		return err
	}
	defer db.Close()

	cfgRow, err := db.GetConstellationConfig()
	if err != nil {
		return err
	}
	client := gateway.WeaverClient(cfg, cfgRow.Model)

	if constellationBackfillLimit > 0 {
		fmt.Printf("backfilling up to %d threads...\n", constellationBackfillLimit)
	} else {
		fmt.Println("backfilling every eligible thread — this may take a while for a large backlog...")
	}

	processed, err := gateway.BackfillConstellation(context.Background(), db, client, constellationBackfillLimit)
	if err != nil {
		return err
	}
	fmt.Printf("done — processed %d thread(s)\n", processed)
	return nil
}

func runDockerConstellationBackfill(limit int) error {
	url := fmt.Sprintf("%s/api/constellation/backfill?limit=%d", dockerLocalBaseURL(), limit)
	// A full backlog run (160+ threads on the potato as of the plan doc's
	// writing) can genuinely take a long time — same generous-timeout
	// reasoning as runDockerModeCall/runDockerSearch, just wider still
	// since this is explicitly the slow, one-time, run-it-and-wait case.
	client := &http.Client{Timeout: 2 * time.Hour}

	if limit > 0 {
		fmt.Printf("backfilling up to %d threads via the running container...\n", limit)
	} else {
		fmt.Println("backfilling every eligible thread via the running container — this may take a while...")
	}

	resp, err := client.Post(url, "application/json", nil)
	if err != nil {
		return fmt.Errorf("reaching the local polaris server at %s: %w (is the container running? try `docker compose ps`)", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var errBody struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&errBody)
		if errBody.Error != "" {
			return fmt.Errorf("%s", errBody.Error)
		}
		return fmt.Errorf("backfill failed (status %d)", resp.StatusCode)
	}

	var body struct {
		Processed int `json:"processed"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return fmt.Errorf("decoding response from %s: %w", url, err)
	}
	fmt.Printf("done — processed %d thread(s)\n", body.Processed)
	return nil
}
