package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	h3 "github.com/uber/h3-go/v4"

	"github.com/stepanok/beacon-server/internal/model"
)

// h3Resolution is the H3 grid resolution used for hotspot aggregation and the
// exported h3id. Res 8 ≈ 0.74 km² hexagons — a neighbourhood-scale grain that
// reads as a damage hotspot without pinpointing a single building, and roughly
// matches the public-tile coarsening intent. Kept as one constant so insert,
// aggregation and export always agree; a per-crisis grain is a future knob.
const h3Resolution = 8

// H3CellR8 returns the resolution-8 H3 cell id (hex string) for a point — the
// hexagonal aggregation/interoperability key. Returns "" if H3 rejects the point
// (it never should for a valid lat/lng), so callers can treat "" as "no cell".
func H3CellR8(lat, lng float64) string {
	cell, err := h3.LatLngToCell(h3.LatLng{Lat: lat, Lng: lng}, h3Resolution)
	if err != nil {
		return ""
	}
	return cell.String()
}

// querier is satisfied by both *pgxpool.Pool and pgx.Tx.
type querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

const insertReportSQL = `
INSERT INTO reports (
  id, idempotency_key, crisis_id, submitter_id, damage, possibly_damaged, verification, debris,
  infra_types, infra_other_detail, infra_name, crisis_nature, geom, lat, lng, gps_accuracy_m,
  building_id, building_source,
  version, supersedes_report_id, plus_code, landmark, place,
  desc_original, desc_original_lang, desc_translated, desc_translated_lang,
  ai_level, ai_confidence, photos, size_bytes, modular, anonymization,
  is_mine, synced, sync_state, captured_at, created_at, updated_at, admin,
  clusters,
  location_resolved,
  h3_r8
) VALUES (
  $1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,
  $12,
  CASE WHEN $13::float8 IS NULL OR $14::float8 IS NULL THEN NULL ELSE ST_SetSRID(ST_MakePoint($13,$14),4326) END,
  $14, $13, $15,
  $16,$17,
  $18,$19,$20,$21,$22,
  $23,$24,$25,$26,
  $27,$28,$29,$30,$31,$32,
  $33,$34,$35,$36,$37,$38,$39,
  $40,
  $41,
  $42
) ON CONFLICT (id) DO NOTHING`

