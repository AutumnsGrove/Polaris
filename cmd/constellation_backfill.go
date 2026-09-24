// constellation_backfill.go implements `polaris constellation backfill` —
// the one-time command that runs Weaver over every eligible thread that
// predates enabling Constellation (see docs/plans/constellation.md's
// "Backfill"). Not a settings-panel affordance: this happens once per
// install, ever.
package cmd

import (
	"fmt"
	"net/http"
	"time"

	"github.com/spf13/cobra"
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
	constellationBackfillCmd.Flags().IntVarP(&constellationBackfillLimit, "n", "n", 0, "process only the N most recently active eligible threads, instead of the full backlog — useful for testing before committing to a full run")
	constellationCmd.AddCommand(constellationBackfillCmd)
	rootCmd.AddCommand(constellationCmd)
}

// runConstellationBackfill proxies through the running container's own
// REST endpoint — the CLI binary outside the container has no access to
// the container's polaris.db (a named Docker volume, not a plain host
// file — see CLAUDE.md).
func runConstellationBackfill(cmd *cobra.Command, args []string) error {
	limit := constellationBackfillLimit
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
		_ = readCappedJSON(resp, &errBody)
		if errBody.Error != "" {
			return fmt.Errorf("%s", errBody.Error)
		}
		return fmt.Errorf("backfill failed (status %d)", resp.StatusCode)
	}

	var body struct {
		Processed int `json:"processed"`
	}
	if err := readCappedJSON(resp, &body); err != nil {
		return fmt.Errorf("decoding response from %s: %w", url, err)
	}
	fmt.Printf("done — processed %d thread(s)\n", body.Processed)
	return nil
}
