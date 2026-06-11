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
			-- Authority FIRST, then depth: the deepest area of the most authoritative source wins.
			-- (COD-AB has official P-codes but, for some countries, only to ADM2 — it must still beat
			-- an illustrative seed ADM3, so source rank cannot be subordinate to level.)
			ORDER BY (CASE source WHEN 'cod' THEN 4 WHEN 'seed' THEN 3 WHEN 'geoboundaries' THEN 2 ELSE 1 END) DESC,
			         level DESC
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

// ReportsInCountry returns every report whose point falls inside the given country (its ADM0
// baseline polygon) — the set to RE-geocode after a higher-authority layer (COD-AB) loads, so
// reports already tagged via geoBoundaries/seed are upgraded to official P-codes + a deeper ADM2.
func (s *Admin) ReportsInCountry(ctx context.Context, iso3 string) ([]PointRow, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT r.id, ST_X(r.geom), ST_Y(r.geom)
		 FROM reports r
		 JOIN admin_areas a ON a.pcode = $1 AND a.level = 0
		 WHERE r.geom IS NOT NULL AND ST_Contains(a.geom, r.geom)`, iso3)
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

// ReportedCountries returns the distinct ISO3 codes of countries that have at least one report,
// resolved via the Natural Earth ADM0 baseline (the canonical point→ISO3 layer) — NOT the report's
// stamped adm0, which may be a seed/2-letter code (e.g. "TR"), not a 3-letter ISO3 ("TUR").
func (s *Admin) ReportedCountries(ctx context.Context) ([]string, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT DISTINCT a.iso3 FROM reports r
		 JOIN admin_areas a ON a.level = 0 AND a.source = 'naturalearth' AND ST_Contains(a.geom, r.geom)
		 WHERE r.geom IS NOT NULL AND a.iso3 IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var iso3 string
		if err := rows.Scan(&iso3); err != nil {
			return nil, err
		}
		if iso3 != "" {
			out = append(out, iso3)
		}
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
