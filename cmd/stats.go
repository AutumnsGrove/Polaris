package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"

	"github.com/spf13/cobra"

	"polaris/config"
	"polaris/models"
	"polaris/store"
)

var statsDays int

var statsCmd = &cobra.Command{
	Use:   "stats",
	Short: "Print usage and research-loop tuning stats (cost, tool calls, check-in/stale-streak firing)",
	RunE:  runStats,
}

func init() {
	statsCmd.Flags().StringVar(&configPath, "config", "config.yaml", "path to config.yaml (bare-metal only — a Docker install fetches this from the running container instead)")
	statsCmd.Flags().IntVar(&statsDays, "days", 30, "trailing days to scope period stats to (0 = all time)")
	rootCmd.AddCommand(statsCmd)
}

func runStats(cmd *cobra.Command, args []string) error {
	// Docker mode: there's no local config.yaml/polaris.db this process
	// can correctly read at all — the real ones live inside the
	// container's own volume (/data/config.yaml, /data/polaris.db).
	// Reading ./config.yaml here would either fail outright, or worse,
	// silently succeed against a *stale* pre-migration copy left over
	// on disk and report convincing-looking but wrong numbers with no
	// error at all. Fetch the real stats from the running container's
	// own /api/stats instead — the exact same data the settings panel's
	// usage section shows.
	if repoPath, err := os.Getwd(); err == nil && isDockerComposeInstall(repoPath) {
		return runDockerStats(statsDays)
	}

	cfg, err := config.Load(configPath, models.Registry)
	if err != nil {
		return err
	}

	db, err := store.Open(cfg.Database.Path)
	if err != nil {
		return err
	}
	defer db.Close()

	s, err := db.GetStats(statsDays)
	if err != nil {
		return err
	}

	printStats(s)
	return nil
}

// runDockerStats fetches the same stats runStats prints, but from the
// running container's /api/stats instead of a local config/db — see
// runStats's doc comment on why that's the only correct source under
// Docker.
func runDockerStats(days int) error {
	url := fmt.Sprintf("%s/api/stats?days=%d", dockerLocalBaseURL(), days)
	resp, err := http.Get(url)
	if err != nil {
		return fmt.Errorf("reaching the local polaris server at %s: %w (is the container running? try `docker compose ps`)", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("fetching stats failed (status %d)", resp.StatusCode)
	}

	var s store.Stats
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		return fmt.Errorf("decoding stats response: %w", err)
	}

	printStats(&s)
	return nil
}

// printStats is runStats/runDockerStats's shared formatting tail —
// identical output regardless of whether the stats came from a local
// store.Store.GetStats call or the same data fetched over HTTP from a
// running container, so bare-metal and Docker report in exactly the
// same shape.
func printStats(s *store.Stats) {
	period := "all time"
	if s.PeriodDays > 0 {
		period = fmt.Sprintf("last %d days", s.PeriodDays)
	}

	fmt.Printf("cost: $%.4f total, $%.4f (%s)\n", s.TotalCostUSD, s.PeriodCostUSD, period)
	fmt.Printf("threads: %d, turns: %d (%s)\n", s.ThreadCount, s.TurnCount, period)
	fmt.Printf("avg turn duration: %.1fs\n", float64(s.AvgTurnDurationMs)/1000)
	fmt.Printf("auto-compactions: %d (%s)\n", s.CompactionCount, period)

	fmt.Printf("\ntool calls (%s):\n", period)
	if len(s.ToolCallCounts) == 0 {
		fmt.Println("  none")
	} else {
		tools := make([]string, 0, len(s.ToolCallCounts))
		for t := range s.ToolCallCounts {
			tools = append(tools, t)
		}
		sort.Strings(tools)
		for _, t := range tools {
			calls := s.ToolCallCounts[t]
			errs := s.ToolErrorCounts[t]
			errPct := 0.0
			if calls > 0 {
				errPct = float64(errs) / float64(calls) * 100
			}
			fmt.Printf("  %-20s %5d calls   %5.1f%% errored\n", t, calls, errPct)
		}
	}

	fmt.Printf("\nresearch loop steering (%s):\n", period)
	fmt.Printf("  check-in nudges:      %d\n", s.CheckInCount)
	fmt.Printf("  stale-streak warnings: %d\n", s.StaleStreakCount)
	fmt.Printf("  max-turns wrap-ups:   %d", s.MaxTurnsWrapupCount)
	if s.TurnCount > 0 {
		fmt.Printf("  (%.1f%% of turns ran out of turn budget)", float64(s.MaxTurnsWrapupCount)/float64(s.TurnCount)*100)
	}
	fmt.Println()
}
