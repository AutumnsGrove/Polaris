package cmd

import (
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/spf13/cobra"

	"polaris/store"
)

var statsDays int

var statsCmd = &cobra.Command{
	Use:   "stats",
	Short: "Print usage and research-loop tuning stats (cost, tool calls, check-in/stale-streak firing)",
	RunE:  runStats,
}

func init() {
	statsCmd.Flags().IntVar(&statsDays, "days", 30, "trailing days to scope period stats to (0 = all time)")
	rootCmd.AddCommand(statsCmd)
}

func runStats(cmd *cobra.Command, args []string) error {
	return runDockerStats(statsDays)
}

// runDockerStats fetches stats from the running container's own
// /api/stats — the real config.yaml/polaris.db live inside the
// container's volume, not on the host this CLI runs on, so this is the
// only correct source.
func runDockerStats(days int) error {
	url := fmt.Sprintf("%s/api/stats?days=%d", dockerLocalBaseURL(), days)
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return fmt.Errorf("reaching the local polaris server at %s: %w (is the container running? try `docker compose ps`)", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("fetching stats failed (status %d)", resp.StatusCode)
	}

	var s store.Stats
	if err := readCappedJSON(resp, &s); err != nil {
		return fmt.Errorf("decoding stats response: %w", err)
	}

	printStats(&s)
	return nil
}

func printStats(s *store.Stats) {
	period := "all time"
	if s.PeriodDays > 0 {
		period = fmt.Sprintf("last %d days", s.PeriodDays)
	}

	fmt.Printf("cost: $%.4f total, $%.4f (%s)\n", s.TotalCostUSD, s.PeriodCostUSD, period)
	fmt.Printf("  polaris: $%.4f total, $%.4f (%s)\n", s.CostBySource.Polaris.TotalCostUSD, s.CostBySource.Polaris.PeriodCostUSD, period)
	fmt.Printf("  pulsar:  $%.4f total, $%.4f (%s)\n", s.CostBySource.Pulsar.TotalCostUSD, s.CostBySource.Pulsar.PeriodCostUSD, period)
	fmt.Printf("  daily:   $%.4f total, $%.4f (%s)\n", s.CostBySource.Daily.TotalCostUSD, s.CostBySource.Daily.PeriodCostUSD, period)
	fmt.Printf("threads: %d, turns: %d (%s)\n", s.ThreadCount, s.TurnCount, period)
	fmt.Printf("avg turn duration: %.1fs\n", float64(s.AvgTurnDurationMs)/1000)
	fmt.Printf("auto-compactions: %d (%s)\n", s.CompactionCount, period)
	fmt.Printf("prompt cache hits: %s (%s), %s total\n",
		cacheHitPercent(s.CacheUsage.PeriodPromptTokens, s.CacheUsage.PeriodCacheReadTokens), period,
		cacheHitPercent(s.CacheUsage.TotalPromptTokens, s.CacheUsage.TotalCacheReadTokens))
	fmt.Printf("code_exec wall time: %.1fs (%s)\n", float64(s.CodeExecWallTimeMS)/1000, period)

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

	fmt.Printf("\nweb_search providers (%s):\n", period)
	if len(s.SearchProviderCounts) == 0 {
		fmt.Println("  none")
	} else {
		total := 0
		for _, c := range s.SearchProviderCounts {
			total += c
		}
		providers := make([]string, 0, len(s.SearchProviderCounts))
		for p := range s.SearchProviderCounts {
			providers = append(providers, p)
		}
		sort.Strings(providers)
		for _, p := range providers {
			count := s.SearchProviderCounts[p]
			pct := float64(count) / float64(total) * 100
			fmt.Printf("  %-10s %5d searches   %5.1f%%\n", p, count, pct)
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

// cacheHitPercent renders a prompt-cache hit rate, or "n/a" when no turn in
// that window recorded usage — same "no misleading 0%" rule as the settings
// panel's Health row.
func cacheHitPercent(prompt, cached int) string {
	if prompt == 0 {
		return "n/a"
	}
	return fmt.Sprintf("%.0f%%", float64(cached)/float64(prompt)*100)
}
