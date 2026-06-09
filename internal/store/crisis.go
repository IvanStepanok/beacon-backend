package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stepanok/beacon-server/internal/model"
)

type Crises struct{ pool *pgxpool.Pool }

func NewCrises(pool *pgxpool.Pool) *Crises { return &Crises{pool: pool} }

const crisisSelect = `
  id, title, area, nature, center_lat, center_lng, source, started_at,
  GREATEST(0, FLOOR(EXTRACT(EPOCH FROM (now() - started_at)) / 3600))::int AS ago_hrs,
  glide, response_level, radius_km, ended_at, status, response_id,
  -- Live count, not the denormalized crises.report_count: seed inserts and the
  -- normal submit path write reports.crisis_id directly and never bump the
  -- column, so it drifts (cheap here: reports.crisis_id is indexed).
  (SELECT count(*)::int FROM reports r WHERE r.crisis_id = crises.id) AS report_count`

func scanCrisis(row pgx.Row) (model.Crisis, error) {
	var c model.Crisis
	if err := row.Scan(&c.ID, &c.Title, &c.Area, &c.Nature, &c.CenterLat, &c.CenterLng,
		&c.Source, &c.StartedAt, &c.StartedAgoHrs, &c.Glide, &c.ResponseLevel,
		&c.RadiusKm, &c.EndedAt, &c.Status, &c.ResponseID, &c.ReportCount); err != nil {
		return model.Crisis{}, err
	}
	c.Lat, c.Lng = c.CenterLat, c.CenterLng // dashboard alias
	return c, nil
}

