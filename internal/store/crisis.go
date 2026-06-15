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
  (SELECT count(*)::int FROM reports r WHERE r.crisis_id = crises.id) AS report_count,
  -- Distinct submitters (the corroboration signal the review queue ranks on): an
  -- emergent cluster of 5 reports from 1 device is far weaker than 5 from 5 devices.
  (SELECT count(DISTINCT r.submitter_id)::int FROM reports r WHERE r.crisis_id = crises.id) AS distinct_submitters`

func scanCrisis(row pgx.Row) (model.Crisis, error) {
	var c model.Crisis
	if err := row.Scan(&c.ID, &c.Title, &c.Area, &c.Nature, &c.CenterLat, &c.CenterLng,
		&c.Source, &c.StartedAt, &c.StartedAgoHrs, &c.Glide, &c.ResponseLevel,
		&c.RadiusKm, &c.EndedAt, &c.Status, &c.ResponseID, &c.ReportCount, &c.DistinctSubmitters); err != nil {
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
			&c.RadiusKm, &c.EndedAt, &c.Status, &c.ResponseID, &c.ReportCount, &c.DistinctSubmitters, &dist); err != nil {
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

// EmergentConfig holds the (deployment-tunable) thresholds for forming an emergent
// crisis. A new crowd cluster has no crisis row yet, so FORMATION uses these global
// defaults (config.Config / env BEACON_EMERGENT_*); the effective values are stamped
// onto the created crisis row for provenance and future per-crisis tuning.
type EmergentConfig struct {
	RadiusKm   float64 // spatial cluster radius (km)
	WindowHrs  int     // look-back window (hours)
	MinReports int     // minimum DISTINCT submitters required to propose a crisis
}

// DefaultEmergentConfig mirrors the historical hardcoded thresholds — used by tests
// and as a safety fallback when config has not been threaded through.
func DefaultEmergentConfig() EmergentConfig {
	return EmergentConfig{RadiusKm: 2.0, WindowHrs: 24, MinReports: 3}
}

// AdminScope constrains an emergent cluster to a single admin area (so a 2 km circle
// straddling two districts cannot merge them into one crisis). Level is the admin
// depth (1/2/3) of the generated reports.adm{N}_pcode column to filter on; Level 0
// means "no admin scope" — the point fell outside all known boundaries, so we fall
// back to the pure-radius behaviour.
type AdminScope struct {
	Level int
	Pcode string
}

func (a AdminScope) active() bool { return a.Level >= 1 && a.Level <= 3 && a.Pcode != "" }

// DetectEmergentCrisis checks whether enough DISTINCT submitters have clustered
// around (lat,lng) within cfg.RadiusKm over the last cfg.WindowHrs — and, when a
// scope is given, within the SAME admin area. If so it creates a 'proposed' crisis
// (source='emergent') at the cluster centroid and pulls the clustered pending
// reports (and their buildings) into it. Returns the new crisis id, or "" when no
// cluster formed. An analyst confirms (→active) or dismisses it; this NEVER
// auto-activates — a proposed crisis stays out of the public/default scope until a
// human confirms it.
//
// The threshold counts DISTINCT submitter_id (not raw rows): three reports from one
// device can never propose a crisis. NULL-submitter (fully anonymous, no device id)
// reports do not count toward the distinct gate.
//
// areaName resolves the centroid to an admin-area name (the service passes the
// admin_areas reverse-geocode; "" / nil = unresolved). The title/area are NEVER
// built from a report's free-text place — client placeholders like "Your location"
// must not leak into crisis titles; the fallback is the centroid's coordinates.
func (s *Crises) DetectEmergentCrisis(ctx context.Context, lat, lng float64, at time.Time, cfg EmergentConfig, scope AdminScope, areaName func(ctx context.Context, lat, lng float64) string) (string, error) {
	cutoff := at.Add(-time.Duration(cfg.WindowHrs) * time.Hour)
	pt := "ST_SetSRID(ST_MakePoint($2,$1),4326)::geography"

	where := "WHERE crisis_id IS NULL AND captured_at >= $3\n" +
		"  AND ST_DWithin(geom::geography, " + pt + ", $4*1000.0)"
	countArgs := []any{lat, lng, cutoff, cfg.RadiusKm}
	if scope.active() {
		where += fmt.Sprintf("\n  AND adm%d_pcode = $5", scope.Level)
		countArgs = append(countArgs, scope.Pcode)
	}

	var nTotal, nDistinct int
	var clat, clng float64
	var earliest time.Time
	var nature *string
	err := s.pool.QueryRow(ctx, "SELECT count(*), count(DISTINCT submitter_id),\n"+
		"  COALESCE(avg(lat),0), COALESCE(avg(lng),0), COALESCE(min(captured_at), now()),\n"+
		"  mode() WITHIN GROUP (ORDER BY (crisis_nature)[1])\n"+
		"FROM reports\n"+where,
		countArgs...).Scan(&nTotal, &nDistinct, &clat, &clng, &earliest, &nature)
	if err != nil {
		return "", err
	}
	if nDistinct < cfg.MinReports {
		return "", nil
	}

	nat := "conflict"
	if nature != nil && *nature != "" {
		nat = *nature
	}
	title := fmt.Sprintf("Possible new event near %.2f, %.2f", clat, clng)
	area := "Reported damage cluster"
	if areaName != nil {
		if name := areaName(ctx, clat, clng); name != "" {
			title = "Possible new event · " + name
			area = name
		}
	}
	var adminPcode *string
	if scope.active() {
		p := scope.Pcode
		adminPcode = &p
	}

	var newID string
	// The pull-in circle MUST use the SAME centre + radius + admin scope as the gate
	// (count) query above — the triggering pin (lat,lng), NOT the centroid. A
	// centroid-centred circle of the same radius is a SHIFTED set, so it could leave
	// some gate-counted reports unattached (and the stored geom/centre still uses the
	// centroid for display). Identical predicate ⇒ exactly the gated reports attach.
	ppt := "ST_SetSRID(ST_MakePoint($4,$3),4326)::geography" // trigger pin ($3=lat,$4=lng)
	txErr := RunInTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx,
			"INSERT INTO crises (id, title, area, nature, geom, center_lat, center_lng, source, started_at, radius_km, status, report_count, admin_pcode, emergent_radius_km, emergent_window_hrs, emergent_min_reports)\n"+
				"VALUES ('emergent-' || replace(gen_random_uuid()::text,'-',''), $1, $2, $3, ST_SetSRID(ST_MakePoint($5,$4),4326), $4, $5, 'emergent', $6, $7, 'proposed', $8, $9, $7, $10, $11)\n"+
				"RETURNING id",
			title, area, nat, clat, clng, earliest, cfg.RadiusKm, nTotal, adminPcode, cfg.WindowHrs, cfg.MinReports).Scan(&newID); err != nil {
			return err
		}
		updWhere := "WHERE crisis_id IS NULL AND captured_at >= $2 AND ST_DWithin(geom::geography, " + ppt + ", $5*1000.0)"
		updArgs := []any{newID, cutoff, lat, lng, cfg.RadiusKm}
		if scope.active() {
			updWhere += fmt.Sprintf(" AND adm%d_pcode = $6", scope.Level)
			updArgs = append(updArgs, scope.Pcode)
		}
		if _, err := tx.Exec(ctx,
			"UPDATE reports SET crisis_id = $1, updated_at = now()\n"+updWhere, updArgs...); err != nil {
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

// FormOverrides loads a crisis's stored modular-form overrides. nil means
// "defaults apply" — returned both when the crisis has no overrides (NULL
// column) and when the crisis does not exist, so the PUBLIC /form-schema read
// never errors out over a stale/unknown crisis id (offline-first clients may
// hold one); the capture form falls back to the built-in sections.
func (s *Crises) FormOverrides(ctx context.Context, crisisID string) (*model.FormOverrides, error) {
	var raw []byte
	err := s.pool.QueryRow(ctx, "SELECT form_overrides FROM crises WHERE id = $1", crisisID).Scan(&raw)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var ov model.FormOverrides
	if err := json.Unmarshal(raw, &ov); err != nil {
		return nil, err
	}
	return &ov, nil
}

// SetFormOverrides stores a crisis's form overrides verbatim. Empty overrides
// (both lists empty) clear the column back to NULL — "reset to defaults".
// Returns false when the crisis does not exist (→ 404 at the handler).
func (s *Crises) SetFormOverrides(ctx context.Context, crisisID string, ov model.FormOverrides) (bool, error) {
	var val any
	if len(ov.Required) > 0 || len(ov.Disabled) > 0 {
		b, err := json.Marshal(ov)
		if err != nil {
			return false, err
		}
		val = b
	}
	tag, err := s.pool.Exec(ctx, "UPDATE crises SET form_overrides = $2 WHERE id = $1", crisisID, val)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
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
