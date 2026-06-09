package store

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stepanok/beacon-server/internal/model"
)

type Admin struct{ pool *pgxpool.Pool }

func NewAdmin(pool *pgxpool.Pool) *Admin { return &Admin{pool: pool} }

// UpsertAdminArea inserts a reference admin area from a bounding box (or geom=NULL
// for upper-level refs that only exist to be walked to as parents). source/iso3
// record provenance (see migration 00011).
func UpsertAdminArea(ctx context.Context, q querier, pcode string, level int, name string, parent *string, iso3, source string, bbox *[4]float64) error {
	if bbox == nil {
		_, err := q.Exec(ctx,
			"INSERT INTO admin_areas (pcode, level, name, parent_pcode, iso3, source, geom) VALUES ($1,$2,$3,$4,$5,$6,NULL) ON CONFLICT (pcode) DO NOTHING",
			pcode, level, name, parent, iso3, source)
		return err
	}
	b := *bbox
	_, err := q.Exec(ctx,
		"INSERT INTO admin_areas (pcode, level, name, parent_pcode, iso3, source, geom) VALUES ($1,$2,$3,$4,$5,$6, ST_MakeEnvelope($7,$8,$9,$10,4326)) ON CONFLICT (pcode) DO NOTHING",
		pcode, level, name, parent, iso3, source, b[0], b[1], b[2], b[3])
	return err
}

// UpsertAdminAreaGeoJSON inserts a real boundary polygon from a GeoJSON geometry
// (geoBoundaries / Natural Earth). The geometry is repaired (ST_MakeValid) and
// normalized to MultiPolygon so ST_Contains is reliable. ON CONFLICT keeps the
// existing row (idempotent re-loads); a higher-precedence source replacing geom is
// a future concern handled by the source rank in ResolveAdmin.
func UpsertAdminAreaGeoJSON(ctx context.Context, q querier, pcode string, level int, name string, parent *string, iso3, source, version string, geomJSON []byte) error {
	_, err := q.Exec(ctx, `
		INSERT INTO admin_areas (pcode, level, name, parent_pcode, iso3, source, source_version, geom)
		VALUES ($1,$2,$3,$4,$5,$6,$7, ST_Multi(ST_MakeValid(ST_SetSRID(ST_GeomFromGeoJSON($8),4326))))
		ON CONFLICT (pcode) DO NOTHING`,
		pcode, level, name, parent, iso3, source, version, string(geomJSON))
	return err
}

// AreaCount gates idempotent admin seeding.
func (s *Admin) AreaCount(ctx context.Context) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, "SELECT count(*) FROM admin_areas").Scan(&n)
	if err == pgx.ErrNoRows {
		return 0, nil
	}
	return n, err
}

// AreaCountByISO3 gates the per-country lazy loader: how many areas of a given
// source we already hold for a country (so we fetch each country at most once).
func (s *Admin) AreaCountByISO3(ctx context.Context, iso3, source string) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx,
		"SELECT count(*) FROM admin_areas WHERE iso3 = $1 AND source = $2", iso3, source).Scan(&n)
	if err == pgx.ErrNoRows {
		return 0, nil
	}
	return n, err
}

// ResolveAdmin reverse-geocodes a point to its admin chain: it finds the deepest
// area whose polygon contains the point, then walks the parent chain up to ADM0.
// At equal depth, a more authoritative source wins (seed/COD over geoBoundaries
// over the coarse Natural Earth baseline). Returns nil if the point falls outside
// all known boundaries.
func (s *Admin) ResolveAdmin(ctx context.Context, lng, lat float64) (*model.AdminChain, error) {
	rows, err := s.pool.Query(ctx, `
		WITH RECURSIVE seed AS (
			SELECT pcode, level, name, parent_pcode
			FROM admin_areas
			WHERE geom IS NOT NULL
			  AND ST_Contains(geom, ST_SetSRID(ST_MakePoint($1,$2),4326))
			ORDER BY level DESC,
			         (CASE source WHEN 'cod' THEN 4 WHEN 'seed' THEN 3 WHEN 'geoboundaries' THEN 2 ELSE 1 END) DESC
			LIMIT 1
		),
		hit AS (
			SELECT pcode, level, name, parent_pcode FROM seed
			UNION ALL
			SELECT a.pcode, a.level, a.name, a.parent_pcode
			FROM admin_areas a JOIN hit ON a.pcode = hit.parent_pcode
		)
		SELECT pcode, level, name FROM hit`, lng, lat)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	chain := &model.AdminChain{}
	found := false
	for rows.Next() {
		var pcode, name string
		var level int
		if err := rows.Scan(&pcode, &level, &name); err != nil {
			return nil, err
		}
		found = true
		ref := &model.AdminRef{Pcode: pcode, Name: name}
		switch level {
		case 0:
			chain.Adm0 = ref
		case 1:
			chain.Adm1 = ref
		case 2:
			chain.Adm2 = ref
		case 3:
			chain.Adm3 = ref
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if !found {
		return nil, nil
	}
	return chain, nil
}

// PointRow is a report's id + coordinates for re-geocoding.
type PointRow struct {
	ID  string
	Lng float64
	Lat float64
}

// ReportsMissingRegion returns reports that have no resolved ADM1 yet (admin '{}'
// or country-only) — the set a freshly-loaded country's boundaries can now fill.
// Coordinates come from the stored point geometry (ST_X=lng, ST_Y=lat).
func (s *Admin) ReportsMissingRegion(ctx context.Context) ([]PointRow, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, ST_X(geom), ST_Y(geom) FROM reports
		 WHERE geom IS NOT NULL AND (admin->'adm1') IS NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PointRow
	for rows.Next() {
		var p PointRow
		if err := rows.Scan(&p.ID, &p.Lng, &p.Lat); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// UpdateReportAdmin re-stamps a report's admin chain. The adm1/2/3_pcode generated
// columns follow automatically from the jsonb (migration 00003).
func (s *Admin) UpdateReportAdmin(ctx context.Context, id string, chain *model.AdminChain) error {
	b, err := json.Marshal(chain)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, "UPDATE reports SET admin = $1 WHERE id = $2", b, id)
	return err
}