// List returns crises filtered by status (empty = all), newest first.
func (s *Crises) List(ctx context.Context, statuses []string) ([]model.Crisis, error) {
	sql := "SELECT " + crisisSelect + " FROM crises"
	args := []any{}
	if len(statuses) > 0 {
		sql += " WHERE status = ANY($1)"
		args = append(args, statuses)
	}
	sql += " ORDER BY started_at DESC"
	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Crisis{}
	for rows.Next() {
		c, err := scanCrisis(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Near returns active/proposed crises whose center is within max(radius, withinKm)
// of the point, nearest first, annotated with distanceKm + covers (point inside
// the crisis's own radius). Powers the mobile location-first launch.
func (s *Crises) Near(ctx context.Context, lat, lng, withinKm float64) ([]model.Crisis, error) {
	pt := "ST_SetSRID(ST_MakePoint($2,$1),4326)::geography"
	sql := "SELECT " + crisisSelect + ",\n" +
		"  ST_Distance(geom::geography, " + pt + ")/1000.0 AS dist_km\n" +
		"FROM crises\n" +
		"WHERE status IN ('active','proposed')\n" +
		"  AND ST_DWithin(geom::geography, " + pt + ", GREATEST(radius_km, $3)*1000.0)\n" +
		"ORDER BY dist_km ASC"
	rows, err := s.pool.Query(ctx, sql, lat, lng, withinKm)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Crisis{}
	for rows.Next() {
		var c model.Crisis
		var dist float64
		if err := rows.Scan(&c.ID, &c.Title, &c.Area, &c.Nature, &c.CenterLat, &c.CenterLng,
			&c.Source, &c.StartedAt, &c.StartedAgoHrs, &c.Glide, &c.ResponseLevel,
			&c.RadiusKm, &c.EndedAt, &c.Status, &c.ResponseID, &c.ReportCount, &dist); err != nil {
			return nil, err
		}
		c.Lat, c.Lng = c.CenterLat, c.CenterLng
		d := dist
		covers := dist <= c.RadiusKm
		c.DistanceKm, c.Covers = &d, &covers
		out = append(out, c)
	}
	return out, rows.Err()
}

// AssignCrisis picks the crisis that should own a report at this point+time: the
// nearest active/proposed crisis whose coverage radius contains the point and
// whose (generous) time window contains capturedAt. Returns "" if none → pending.
func (s *Crises) AssignCrisis(ctx context.Context, lat, lng float64, capturedAt time.Time) (string, error) {
	pt := "ST_SetSRID(ST_MakePoint($2,$1),4326)::geography"
	var id string
	err := s.pool.QueryRow(ctx, "SELECT id FROM crises\n"+
		"WHERE status IN ('active','proposed')\n"+
		"  AND ST_DWithin(geom::geography, "+pt+", radius_km*1000.0)\n"+
		"  AND $3 >= started_at - interval '7 days'\n"+
		"  AND (ended_at IS NULL OR $3 <= ended_at + interval '7 days')\n"+
		"ORDER BY ST_Distance(geom::geography, "+pt+") ASC\n"+
		"LIMIT 1", lat, lng, capturedAt).Scan(&id)
	if err != nil {
		if err == pgx.ErrNoRows {
			return "", nil
		}
		return "", err
	}
	return id, nil
}

// Emergent-cluster tuning (configurable in production; these are sane defaults).
const (
	emergentRadiusKm  = 2.0
	emergentWindowHrs = 24
	emergentMinReport = 3
)

// DetectEmergentCrisis checks whether enough PENDING reports have clustered around
// (lat,lng) within emergentRadiusKm over the last emergentWindowHrs. If so it
// creates a 'proposed' crisis (source='emergent') at the cluster centroid and
// pulls the clustered pending reports (and their buildings) into it. Returns the
// new crisis id, or "" when no cluster formed. An analyst confirms/dismisses it.
func (s *Crises) DetectEmergentCrisis(ctx context.Context, lat, lng float64, at time.Time) (string, error) {
	cutoff := at.Add(-emergentWindowHrs * time.Hour)
	pt := "ST_SetSRID(ST_MakePoint($2,$1),4326)::geography"

	var n int
	var clat, clng float64
	var earliest time.Time
	var nature, place *string
	err := s.pool.QueryRow(ctx, "SELECT count(*), COALESCE(avg(lat),0), COALESCE(avg(lng),0), COALESCE(min(captured_at), now()),\n"+
		"  mode() WITHIN GROUP (ORDER BY (crisis_nature)[1]),\n"+
		"  mode() WITHIN GROUP (ORDER BY NULLIF(place,''))\n"+
		"FROM reports\n"+
		"WHERE crisis_id IS NULL AND captured_at >= $3\n"+
		"  AND ST_DWithin(geom::geography, "+pt+", $4*1000.0)",
		lat, lng, cutoff, emergentRadiusKm).Scan(&n, &clat, &clng, &earliest, &nature, &place)
	if err != nil {
		return "", err
	}
	if n < emergentMinReport {
		return "", nil
	}

	nat := "conflict"
	if nature != nil && *nature != "" {
		nat = *nature
	}
	title := "Possible new event"
	area := "Reported damage cluster"
	if place != nil && *place != "" {
		title += " · " + *place
		area = *place
	}

	var newID string
	cpt := "ST_SetSRID(ST_MakePoint($4,$3),4326)::geography" // centroid point
	txErr := RunInTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx,
			"INSERT INTO crises (id, title, area, nature, geom, center_lat, center_lng, source, started_at, radius_km, status, report_count)\n"+
				"VALUES ('emergent-' || replace(gen_random_uuid()::text,'-',''), $1, $2, $3, ST_SetSRID(ST_MakePoint($5,$4),4326), $4, $5, 'emergent', $6, $7, 'proposed', $8)\n"+
				"RETURNING id",
			title, area, nat, clat, clng, earliest, emergentRadiusKm, n).Scan(&newID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			"UPDATE reports SET crisis_id = $1, updated_at = now()\n"+
				"WHERE crisis_id IS NULL AND captured_at >= $2 AND ST_DWithin(geom::geography, "+cpt+", $5*1000.0)",
			newID, cutoff, clat, clng, emergentRadiusKm); err != nil {
			return err
		}
		_, err := tx.Exec(ctx,
			"UPDATE buildings SET crisis_id = $1 WHERE crisis_id IS NULL AND id IN (SELECT building_id FROM reports WHERE crisis_id = $1 AND building_id IS NOT NULL)",
			newID)
		return err
	})
	if txErr != nil {
		return "", txErr
	}
	return newID, nil
}

