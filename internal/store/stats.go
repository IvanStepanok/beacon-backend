package store

import (
	"context"
	"math"

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

// TimeSeries returns activity buckets (index N-1 oldest .. 0 = now), mirroring the
// dashboard's bucketing: idx = min(N-1, floor(ageMin/width)). The width adapts to
// the crisis age so the chart stays meaningful long after onset: up to 48h it keeps
// the original 12 hourly buckets; older crises switch to daily buckets covering the
// span (min 7, capped at 30 days). The returned unit is "hour" or "day".
func (s *Reports) TimeSeries(ctx context.Context, crisisID string) ([]model.TimeBucket, string, error) {
	// Crisis age picks the bucket width; an unknown crisis keeps the hourly view.
	var ageHours float64
	if err := s.pool.QueryRow(ctx,
		`SELECT COALESCE(EXTRACT(EPOCH FROM (now()-started_at))/3600, 0) FROM crises WHERE id = $1`,
		crisisID).Scan(&ageHours); err != nil {
		ageHours = 0
	}
	unit, width, n := "hour", 60, 12 // width in minutes
	if ageHours > 48 {
		unit, width = "day", 60*24
		n = int(math.Ceil(ageHours / 24))
		if n < 7 {
			n = 7 // at least a week so early daily charts don't render a few giant bars
		}
		if n > 30 {
			n = 30
		}
	}
	rows, err := s.pool.Query(ctx, `
		WITH ages AS (
			SELECT LEAST($2::int - 1, FLOOR(GREATEST(0, EXTRACT(EPOCH FROM (now()-captured_at))/60) / $3))::int AS bucket
			FROM reports WHERE crisis_id = $1
		)
		SELECT g AS bucket, COALESCE(c.cnt, 0) AS cnt
		FROM generate_series(0, $2::int - 1) g
		LEFT JOIN (SELECT bucket, count(*) cnt FROM ages GROUP BY bucket) c ON c.bucket = g
		ORDER BY g DESC`, crisisID, n, width)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	out := make([]model.TimeBucket, 0, n)
	for rows.Next() {
		var b model.TimeBucket
		if err := rows.Scan(&b.Hour, &b.Count); err != nil {
			return nil, "", err
		}
		out = append(out, b)
	}
	return out, unit, rows.Err()
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
