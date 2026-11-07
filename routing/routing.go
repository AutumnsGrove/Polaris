// Package routing builds the Nova provider router Polaris routes every
// chat-shaped call through (docs/plans/multi-provider.md §5). The mapping
// from Polaris config to route.Config is one-to-one and lives here, in one
// function, so a second policy layer can never drift from Nova's:
//
//   - providers.* (api_key, base_url) → ProviderDecls (name/kind/URL/key)
//   - a go_model + an eligible backend → a two-attempt route (Go first,
//     then OpenRouter) with OnLimitOrRefusal spill and a visible OnFallback
//   - default_backend: openrouter → no Go routes are built at all (every id
//     falls through to the default provider unrewritten)
//   - default_backend: opencode_go → Go-first routes for eligible ids,
//     OpenRouter single-attempt routes for the rest (deliberately not
//     ForceProvider: a pin would send go_model-less ids to Go carrying an
//     OpenRouter slug)
//   - model_backends[m]: openrouter → single-attempt OpenRouter regardless
//     of default; model_backends[m]: opencode_go → Go-first (a load error
//     if the id has no go_model, never a silent fallback)
//   - failover: none → omit the second attempt (there is no separate flag)
//   - POLARIS_NOVA_LOOP=0 → ForceProvider = "openrouter": the only use of
//     the pin, the kill switch that keeps the bespoke loop fully OpenRouter
//   - the thread id → Config.SessionID, read fresh per call (exactly the
//     x-opencode-session header requirement)
//
// route.New panics on a misconfigured policy, so config.Load validates
// providers/routing/model_backends itself (config.validateRouting) — a typo
// in config.yaml breaks startup with a message, never a running server.
// Build assumes a validated config. It is cheap (a router per turn from
// that turn's config generation, not one shared process-wide), because
// SessionID must close over the calling thread: a cached process-wide
// router could not carry per-thread session ids.
//
// Build also registers every registry model in Nova's catalog
// (RegisterModels), so chat.Complete can translate a Polaris id to its wire
// slug — without it every call silently falls through to the default
// provider, looking exactly like success while billing OpenRouter.
package routing

import (
	"github.com/AutumnsGrove/nova/provider"
	"github.com/AutumnsGrove/nova/provider/route"

	"polaris/config"
)

// OpenRouter and OpenCodeGo are the stable provider names attempt chains
// and Response.Provider use. OpenRouter matches its providers-block key;
// OpenCodeGo (hyphen) is the router-side name for the providers-block key
// "opencode_go" (underscore) — config.validateRouting enforces that only
// those two keys exist, and routerName maps them (a route carrying the
// config key into an attempt chain would never match Response.Provider).
const (
	OpenRouter = "openrouter"
	OpenCodeGo = "opencode-go"
)

// routerName maps a providers-block key to the router-side provider name.
var routerName = map[string]string{
	"openrouter":  OpenRouter,
	"opencode_go": OpenCodeGo,
}

// Options tunes one built router. Everything is read once at Build time
// except SessionID, which the router calls fresh on every request.
type Options struct {
	// SessionID returns the conversation id for the current call. Nil (or
	// an empty result) sends no x-opencode-session header.
	SessionID func() string
	// UserAgent is sent on requests that don't already carry one, so the
	// backend sees the app, not a SDK.
	UserAgent string
	// OnFallback fires once per spill with the failed attempt, the next
	// attempt, and the classified error — the hook that makes a paid
	// retry visible (transcript chip) instead of silent.
	OnFallback func(from, to string, err error)
	// ForceOpenRouter pins every call to OpenRouter: the kill switch
	// (POLARIS_NOVA_LOOP=0). The only use of the pin — all other policy
	// is route-building.
	ForceOpenRouter bool
	// Headroom is the Phase 4 seam (plan §11 risk 9): under
	// default_backend auto, a model reporting no headroom resolves to
	// OpenRouter up front instead of spending a Go attempt to discover
	// the window is spent. Nil means no pre-emption — failover stays the
	// backstop, which is the whole of Phase 2.
	Headroom func(modelID string) bool
}