// ActivateIfProposed flips a 'proposed' crisis to 'active' — the ground-truth
// activation step: feed-detected (USGS/GDACS) and emergent crises are born
// 'proposed' and become 'active' only when a community report is assigned to
// them (or an analyst activates them via SetCrisisStatus). Reports true when
// the promotion happened, false when the crisis was already active/closed.
func (s *Crises) ActivateIfProposed(ctx context.Context, id string) (bool, error) {
	tag, err := s.pool.Exec(ctx,
		"UPDATE crises SET status = 'active' WHERE id = $1 AND status = 'proposed'", id)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// SetCrisisStatus transitions a crisis (analyst confirm/dismiss of an emergent
// proposal). On 'dismissed' it releases the crisis's reports back to pending so
// they can re-cluster or be assigned elsewhere. Returns the updated crisis.
func (s *Crises) SetCrisisStatus(ctx context.Context, id, status string) (*model.Crisis, error) {
	var updated *model.Crisis
	err := RunInTx(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, "UPDATE crises SET status = $2 WHERE id = $1", id, status)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return nil // updated stays nil → 404 at handler
		}
		if status == "dismissed" {
			if _, err := tx.Exec(ctx, "UPDATE reports SET crisis_id = NULL, updated_at = now() WHERE crisis_id = $1", id); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, "UPDATE buildings SET crisis_id = NULL WHERE crisis_id = $1", id); err != nil {
				return err
			}
		}
		c, err := scanCrisis(tx.QueryRow(ctx, "SELECT "+crisisSelect+" FROM crises WHERE id = $1", id))
		if err != nil {
			return err
		}
		updated = &c
		return nil
	})
	return updated, err
}

func (s *Crises) Get(ctx context.Context, id string) (*model.Crisis, error) {
	c, err := scanCrisis(s.pool.QueryRow(ctx, "SELECT "+crisisSelect+" FROM crises WHERE id = $1", id))
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &c, nil
}

// Active returns the most recently started ACTIVE crisis, or nil if none.
func (s *Crises) Active(ctx context.Context) (*model.Crisis, error) {
	c, err := scanCrisis(s.pool.QueryRow(ctx, "SELECT "+crisisSelect+" FROM crises WHERE status = 'active' ORDER BY started_at DESC LIMIT 1"))
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &c, nil
}

// ActiveID returns the id of the most recently started ACTIVE crisis (the same
// crisis Active() returns), or "" if none. Used as the default scope for
// stats/map/area-groups/export when no ?crisisId is supplied, so an omitted scope
// is coherent with the /crises/active header. A lightweight id-only query that
// mirrors Active()'s exact WHERE/ORDER so it resolves to the identical crisis.
func (s *Crises) ActiveID(ctx context.Context) (string, error) {
	var id string
	err := s.pool.QueryRow(ctx, "SELECT id FROM crises WHERE status = 'active' ORDER BY started_at DESC LIMIT 1").Scan(&id)
	if err != nil {
		if err == pgx.ErrNoRows {
			return "", nil
		}
		return "", err
	}
	return id, nil
}