// UpsertReport inserts a fully-formed report idempotently (ON CONFLICT (id) DO
// NOTHING). Returns inserted=false if the id already existed (replay). The caller
// (service) computes version/supersedes; the seeder passes version=1.
func UpsertReport(ctx context.Context, q querier, r model.Report) (inserted bool, err error) {
	var descO, descOL, descT, descTL *string
	if r.Description != nil {
		descO = &r.Description.Original
		ol := r.Description.OriginalLang
		descOL = &ol
		if t := r.Description.Translated; t != "" && t != r.Description.Original {
			descT = &t // store only a genuine translation; "" / echo-of-original => NULL
		}
		descTL = r.Description.TranslatedLang
	}
	photos, _ := json.Marshal(r.Photos)
	if len(photos) == 0 {
		photos = []byte("[]")
	}
	anon, _ := json.Marshal(r.Anonymization)
	sync := []byte(r.Sync)
	if len(sync) == 0 {
		sync = []byte(`{"type":"Synced"}`)
	}
	var modular any
	if len(r.Modular) > 0 && string(r.Modular) != "null" {
		modular = []byte(r.Modular)
	}
	admin := []byte("{}")
	if r.Admin != nil {
		if b, err := json.Marshal(r.Admin); err == nil {
			admin = b
		}
	}
	// crisis_id is nullable: empty => pending (NULL), so the FK to crises holds.
	var crisisID any
	if r.CrisisID != "" {
		crisisID = r.CrisisID
	}
	now := time.Now().UTC()
	if r.CreatedAt.IsZero() {
		r.CreatedAt = now
	}
	if r.UpdatedAt.IsZero() {
		r.UpdatedAt = now
	}
	if r.Clusters == nil {
		r.Clusters = []string{}
	}

	// H3 cell for hotspot aggregation + the h3id export column. NULL for a
	// location-unresolved report (no point to index). Computed on EVERY insert path
	// (submit, seed) so the h3_r8 column is always populated for resolved reports.
	var h3cell any
	if r.Lat != nil && r.Lng != nil {
		if c := H3CellR8(*r.Lat, *r.Lng); c != "" {
			h3cell = c
		}
	}

	// r.Lng/r.Lat are *float64; pgx encodes nil → NULL, and the geom expression's
	// CASE leaves geom NULL when either is NULL (a location-unresolved report).
	tag, err := q.Exec(ctx, insertReportSQL,
		r.ID, r.IdempotencyKey, crisisID, r.SubmitterID, r.Damage, r.PossiblyDamaged, r.Verification, r.Debris,
		r.InfraTypes, r.InfraOtherDetail, r.InfraName, r.CrisisNature, r.Lng, r.Lat, r.GPSAccuracyMeters,
		r.BuildingID, r.BuildingSource,
		r.Version, r.SupersedesReportID, r.PlusCode, r.Landmark, r.Place,
		descO, descOL, descT, descTL,
		r.AILevel, r.AIConfidence, photos, r.SizeBytes, modular, anon,
		r.IsMine, r.Synced, sync, r.CapturedAt, r.CreatedAt, r.UpdatedAt, admin,
		r.Clusters,
		r.LocationResolved,
		h3cell,
	)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// UpsertBuilding ensures the building row exists and refreshes its cached damage.
func UpsertBuilding(ctx context.Context, q querier, b model.Building) error {
	var lng, lat any
	if b.Lng != nil {
		lng = *b.Lng
	}
	if b.Lat != nil {
		lat = *b.Lat
	}
	var crisisID any // nullable: empty => pending building (no crisis yet)
	if b.CrisisID != "" {
		crisisID = b.CrisisID
	}
	_, err := q.Exec(ctx, `
		INSERT INTO buildings (id, crisis_id, geom, lat, lng, current_damage)
		VALUES ($1,$2,
		        CASE WHEN $3::float8 IS NULL THEN NULL ELSE ST_SetSRID(ST_MakePoint($3,$4),4326) END,
		        $4,$3,$5)
		ON CONFLICT (id) DO UPDATE SET current_damage = COALESCE(EXCLUDED.current_damage, buildings.current_damage)`,
		b.ID, crisisID, lng, lat, b.CurrentDamage)
	return err
}

// NextVersionForBuilding locks the building row, then returns the next version
// number and the latest prior report id (to be superseded). Call inside a tx.
func NextVersionForBuilding(ctx context.Context, tx pgx.Tx, buildingID string) (version int, supersedes *string, err error) {
	// Lock the building so concurrent submits serialize on it.
	_, _ = tx.Exec(ctx, "SELECT 1 FROM buildings WHERE id = $1 FOR UPDATE", buildingID)
	var count int
	var latest *string
	err = tx.QueryRow(ctx, `
		SELECT count(*),
		       (SELECT id FROM reports WHERE building_id = $1 ORDER BY captured_at DESC, id DESC LIMIT 1)
		FROM reports WHERE building_id = $1`, buildingID).Scan(&count, &latest)
	if err != nil {
		return 0, nil, err
	}
	return count + 1, latest, nil
}

// RefreshBuildingCurrentDamage recomputes the cached damage from the building's
// NEWEST report (by captured_at). Deriving from the data — rather than blindly
// writing the incoming report's damage — keeps current_damage correct regardless
// of submit order, and never regresses on an idempotent replay (which inserts no
// new row, so the latest is unchanged).
func RefreshBuildingCurrentDamage(ctx context.Context, q querier, buildingID string) error {
	_, err := q.Exec(ctx, `
		UPDATE buildings SET current_damage = (
			SELECT damage FROM reports WHERE building_id = $1 ORDER BY captured_at DESC, id DESC LIMIT 1
		) WHERE id = $1`, buildingID)
	return err
}

// ErrPhotoRequired → 409 photo_required at the handler: an analyst tried to mark a
// photo-less report verified without the explicit force=true override.
var ErrPhotoRequired = errors.New("cannot verify a report without a photo")

// UpdateVerification persists an analyst decision and writes an audit row (incl.
// the optional note and whether the photo gate was forced) in one tx. Marking a
// report 'verified' when it has NO photo returns ErrPhotoRequired unless
// force=true — evidence-less verification is almost always an analyst slip.
// Returns nil report if the id doesn't exist.
func (s *Reports) UpdateVerification(ctx context.Context, id, status, actor string, note *string, force bool) (*model.Report, error) {
	var updated *model.Report
	err := RunInTx(ctx, s.pool, func(tx pgx.Tx) error {
		var from string
		var photoURL *string
		err := tx.QueryRow(ctx, "SELECT verification, photo_url FROM reports WHERE id = $1 FOR UPDATE", id).Scan(&from, &photoURL)
		if err != nil {
			if err == pgx.ErrNoRows {
				return nil // updated stays nil
			}
			return err
		}
		if status == "verified" && (photoURL == nil || *photoURL == "") && !force {
			return ErrPhotoRequired
		}
		if _, err := tx.Exec(ctx,
			"INSERT INTO report_verification_audit (report_id, from_status, to_status, actor, note, forced) VALUES ($1,$2,$3,$4,$5,$6)",
			id, from, status, actor, note, force); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			"UPDATE reports SET verification = $2, updated_at = now() WHERE id = $1", id, status); err != nil {
			return err
		}
		row := tx.QueryRow(ctx, "SELECT "+reportSelect+" FROM reports WHERE id = $1", id)
		r, err := scanReport(row)
		if err != nil {
			return err
		}
		updated = &r
		return nil
	})
	return updated, err
}

// ReportCount returns total reports (used by the idempotent seeder gate).
func (s *Reports) ReportCount(ctx context.Context) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, "SELECT count(*) FROM reports").Scan(&n)
	return n, err
}
