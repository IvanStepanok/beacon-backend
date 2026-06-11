package service

import (
	"context"
	"math"

	"github.com/stepanok/beacon-server/internal/model"
	"github.com/stepanok/beacon-server/internal/store"
)

type StatsService struct{ reports *store.Reports }

func NewStatsService(reports *store.Reports) *StatsService { return &StatsService{reports: reports} }

func pct(n, total int) int {
	if total == 0 {
		return 0
	}
	return int(math.Round(float64(n) / float64(total) * 100))
}

// Overview assembles the dashboard's overview entirely from SQL aggregates.
func (s *StatsService) Overview(ctx context.Context, crisisID string) (*model.StatsOverview, error) {
	total, tier, ver, synced, err := s.reports.StatsCounts(ctx, crisisID)
	if err != nil {
		return nil, err
	}
	// All-statuses area counts: /stats/overview is analyst-gated (requireAnalyst),
	// and its aggregate numbers deliberately cover every report (the handler
	// coarsens only the embedded Recent[] for the viewer tier). The PUBLIC
	// /reports/area-groups endpoint passes verifiedOnly=true for the anon tier.
	areas, err := s.reports.AreaGroups(ctx, crisisID, false)
	if err != nil {
		return nil, err
	}
	series, seriesUnit, err := s.reports.TimeSeries(ctx, crisisID)
	if err != nil {
		return nil, err
	}
	recent, err := s.reports.Recent(ctx, crisisID, 6)
	if err != nil {
		return nil, err
	}
	// Headline damage percentages off the canonical tier rollup. complete == the
	// complete tier; partial+complete == the "heavy damage" headline.
	completePct := pct(tier.Complete, total)
	return &model.StatsOverview{
		TotalReports:       total,
		DamageTierCounts:   tier,
		VerificationCounts: ver,
		SyncedCount:        synced,
		SyncedPct:          pct(synced, total),
		CompletePct:        completePct,
		DestroyedPct:       completePct, // alias kept for dashboard compatibility
		SeverePlusPct:      pct(tier.Partial+tier.Complete, total),
		Areas:              areas,
		TimeSeries:         series,
		TimeSeriesUnit:     seriesUnit,
		Recent:             recent,
	}, nil
}