func (s *Crises) DangerZones(ctx context.Context, crisisID string) ([]model.DangerZone, error) {
	rows, err := s.pool.Query(ctx,
		"SELECT id, crisis_id, name, note, severity FROM danger_zones WHERE crisis_id = $1 ORDER BY id", crisisID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.DangerZone{}
	for rows.Next() {
		var d model.DangerZone
		if err := rows.Scan(&d.ID, &d.CrisisID, &d.Name, &d.Note, &d.Severity); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// ── seeder upserts ──────────────────────────────────────────────────────

func (s *Crises) UpsertCrisis(ctx context.Context, c model.Crisis) error {
	if c.Status == "" {
		c.Status = "active"
	}
	if c.RadiusKm == 0 {
		c.RadiusKm = 40
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO crises (id, title, area, nature, geom, center_lat, center_lng, source, started_at, glide, response_level, radius_km, status, response_id)
		VALUES ($1,$2,$3,$4, ST_SetSRID(ST_MakePoint($6,$5),4326), $5,$6,$7,$8,$9,$10,$11,$12,$13)
		ON CONFLICT (id) DO NOTHING`,
		c.ID, c.Title, c.Area, c.Nature, c.CenterLat, c.CenterLng, c.Source, c.StartedAt, c.Glide, c.ResponseLevel,
		c.RadiusKm, c.Status, c.ResponseID)
	return err
}

// UpsertExternalCrisis inserts or refreshes a feed-sourced crisis (USGS/GDACS),
// idempotent by its deterministic id so re-polling updates rather than duplicates.
// On conflict it ONLY updates rows that are themselves feed-sourced — it never
// clobbers an analyst-declared or emergent crisis.
func (s *Crises) UpsertExternalCrisis(ctx context.Context, c model.Crisis) error {
	if c.Status == "" {
		c.Status = "active"
	}
	if c.RadiusKm == 0 {
		c.RadiusKm = 40
	}
	if c.StartedAt.IsZero() {
		c.StartedAt = time.Now().UTC()
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO crises (id, title, area, nature, geom, center_lat, center_lng, source, started_at, ended_at, glide, radius_km, status)
		VALUES ($1,$2,$3,$4, ST_SetSRID(ST_MakePoint($6,$5),4326), $5,$6,$7,$8,$9,$10,$11,$12)
		ON CONFLICT (id) DO UPDATE SET
		    title = EXCLUDED.title, area = EXCLUDED.area, nature = EXCLUDED.nature,
		    geom = EXCLUDED.geom, center_lat = EXCLUDED.center_lat, center_lng = EXCLUDED.center_lng,
		    ended_at = EXCLUDED.ended_at, glide = EXCLUDED.glide, radius_km = EXCLUDED.radius_km,
		    status = EXCLUDED.status
		  WHERE crises.source LIKE 'feed:%'`,
		c.ID, c.Title, c.Area, c.Nature, c.CenterLat, c.CenterLng, c.Source, c.StartedAt, c.EndedAt, c.Glide, c.RadiusKm, c.Status)
	return err
}

// AssignPendingToCrisis pulls PENDING reports (crisis_id NULL) that fall within a
// crisis's coverage radius + (generous) time window into it, and refreshes the
// crisis's denormalized report_count. Returns how many reports were assigned.
func (s *Crises) AssignPendingToCrisis(ctx context.Context, crisisID string) (int, error) {
	var assigned int
	err := RunInTx(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			WITH c AS (SELECT geom, radius_km, started_at, ended_at FROM crises WHERE id = $1)
			UPDATE reports r SET crisis_id = $1, updated_at = now()
			FROM c
			WHERE r.crisis_id IS NULL
			  AND ST_DWithin(r.geom::geography, c.geom::geography, c.radius_km*1000.0)
			  AND r.captured_at >= c.started_at - interval '7 days'
			  AND (c.ended_at IS NULL OR r.captured_at <= c.ended_at + interval '7 days')`, crisisID)
		if err != nil {
			return err
		}
		assigned = int(tag.RowsAffected())
		_, err = tx.Exec(ctx,
			"UPDATE crises SET report_count = (SELECT count(*) FROM reports WHERE crisis_id = $1) WHERE id = $1", crisisID)
		return err
	})
	return assigned, err
}

func (s *Crises) UpsertDangerZone(ctx context.Context, d model.DangerZone) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO danger_zones (id, crisis_id, name, note, severity)
		VALUES ($1,$2,$3,$4,$5) ON CONFLICT (id) DO NOTHING`,
		d.ID, d.CrisisID, d.Name, d.Note, d.Severity)
	return err
}

func (s *Crises) UpsertSubmitter(ctx context.Context, anonymousID string, alias *string, reportCount, buildingCount, points int, badges json.RawMessage) error {
	if len(badges) == 0 {
		badges = json.RawMessage("[]")
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO submitters (anonymous_id, alias, report_count, building_count, points, badges)
		VALUES ($1,$2,$3,$4,$5,$6) ON CONFLICT (anonymous_id) DO NOTHING`,
		anonymousID, alias, reportCount, buildingCount, points, []byte(badges))
	return err
}

// PointsPerVerifiedReport is the fixed, server-authoritative award for each of a
// submitter's reports that an analyst has VERIFIED. Points are anti-gaming: they
// are DERIVED at read time from the count of that submitter's verified reports
// (see GetOrCreateProfile), never a mutable counter a client can increment. A bad
// actor cannot inflate points by spamming unverified reports or by calling any API.
const PointsPerVerifiedReport = 10

// VerifiedBadgeThreshold is the number of verified reports that earns the
// "Verified eyes" badge (badges likewise key off verified contributions only).
const VerifiedBadgeThreshold = 5

// GetOrCreateProfile resolves a profile by its public anonymous id, creating an
// empty submitter row on first sight (anonymous device onboarding).
//
// ANTI-GAMING (Requirement #3): the gamified fields — points, reportCount,
// buildingCount and badges — are COMPUTED HERE from the submitter's actual reports,
// NOT read from the mutable submitters.* counters (which the client could otherwise
// inflate). points = (# of this submitter's reports with verification='verified') *
// PointsPerVerifiedReport. Unverified/flagged spam earns nothing.
func (s *Crises) GetOrCreateProfile(ctx context.Context, anonymousID string) (*model.Profile, error) {
	_, err := s.pool.Exec(ctx,
		"INSERT INTO submitters (anonymous_id) VALUES ($1) ON CONFLICT (anonymous_id) DO NOTHING", anonymousID)
	if err != nil {
		return nil, err
	}
	var p model.Profile
	var verifiedReports int
	err = s.pool.QueryRow(ctx, `
		SELECT s.anonymous_id, s.alias,
		       COALESCE(agg.report_count, 0)   AS report_count,
		       COALESCE(agg.building_count, 0) AS building_count,
		       COALESCE(agg.verified_count, 0) AS verified_count
		FROM submitters s
		LEFT JOIN LATERAL (
			SELECT count(*)                                                          AS report_count,
			       count(DISTINCT r.building_id) FILTER (WHERE r.building_id IS NOT NULL) AS building_count,
			       count(*) FILTER (WHERE r.verification = 'verified')               AS verified_count
			FROM reports r WHERE r.submitter_id = s.id
		) agg ON true
		WHERE s.anonymous_id = $1`, anonymousID).
		Scan(&p.AnonymousID, &p.Alias, &p.ReportCount, &p.BuildingCount, &verifiedReports)
	if err != nil {
		return nil, err
	}
	// Server-derived points: only verified contributions count.
	p.Points = verifiedReports * PointsPerVerifiedReport
	p.Badges = deriveBadges(p.ReportCount, p.BuildingCount, verifiedReports)
	return &p, nil
}

// deriveBadges computes the reporter's badges from real contribution counts (NOT a
// stored list). Every badge keys off verified work or genuine distinct activity, so
// repeat/spam submissions cannot earn them. Mirrors the demo badge shape the client
// already renders ({id,name,earned,progressLabel?}).
func deriveBadges(reportCount, buildingCount, verifiedCount int) json.RawMessage {
	type badge struct {
		ID            string `json:"id"`
		Name          string `json:"name"`
		Earned        bool   `json:"earned"`
		ProgressLabel string `json:"progressLabel,omitempty"`
	}
	badges := []badge{
		{ID: "first", Name: "First responder", Earned: verifiedCount >= 1},
		{ID: "streets", Name: "Street mapper", Earned: buildingCount >= 5},
	}
	verified := badge{ID: "verified", Name: "Verified eyes", Earned: verifiedCount >= VerifiedBadgeThreshold}
	if !verified.Earned {
		verified.ProgressLabel = fmt.Sprintf("%d/%d verified", verifiedCount, VerifiedBadgeThreshold)
	}
	badges = append(badges, verified)
	b, err := json.Marshal(badges)
	if err != nil || len(b) == 0 {
		return json.RawMessage("[]")
	}
	return json.RawMessage(b)
}

// ResolveSubmitterID maps a public anonymous id (e.g. the device's X-Device-Id)
// to the internal submitters.id UUID (as text), creating the submitter on first
// sight. Used to stamp submitter_id on submit and to back the mine=true filter.
func (s *Crises) ResolveSubmitterID(ctx context.Context, anonymousID string) (string, error) {
	if _, err := s.pool.Exec(ctx,
		"INSERT INTO submitters (anonymous_id) VALUES ($1) ON CONFLICT (anonymous_id) DO NOTHING", anonymousID); err != nil {
		return "", err
	}
	var id string
	err := s.pool.QueryRow(ctx, "SELECT id::text FROM submitters WHERE anonymous_id = $1", anonymousID).Scan(&id)
	return id, err
}

// NOTE: the former AddPoints mutator was REMOVED as part of the anti-gaming work
// (Requirement #3). Points are now derived purely from verified reports in
// GetOrCreateProfile; there is no longer any code path that lets a caller increment
// a submitter's points. The submitters.points/report_count/building_count columns
// are now vestigial (only ever written by the seeder) and are ignored on read.
