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
	total, dmg, tier, ver, synced, err := s.reports.StatsCounts(ctx, crisisID)
	if err != nil {
		return nil, err
	}
	areas, err := s.reports.AreaGroups(ctx, crisisID)
	if err != nil {
		return nil, err
	}
	series, err := s.reports.TimeSeries(ctx, crisisID)
	if err != nil {
		return nil, err
	}
	recent, err := s.reports.Recent(ctx, crisisID, 6)
	if err != nil {
		return nil, err
	}
	tc, lifeSafetyOpen, err := s.reports.TaskStats(ctx, crisisID)
	if err != nil {
		return nil, err
	}
	// Headline damage percentages are computed off the CANONICAL tier rollup so they
	// stay correct under either capture scale (the global default is tier3, whose
	// reports never appear in the 5-level dmg.* counts). complete == EMS-98 destroyed
	// + tier-3 complete; partial+complete == the "heavy damage" headline.
	completePct := pct(tier.Complete, total)
	return &model.StatsOverview{
		TotalReports:       total,
		DamageTierCounts:   tier,
		DamageCounts:       dmg,
		VerificationCounts: ver,
		SyncedCount:        synced,
		SyncedPct:          pct(synced, total),
		CompletePct:        completePct,
		DestroyedPct:       completePct, // alias kept for dashboard compatibility
		SeverePlusPct:      pct(tier.Partial+tier.Complete, total),
		TaskCounts:         tc,
		LifeSafetyOpen:     lifeSafetyOpen,
		Areas:              areas,
		TimeSeries:         series,
		Recent:             recent,
	}, nil
}
