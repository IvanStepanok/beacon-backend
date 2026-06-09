package store

import (
	"context"

	"github.com/stepanok/beacon-server/internal/model"
)

// StatsCounts returns totals + damage counts (BOTH the canonical 3-tier rollup and
// the 5-level EMS-98 detail) + verification/synced counts in one row. The tier
// counts (minimal/partial/complete) are computed off the generated damage_tier
// column so every report — on either capture scale — is counted; tier3-scale
// reports are NEVER silently dropped. tierMin+tierPart+tierComp == total.
func (s *Reports) StatsCounts(ctx context.Context, crisisID string) (total int, dmg model.DamageCounts, tier model.DamageTierCounts, ver model.VerificationCounts, synced int, err error) {
	err = s.pool.QueryRow(ctx, `
		SELECT count(*),
		       count(*) FILTER (WHERE damage_tier='minimal'),
		       count(*) FILTER (WHERE damage_tier='partial'),
		       count(*) FILTER (WHERE damage_tier='complete'),
		       count(*) FILTER (WHERE damage='none'),
		       count(*) FILTER (WHERE damage='slight'),
		       count(*) FILTER (WHERE damage='moderate'),
		       count(*) FILTER (WHERE damage='severe'),
		       count(*) FILTER (WHERE damage='destroyed'),
		       count(*) FILTER (WHERE verification='verified'),
		       count(*) FILTER (WHERE verification='pending'),
		       count(*) FILTER (WHERE verification='flagged'),
		       count(*) FILTER (WHERE synced)
		FROM reports WHERE crisis_id = $1`, crisisID).
		Scan(&total, &tier.Minimal, &tier.Partial, &tier.Complete,
			&dmg.None, &dmg.Slight, &dmg.Moderate, &dmg.Severe, &dmg.Destroyed,
			&ver.Verified, &ver.Pending, &ver.Flagged, &synced)
	return
}

// TaskStats returns dispatch-board counts (by task stage) + open life-safety tasks.
func (s *Reports) TaskStats(ctx context.Context, crisisID string) (tc model.TaskCounts, lifeSafetyOpen int, err error) {
	err = s.pool.QueryRow(ctx, `
		SELECT
		  count(*) FILTER (WHERE task_status='new'),
		  count(*) FILTER (WHERE task_status='triaged'),
		  count(*) FILTER (WHERE task_status='assigned'),
		  count(*) FILTER (WHERE task_status='in_progress'),
		  count(*) FILTER (WHERE task_status='resolved'),
		  count(*) FILTER (WHERE task_status='closed'),
		  count(*) FILTER (WHERE life_safety AND task_status NOT IN ('resolved','closed'))
		FROM reports WHERE crisis_id = $1`, crisisID).
		Scan(&tc.New, &tc.Triaged, &tc.Assigned, &tc.InProgress, &tc.Resolved, &tc.Closed, &lifeSafetyOpen)
	return
}

// TimeSeries returns 12 hourly buckets (hour 11 oldest .. 0 = now), mirroring the
// dashboard's bucketing: h = min(11, floor(ageMin/60)).
func (s *Reports) TimeSeries(ctx context.Context, crisisID string) ([]model.TimeBucket, error) {
	rows, err := s.pool.Query(ctx, `
		WITH ages AS (
			SELECT LEAST(11, FLOOR(GREATEST(0, EXTRACT(EPOCH FROM (now()-captured_at))/60) / 60))::int AS hour
			FROM reports WHERE crisis_id = $1
		)
		SELECT g AS hour, COALESCE(c.cnt, 0) AS cnt
		FROM generate_series(0,11) g
		LEFT JOIN (SELECT hour, count(*) cnt FROM ages GROUP BY hour) c ON c.hour = g
		ORDER BY g DESC`, crisisID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]model.TimeBucket, 0, 12)
	for rows.Next() {
		var b model.TimeBucket
		if err := rows.Scan(&b.Hour, &b.Count); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// Recent returns the newest n reports for a crisis.
func (s *Reports) Recent(ctx context.Context, crisisID string, n int) ([]model.Report, error) {
	rows, err := s.pool.Query(ctx,
		"SELECT "+reportSelect+" FROM reports WHERE crisis_id = $1 ORDER BY captured_at DESC, id DESC LIMIT $2",
		crisisID, n)
	if err != nil {
		return nil, err
	}
	return scanReports(rows)
}

// PdnaPivot aggregates damage COUNTS by (admin area × sector) — PDNA-ready damage-
// count aggregates that feed a PDNA, NOT a loss/cost estimation. Each report
// contributes one row per infrastructure type (LATERAL unnest), grouped by ADM2 and
// sector. The CANONICAL breakdown is the 3-tier rollup (minimal/partial/complete),
// computed off damage_tier so it is vocabulary-agnostic and minimal+partial+complete
// == total per row (no report is dropped or mis-bucketed). The 5-level EMS-98
// columns are kept as detail and only carry counts for ems98-scale reports.
func (s *Reports) PdnaPivot(ctx context.Context, crisisID string) ([]model.PdnaRow, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT COALESCE(adm2_pcode, '') AS pcode,
		       COALESCE(admin->'adm2'->>'name', '(unassigned)') AS name,
		       sector,
		       count(*) FILTER (WHERE damage_tier='minimal'),
		       count(*) FILTER (WHERE damage_tier='partial'),
		       count(*) FILTER (WHERE damage_tier='complete'),
		       count(*) FILTER (WHERE damage='none'),
		       count(*) FILTER (WHERE damage='slight'),
		       count(*) FILTER (WHERE damage='moderate'),
		       count(*) FILTER (WHERE damage='severe'),
		       count(*) FILTER (WHERE damage='destroyed'),
		       count(*) AS total
		FROM reports, unnest(infra_types) AS sector
		WHERE crisis_id = $1
		GROUP BY adm2_pcode, admin->'adm2'->>'name', sector
		ORDER BY pcode, sector`, crisisID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.PdnaRow{}
	for rows.Next() {
		var p model.PdnaRow
		if err := rows.Scan(&p.AdmPcode, &p.AdmName, &p.Sector,
			&p.Minimal, &p.Partial, &p.Complete,
			&p.None, &p.Slight, &p.Moderate, &p.Severe, &p.Destroyed, &p.Total); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ExportRows streams all reports matching a filter (no pagination) for export.
func (s *Reports) ExportRows(ctx context.Context, f ListFilter) ([]model.Report, error) {
	where, args := f.whereClause(1)
	sql := "SELECT " + reportSelect + " FROM reports " + where + " ORDER BY captured_at DESC, id DESC"
	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return scanReports(rows)
}
