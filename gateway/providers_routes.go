package gateway

import (
	"context"
	"net/http"
	"time"

	"github.com/AutumnsGrove/nova/auth/opencode"
	"github.com/AutumnsGrove/nova/provider"

	"polaris/agent"
	"polaris/routing"
)

// providers_routes.go serves GET /api/providers/status — what
// `polaris providers status|models` prints, and the same numbers the
// settings panel's Go section will show (Phase 3). It reports both
// backends' configuration, the live Go meters, the effective per-model
// policy, and the last probe error — never failing the request when the
// meters are unreachable, since the meters endpoint is undocumented and
// may move (plan §11 risk 6): then the panel degrades to "unavailable"
// and keeps the ledger-derived number.

// ProvidersStatusResponse is GET /api/providers/status's body.
type ProvidersStatusResponse struct {
	OpenRouter ProvidersBackendStatus `json:"openrouter"`
	OpenCodeGo ProvidersGoStatus      `json:"opencode_go"`
	Policy     ProvidersPolicyStatus  `json:"policy"`
	Models     []ProvidersModelStatus `json:"models"`
}

// ProvidersBackendStatus is the configured/not-configured bit for a backend.
type ProvidersBackendStatus struct {
	Configured bool `json:"configured"`
}

// ProvidersGoStatus is the OpenCode Go backend: its plan, the live
// account-wide meters, Polaris's own share of the window from the ledger,
// and the last probe error. Meters are account-wide (they include pi and
// OpenCode usage, not just Polaris), so the panel must say so — a number
// that silently mixes in other clients looks wrong next to the ledger.
type ProvidersGoStatus struct {
	Configured               bool             `json:"configured"`
	Plan                     string           `json:"plan,omitempty"`
	Meters                   []ProvidersMeter `json:"meters,omitempty"`
	PeriodEnd                string           `json:"period_end,omitempty"`
	PolarisShareAllowanceUSD float64          `json:"polaris_share_allowance_usd"`
	Error                    string           `json:"error,omitempty"`
}

// ProvidersMeter mirrors opencode.UsageMeter for the wire.
type ProvidersMeter struct {
	Kind     string  `json:"kind"`
	Label    string  `json:"label"`
	Short    string  `json:"short"`
	Percent  float64 `json:"percent"`
	UsedUSD  float64 `json:"used_usd"`
	LimitUSD float64 `json:"limit_usd"`
	ResetsAt string  `json:"resets_at,omitempty"`
}

// ProvidersPolicyStatus is the effective routing policy: the configured
// default and failover, plus whether the kill switch currently pins
// everything to OpenRouter regardless of the policy above.
type ProvidersPolicyStatus struct {
	DefaultBackend string `json:"default_backend"`
	Failover       string `json:"failover"`
	Pinned         bool   `json:"pinned"`
}

// ProvidersModelStatus is one registry model's effective route: the
// serving backend and wire slug from route.Primary, plus the Go monthly
// limit from Nova's catalog (0 when the model has no Go entry — unknown,
// not unlimited). PolarisShareAllowanceUSD is this model's ledger share
// of the plan window this month — the picker's headroom tint reads this
// Polaris-only number, never the account-wide meter.
type ProvidersModelStatus struct {
	ID                       string  `json:"id"`
	Primary                  string  `json:"primary"`
	WireModel                string  `json:"wire_model"`
	GoMonthlyLimitUSD        float64 `json:"go_monthly_limit_usd"`
	PolarisShareAllowanceUSD float64 `json:"polaris_share_allowance_usd"`
}

