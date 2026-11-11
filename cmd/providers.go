// providers.go implements `polaris providers status|models` — the
// operator's view into the multi-provider routing (docs/plans/multi-
// provider.md §7.4): which backends are configured, the live Go meters,
// the effective per-model policy, and the per-model route table.
//
// Thin client, like every other command that talks to a running Polaris:
// the real config.yaml and polaris.db live inside the container's named
// volume, not on the host this binary runs on, so the only correct route
// is the container's own REST API (cmd/docker_client.go).
package cmd

import (
	"fmt"
	"net/http"
	"time"

	"github.com/spf13/cobra"
)

var providersCmd = &cobra.Command{
	Use:   "providers",
	Short: "Inspect chat provider routing and the OpenCode Go subscription",
	Long: `Inspect which backend serves each model and what the Go subscription covers.

  polaris providers status   backends, live Go meters, policy, per-model routes
  polaris providers models   per-model route table with Go monthly limits`,
}

var providersStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show backend configuration, live Go meters, and effective routing",
	Args:  cobra.NoArgs,
	RunE:  runProvidersStatus,
}

var providersModelsCmd = &cobra.Command{
	Use:   "models",
	Short: "Show each model's serving backend and Go monthly limit",
	Args:  cobra.NoArgs,
	RunE:  runProvidersModels,
}

func init() {
	providersCmd.AddCommand(providersStatusCmd, providersModelsCmd)
	rootCmd.AddCommand(providersCmd)
}

// ProvidersStatusResponse mirrors gateway.ProvidersStatusResponse.
// Redefined here rather than imported so the CLI has no build-time
// dependency on the gateway package's wire types, same as cmd/stats.go's
// own shape.
type ProvidersStatusResponse struct {
	OpenRouter struct {
		Configured bool `json:"configured"`
	} `json:"openrouter"`
	OpenCodeGo struct {
		Configured               bool             `json:"configured"`
		Plan                     string           `json:"plan,omitempty"`
		Meters                   []ProvidersMeter `json:"meters,omitempty"`
		PeriodEnd                string           `json:"period_end,omitempty"`
		PolarisShareAllowanceUSD float64          `json:"polaris_share_allowance_usd"`
		Error                    string           `json:"error,omitempty"`
	} `json:"opencode_go"`
	Policy struct {
		DefaultBackend string `json:"default_backend"`
		Failover       string `json:"failover"`
		Pinned         bool   `json:"pinned"`
	} `json:"policy"`
	Models []ProvidersModelStatus `json:"models"`
}

// ProvidersMeter mirrors gateway.ProvidersMeter.
type ProvidersMeter struct {
	Kind     string  `json:"kind"`
	Label    string  `json:"label"`
	Short    string  `json:"short"`
	Percent  float64 `json:"percent"`
	UsedUSD  float64 `json:"used_usd"`
	LimitUSD float64 `json:"limit_usd"`
	ResetsAt string  `json:"resets_at,omitempty"`
}

// ProvidersModelStatus mirrors gateway.ProvidersModelStatus.
type ProvidersModelStatus struct {
	ID                string  `json:"id"`
	Primary           string  `json:"primary"`
	WireModel         string  `json:"wire_model"`
	GoMonthlyLimitUSD float64 `json:"go_monthly_limit_usd"`
}

func fetchProvidersStatus() (*ProvidersStatusResponse, error) {
	url := dockerLocalBaseURL() + "/api/providers/status"
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("reaching the local polaris server at %s: %w (is the container running? try `docker compose ps`)", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetching providers status failed (status %d)", resp.StatusCode)
	}
	var body ProvidersStatusResponse
	if err := readCappedJSON(resp, &body); err != nil {
		return nil, fmt.Errorf("decoding response from %s: %w", url, err)
	}
	return &body, nil
}

func runProvidersStatus(cmd *cobra.Command, args []string) error {
	body, err := fetchProvidersStatus()
	if err != nil {
		return err
	}

	orState := "not configured"
	if body.OpenRouter.Configured {
		orState = "configured"
	}
	fmt.Printf("openrouter: %s\n", orState)

	goState := "not configured"
	if body.OpenCodeGo.Configured {
		goState = "configured"
		if body.OpenCodeGo.Plan != "" {
			goState += fmt.Sprintf(" (plan %s)", body.OpenCodeGo.Plan)
		}
	}
	fmt.Printf("opencode_go: %s\n", goState)
	if body.OpenCodeGo.Configured {
		if body.OpenCodeGo.Error != "" {
			fmt.Printf("  meters unavailable: %s\n", body.OpenCodeGo.Error)
		} else {
			// The meters are account-wide (pi and OpenCode usage count
			// too), so the panel and this output say so — a number that
			// silently mixes in other clients looks wrong next to
			// Polaris's own ledger below.
			fmt.Println("  meters are account-wide (include non-Polaris usage):")
			for _, m := range body.OpenCodeGo.Meters {
				fmt.Printf("  %-12s $%7.2f / $%-7.2f (%5.1f%%) resets %s\n",
					m.Label+":", m.UsedUSD, m.LimitUSD, m.Percent, m.ResetsAt)
			}
			if body.OpenCodeGo.PeriodEnd != "" {
				fmt.Printf("  plan period ends %s\n", body.OpenCodeGo.PeriodEnd)
			}
		}
		fmt.Printf("  Polaris's share this month: $%.2f allowance\n", body.OpenCodeGo.PolarisShareAllowanceUSD)
	}

	pin := ""
	if body.Policy.Pinned {
		pin = " (kill switch pinned: everything on OpenRouter)"
	}
	fmt.Printf("policy: default=%s failover=%s%s\n", body.Policy.DefaultBackend, body.Policy.Failover, pin)
	for _, m := range body.Models {
		fmt.Printf("  %-12s -> %-11s (%s)\n", m.ID, m.Primary, m.WireModel)
	}
	return nil
}

func runProvidersModels(cmd *cobra.Command, args []string) error {
	body, err := fetchProvidersStatus()
	if err != nil {
		return err
	}
	for _, m := range body.Models {
		limit := "no Go route"
		if m.GoMonthlyLimitUSD > 0 {
			limit = fmt.Sprintf("$%.0f/mo Go limit", m.GoMonthlyLimitUSD)
		} else if m.Primary == "opencode-go" {
			// A Go-first route with no catalog limit (discovery-only
			// model): the limit is unknown, not unlimited.
			limit = "Go limit unknown (discovery-only model)"
		}
		fmt.Printf("%-12s %-11s %-28s %s\n", m.ID, m.Primary, m.WireModel, limit)
	}
	return nil
}
