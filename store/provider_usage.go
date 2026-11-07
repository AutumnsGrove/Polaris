package store

// provider_usage.go is the per-call ledger of the OpenCode Go subscription
// side of spend (docs/plans/multi-provider.md §6) — one row per call that
// touched Go, the same shape and the same "record only what completed"
// rule as decisions_usage/aux_usage. OpenRouter calls get no row: their
// cost already lives in messages.cost_usd/aux_usage.

// LogProviderUsage records one completed call that touched the Go
// subscription. allowanceUSD is plan dollars (never summed into Stats
// totals); listCostUSD is the same call at retail, the currency the
// "covered by Go" headline is denominated in. spilled is true when a
// failover fired mid-call and the failed leg's tokens folded into this
// row — the panel footnotes the split as approximate instead of
// presenting it as exact.
func (s *Store) LogProviderUsage(provider, model string, allowanceUSD, listCostUSD float64, billingSource string, promptTokens, completionTokens, cacheReadTokens int, spilled bool) error {
	spilledInt := 0
	if spilled {
		spilledInt = 1
	}
	_, err := s.db.Exec(
		`INSERT INTO provider_usage (provider, model, allowance_usd, list_cost_usd, billing_source, prompt_tokens, completion_tokens, cache_read_tokens, spilled)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		provider, model, allowanceUSD, listCostUSD, billingSource, promptTokens, completionTokens, cacheReadTokens, spilledInt,
	)
	return err
}

// providerUsageSum is the shared SUM for the Stats breakouts below: list
// price in one currency, so the covered/balance slices add up with the
// existing totals instead of mixing plan dollars into them.
func (s *Store) providerUsageSum(where, since string) (float64, error) {
	query := `SELECT COALESCE(SUM(list_cost_usd), 0) FROM provider_usage WHERE ` + where
	args := []interface{}{}
	if since != "" {
		query += ` AND created_at >= ?`
		args = append(args, since)
	}
	var total float64
	err := s.db.QueryRow(query, args...).Scan(&total)
	return total, err
}

// CoveredBySubscription sums list_cost_usd where billing_source says the
// subscription covered it (go/go-plus) — the "covered by Go" breakout,
// already counted once inside the normal totals via messages.cost_usd.
func (s *Store) CoveredBySubscription(since string) (float64, error) {
	return s.providerUsageSum(`billing_source IN ('go', 'go-plus')`, since)
}

// BilledToBalance sums list_cost_usd where billing_source is balance —
// real console money, and always a misconfiguration (plan §6.6).
func (s *Store) BilledToBalance(since string) (float64, error) {
	return s.providerUsageSum(`billing_source = 'balance'`, since)
}

// ProviderAllowanceThisMonth sums allowance_usd for the current calendar
// month — Polaris's own share of the plan window, shown in the Go panel
// next to the account-wide meter ("Polaris's share this month").
func (s *Store) ProviderAllowanceThisMonth() (float64, error) {
	var total float64
	err := s.db.QueryRow(
		`SELECT COALESCE(SUM(allowance_usd), 0) FROM provider_usage WHERE strftime('%Y-%m', created_at) = strftime('%Y-%m', 'now')`,
	).Scan(&total)
	return total, err
}

// ProviderModelAllowanceThisMonth breaks ProviderAllowanceThisMonth down
// by registry model id — the picker's per-row Polaris-only headroom tint
// reads this, never the account-wide meter (which includes other clients
// and would mislabel a shared window as ours alone).
func (s *Store) ProviderModelAllowanceThisMonth() (map[string]float64, error) {
	rows, err := s.db.Query(
		`SELECT model, COALESCE(SUM(allowance_usd), 0) FROM provider_usage WHERE strftime('%Y-%m', created_at) = strftime('%Y-%m', 'now') GROUP BY model`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]float64{}
	for rows.Next() {
		var model string
		var total float64
		if err := rows.Scan(&model, &total); err != nil {
			return nil, err
		}
		out[model] = total
	}
	return out, rows.Err()
}