func (s *Server) handleProvidersStatus(w http.ResponseWriter, r *http.Request) {
	cfg := s.liveConfig()

	out := ProvidersStatusResponse{}
	if p, ok := cfg.Providers["openrouter"]; ok && p.APIKey != "" {
		out.OpenRouter.Configured = true
	}
	goCfg, goOK := cfg.Providers["opencode_go"]
	out.OpenCodeGo.Configured = goOK && goCfg.APIKey != ""
	out.OpenCodeGo.Plan = goCfg.Plan

	out.Policy.DefaultBackend = cfg.Routing.DefaultBackend
	if out.Policy.DefaultBackend == "" {
		out.Policy.DefaultBackend = "openrouter"
	}
	out.Policy.Failover = cfg.Routing.Failover
	if out.Policy.Failover == "" {
		out.Policy.Failover = "openrouter"
	}
	out.Policy.Pinned = agent.ForceOpenRouter()

	if share, err := s.db.ProviderAllowanceThisMonth(); err == nil {
		out.OpenCodeGo.PolarisShareAllowanceUSD = share
	} else {
		log.Warn("providers status: reading Polaris share failed", "err", err)
	}

	if out.OpenCodeGo.Configured {
		meters, err := fetchGoMeters(r.Context(), goCfg.APIKey)
		if err != nil {
			// Degrade, never fail: the meters endpoint is undocumented
			// and may change shape; the panel keeps the ledger number.
			out.OpenCodeGo.Error = err.Error()
		} else {
			for _, m := range meters {
				out.OpenCodeGo.Meters = append(out.OpenCodeGo.Meters, ProvidersMeter{
					Kind: string(m.Kind), Label: m.Label, Short: m.Short,
					Percent: m.Percent, UsedUSD: m.UsedUSD, LimitUSD: m.LimitUSD,
					ResetsAt: m.ResetsAt,
				})
			}
			out.OpenCodeGo.PeriodEnd = opencode.PeriodEnd(meters)
		}
	} else {
		out.OpenCodeGo.Error = "not configured — set providers.opencode_go.api_key"
	}

	// The effective per-model table: the same router turns use, so a
	// missing ModelID (which would silently bill OpenRouter) shows up
	// here as "default" before any money moves.
	shareByModel, err := s.db.ProviderModelAllowanceThisMonth()
	if err != nil {
		log.Warn("providers status: reading per-model share failed", "err", err)
		shareByModel = map[string]float64{}
	}
	router := routing.Build(cfg, routing.Options{ForceOpenRouter: out.Policy.Pinned})
	for _, m := range cfg.Models {
		primary, wire, _ := router.Primary(m.ID)
		st := ProvidersModelStatus{ID: m.ID, Primary: primary, WireModel: wire}
		if m.GoModel != "" {
			if gm, ok := provider.GetModel("opencode-go/" + m.GoModel); ok {
				st.GoMonthlyLimitUSD = gm.MonthlyLimitUSD
			}
		}
		st.PolarisShareAllowanceUSD = shareByModel[m.ID]
		out.Models = append(out.Models, st)
	}

	writeJSON(w, out)
}

// logOneShotProviderUsage records one routed one-shot call's Go spend —
// the same rule as turns (routing.TouchedGo: OpenRouter legs get no row),
// for ledger parity between loop calls and background calls (titles,
// suggestions, extraction). modelID is the Polaris registry id.
func (s *Server) logOneShotProviderUsage(res *agent.OneShotResult, modelID string) {
	if res == nil || !routing.TouchedGo(res.Usage) {
		return
	}
	if err := s.db.LogProviderUsage(routing.OpenCodeGo, modelID,
		res.Usage.AllowanceUSD, res.ListCost, res.Usage.BillingSource,
		res.Usage.PromptTokens, res.Usage.CompletionTokens, res.Usage.CachedTokens, res.Spilled); err != nil {
		log.Warn("failed to record one-shot provider usage", "model", modelID, "err", err)
	}
}

// fetchGoMeters reads the account-wide allowance windows with the
// operator's own key and no Nova credential store. Bounded: the settings
// panel must not hang a request on the Console.
func fetchGoMeters(ctx context.Context, key string) ([]opencode.UsageMeter, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	return opencode.FetchMeters(ctx, &http.Client{Timeout: 15 * time.Second}, "", key, "")
}
