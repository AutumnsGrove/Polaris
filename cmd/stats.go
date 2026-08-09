package cmd

import (
	"fmt"
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
	statsCmd.Flags().StringVar(&configPath, "config", "config.yaml", "path to config.yaml")
	statsCmd.Flags().IntVar(&statsDays, "days", 30, "trailing days to scope period stats to (0 = all time)")
	rootCmd.AddCommand(statsCmd)
}

func runStats(cmd *cobra.Command, args []string) error {
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

	return nil
}