// Build constructs the router for one config generation. See the package
// doc comment for the mapping; see config.BackendFor for per-model
// resolution.
func Build(cfg *config.Config, opts Options) *route.Router {
	RegisterModels(cfg.Models)

	decls := make([]route.ProviderDecl, 0, len(cfg.Providers))
	for name, p := range cfg.Providers {
		rname, ok := routerName[name]
		if !ok {
			// Unreachable on a Loaded config (validateRouting rejects
			// unknown provider names); skipping a hand-built unknown is
			// louder downstream — any route naming it panics in New —
			// than silently misrouting it through a guessed kind.
			continue
		}
		var kind route.Kind
		if rname == OpenCodeGo {
			kind = route.OpenCodeGo
		} else {
			kind = route.OpenRouter
		}
		decls = append(decls, route.ProviderDecl{
			Name:    rname,
			Kind:    kind,
			BaseURL: p.BaseURL,
			APIKey:  p.APIKey,
		})
	}

	failover := cfg.Routing.Failover
	if failover == "" {
		failover = "openrouter"
	}
	routes := make(map[string]route.Route, len(cfg.Models))
	for _, m := range cfg.Models {
		// BackendFor speaks config keys ("opencode_go"); the attempt
		// chain speaks router names (OpenCodeGo). Same backend, two
		// spellings — see the const block above.
		backend, _ := cfg.BackendFor(m.ID)
		// Under auto, a model reporting no headroom stays on OpenRouter
		// up front (Phase 4). Without the hook, auto treats every
		// eligible model as headed for Go and lets failover decide.
		if backend == "opencode_go" && opts.Headroom != nil &&
			cfg.Routing.DefaultBackend == "auto" && !opts.Headroom(m.ID) {
			backend = OpenRouter
		}
		if backend == "opencode_go" {
			attempts := []route.Attempt{{Provider: OpenCodeGo, Model: m.GoModel}}
			if failover == "openrouter" {
				attempts = append(attempts, route.Attempt{Provider: OpenRouter, Model: m.Model})
			}
			routes[m.ID] = route.Route{
				Attempts:   attempts,
				FallbackOn: route.OnLimitOrRefusal,
				OnFallback: opts.OnFallback,
			}
		} else {
			routes[m.ID] = route.Route{
				Attempts:   []route.Attempt{{Provider: OpenRouter, Model: m.Model}},
				FallbackOn: route.OnLimitOrRefusal,
				OnFallback: opts.OnFallback,
			}
		}
	}

	defaultProvider := OpenRouter
	if _, ok := cfg.Providers[OpenRouter]; !ok {
		// A Go-only boot has no OpenRouter entry; unknown ids fall to Go
		// rather than panicking New on a default naming no provider.
		// Every real model id has an explicit route, so this only serves
		// ids the registry never declared.
		defaultProvider = OpenCodeGo
	}
	force := ""
	if opts.ForceOpenRouter {
		force = OpenRouter
	}
	return route.New(route.Config{
		Providers:       decls,
		Routes:          routes,
		DefaultProvider: defaultProvider,
		ForceProvider:   force,
		SessionID:       opts.SessionID,
		UserAgent:       opts.UserAgent,
	})
}

// RegisterModels adds every registry entry to Nova's catalog (ID → wire
// slug), replacing any entry with the same ID. chat.Complete resolves a
// Polaris id through this catalog, and would otherwise send the bare id
// ("deepseek") to the wire. Idempotent: safe to call on every Build.
func RegisterModels(models []config.ModelConfig) {
	for _, m := range models {
		if m.ID == "" {
			continue
		}
		provider.RegisterModel(provider.Model{
			ID:       m.ID,
			Name:     m.Name,
			Provider: OpenRouter,
			Slug:     m.Model,
		})
	}
}

// TouchedGo reports whether a call's usage shows Go involvement: a
// present usage.opencode block (billing source set) or either dollar
// figure nonzero. A covered free-model call has $0 allowance but a real
// list price, so allowance alone is not the test. OpenRouter calls carry
// none of the three and get no provider_usage row.
func TouchedGo(u provider.Usage) bool {
	return u.BillingSource != "" || u.AllowanceUSD != 0 || u.ListCostUSD != 0
}

// ListCost is the one expression that stays correct across all three call
// shapes (docs/plans/multi-provider.md §6), and the failure mode of getting
// it wrong is a number that looks plausible:
//
//   - a single Go attempt: CostUSD == AllowanceUSD, so it reduces to
//     ListCostUSD (retail);
//   - an OpenRouter attempt: both Go fields are zero, so it reduces to
//     CostUSD (retail);
//   - a spilled call: Usage.Add sums CostUSD across attempts, so CostUSD is
//     allowance plus retail while ListCostUSD only covers the Go attempt —
//     the subtraction removes the allowance half.
//
// A plain ctx.AddCost(resp.Usage.CostUSD) is right for OpenRouter and wrong
// for Go: it overstates list cost by the peak multiplier (×2 in DeepSeek's
// weekday windows) and records $0 for a billing_source "free" call that
// still has a real list price. Never sum allowance dollars into a total.
func ListCost(u provider.Usage) float64 {
	return u.ListCostUSD + (u.CostUSD - u.AllowanceUSD)
}
