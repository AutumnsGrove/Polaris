// comet_backfill.go runs Comet month by month over historical data — the
// one-time catch-up for a database that predates Comet (or a copied
// production DB being tested locally), mirroring
// `polaris constellation backfill`'s role for Weaver. Each month is
// classified then generated in order, oldest first, so a copied database
// gets real editions for every month it has conversations in.
package gateway

import (
	"context"
	"errors"
	"fmt"
	"time"

	"polaris/store"
	"polaris/tz"
)

// CometBackfillResult is one month's outcome.
type CometBackfillResult struct {
	Period    string `json:"period"`
	Trails    int    `json:"trails"`
	Generated bool   `json:"generated"`
	Error     string `json:"error,omitempty"`
}

// BackfillComet classifies and generates every month in [from, to]. Either
// bound may be empty: to defaults to the most recent complete month, from
// to the month of the earliest message in the database (or to itself when
// the database is empty). A single month failing doesn't abort the run —
// its error is recorded and the loop continues, same tolerance
// constellation's backfill has.
func (s *Server) BackfillComet(reqCtx context.Context, from, to string) ([]CometBackfillResult, error) {
	if to == "" {
		to = cometPeriodOf(tz.Now())
	}
	if _, _, ok := cometPeriodBounds(to); !ok {
		return nil, fmt.Errorf("invalid --to period %q (expected YYYY-MM)", to)
	}
	if from == "" {
		if at, ok, err := s.db.EarliestMessageAt(); err != nil {
			return nil, err
		} else if ok {
			// at is a UTC "YYYY-MM-DD HH:MM:SS" string; parse it in UTC and
			// take the month containing it in the active zone so a
			// late-evening message on the last day of a month doesn't land in
			// the wrong period. cometMonthOf, not cometPeriodOf — the latter
			// would walk back a month, generating an empty edition for the
			// month before the earliest activity.
			if t, perr := time.Parse("2006-01-02 15:04:05", at); perr == nil {
				from = cometMonthOf(t.In(tz.Location()))
			}
		}
		if from == "" {
			from = to
		}
	}
	if _, _, ok := cometPeriodBounds(from); !ok {
		return nil, fmt.Errorf("invalid --from period %q (expected YYYY-MM)", from)
	}
	if from > to {
		return nil, fmt.Errorf("--from (%s) is after --to (%s)", from, to)
	}

	var results []CometBackfillResult
	for period := from; ; {
		r := CometBackfillResult{Period: period}
		if processed, err := s.RunCometClassify(period, 0); err != nil {
			if errors.Is(err, store.ErrCometClassifyAlreadyRunning) {
				return results, err
			}
			r.Error = "classify: " + err.Error()
		} else {
			r.Trails = processed
		}
		if r.Error == "" {
			if err := s.runCometGeneration(reqCtx, period); err != nil {
				r.Error = "generate: " + err.Error()
			} else {
				r.Generated = true
			}
		}
		results = append(results, r)
		log.Info("comet backfill: month done", "period", period, "trails", r.Trails, "generated", r.Generated, "err", r.Error)

		if period == to {
			break
		}
		next, ok := cometAddMonths(period, 1)
		if !ok || next > to {
			break
		}
		period = next
	}
	return results, nil
}
