package store

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stepanok/beacon-server/internal/model"
)

// reportSelect lists every column read for a Report, plus a SQL-computed ageMin
// (minutes since capture, relative to now() — live data ages, seed parity holds
// at seed time because the seeder sets captured_at = now()-ageMin).
const reportSelect = `
  id, idempotency_key, COALESCE(crisis_id, '') AS crisis_id, submitter_id::text, damage, possibly_damaged, verification, debris,
  infra_types, infra_other_detail, infra_name, crisis_nature, lat, lng, gps_accuracy_m, building_id, building_source,
  version, supersedes_report_id, plus_code, landmark, place, photo_url,
  desc_original, desc_original_lang, desc_translated, desc_translated_lang,
  ai_level, ai_confidence, photos, size_bytes, modular, anonymization,
  is_mine, synced, sync_state, captured_at, created_at, updated_at, admin,
  clusters,
  GREATEST(0, FLOOR(EXTRACT(EPOCH FROM (now() - captured_at)) / 60))::int AS age_min,
  damage_tier, location_resolved`

func round1(x float64) float64 { return math.Round(x*10) / 10 }

func scanReport(row pgx.Row) (model.Report, error) {
	var r model.Report
	var (
		submitterID                             *string
		descOriginal, descOriginalLang          *string
		descTranslated, descTranslatedLang      *string
		photosRaw, modularRaw, anonRaw, syncRaw []byte
		adminRaw                                []byte
		lat, lng                                *float64 // nullable: NULL for location-unresolved reports
	)
	err := row.Scan(
		&r.ID, &r.IdempotencyKey, &r.CrisisID, &submitterID, &r.Damage, &r.PossiblyDamaged, &r.Verification, &r.Debris,
		&r.InfraTypes, &r.InfraOtherDetail, &r.InfraName, &r.CrisisNature, &lat, &lng, &r.GPSAccuracyMeters, &r.BuildingID, &r.BuildingSource,
		&r.Version, &r.SupersedesReportID, &r.PlusCode, &r.Landmark, &r.Place, &r.PhotoURL,
		&descOriginal, &descOriginalLang, &descTranslated, &descTranslatedLang,
		&r.AILevel, &r.AIConfidence, &photosRaw, &r.SizeBytes, &modularRaw, &anonRaw,
		&r.IsMine, &r.Synced, &syncRaw, &r.CapturedAt, &r.CreatedAt, &r.UpdatedAt, &adminRaw,
		&r.Clusters, &r.AgeMin,
		&r.DamageTier, &r.LocationResolved,
	)
	if err != nil {
		return model.Report{}, err
	}
	r.Lat, r.Lng = lat, lng
	if r.Clusters == nil {
		r.Clusters = []string{}
	}
	if len(adminRaw) > 0 && string(adminRaw) != "{}" && string(adminRaw) != "null" {
		var ac model.AdminChain
		if json.Unmarshal(adminRaw, &ac) == nil && (ac.Adm0 != nil || ac.Adm1 != nil || ac.Adm2 != nil || ac.Adm3 != nil) {
			r.Admin = &ac
			if ac.Adm1 != nil {
				r.Adm1Pcode = &ac.Adm1.Pcode
			}
			if ac.Adm2 != nil {
				r.Adm2Pcode = &ac.Adm2.Pcode
			}
			if ac.Adm3 != nil {
				r.Adm3Pcode = &ac.Adm3.Pcode
			}
		}
	}

	r.SubmitterID = submitterID
	if descOriginal != nil {
		d := model.ReportDescription{Original: *descOriginal, TranslatedLang: descTranslatedLang}
		if descOriginalLang != nil {
			d.OriginalLang = *descOriginalLang
		}
		if descTranslated != nil && *descTranslated != "" {
			d.Translated = *descTranslated
		} else {
			d.Translated = *descOriginal // coalesce: always present, never empty
		}
		r.Description = &d
	}
	if len(photosRaw) > 0 {
		_ = json.Unmarshal(photosRaw, &r.Photos)
	}
	if r.Photos == nil {
		r.Photos = []model.PhotoRef{}
	}
	if len(modularRaw) > 0 && string(modularRaw) != "null" {
		r.Modular = json.RawMessage(modularRaw)
	}
	if len(anonRaw) > 0 {
		_ = json.Unmarshal(anonRaw, &r.Anonymization)
	}
	// Honesty: face/plate blurring is not implemented; never emit these as true on
	// read, even if an old stored row claims it (migration 00010 also backfills false).
	r.Anonymization.FacesBlurred = false
	r.Anonymization.PlatesBlurred = false
	if len(syncRaw) > 0 {
		r.Sync = json.RawMessage(syncRaw)
	} else {
		r.Sync = json.RawMessage(`{"type":"Synced"}`)
	}
	if r.InfraTypes == nil {
		r.InfraTypes = []string{}
	}
	if r.CrisisNature == nil {
		r.CrisisNature = []string{}
	}

	// Derived / alias projections (superset contract). what3words is a legacy ALIAS
	// of plus_code (the misnamed column was merged away in migration 00014) — both
	// keys emit the same value so existing mobile builds keep working.
	r.SizeMb = round1(float64(r.SizeBytes) / 1e6)
	r.Infra = r.InfraTypes
	r.Crisis = r.CrisisNature
	r.What3Words = r.PlusCode
	r.Location = model.ReportLocation{
		Lat: r.Lat, Lng: r.Lng, BuildingID: r.BuildingID, BuildingSource: r.BuildingSource,
		What3Words: r.PlusCode, PlusCode: r.PlusCode, Landmark: r.Landmark,
		GPSAccuracyMeters: r.GPSAccuracyMeters,
	}
	return r, nil
}

func scanReports(rows pgx.Rows) ([]model.Report, error) {
	defer rows.Close()
	out := []model.Report{}
	for rows.Next() {
		r, err := scanReport(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ── filtering & pagination ─────────────────────────────────────────────

type ListFilter struct {
	CrisisID string
	// CrisisIDs scopes the LIST to multiple crises (a regional analyst's finite scope).
	// Used ONLY when CrisisID == "". Empty/nil CrisisIDs with an empty CrisisID means NO
	// crisis filter at all (org-wide '*' => every crisis). Export keeps using CrisisID only.
	CrisisIDs    []string
	Damage       []string
	Verification []string
	Q            string
	Mine         bool
	SubmitterID  *string // caller identity for mine=true
	BuildingID   *string
	Adm1Pcode    *string
	Adm2Pcode    *string
	Adm3Pcode    *string
	Cluster      *string
	BBox         *[4]float64 // minLng,minLat,maxLng,maxLat
	Limit        int
	Offset       int
	Cursor       *Cursor
}

type Cursor struct {
	CapturedAt time.Time
	ID         string
}

func EncodeCursor(c Cursor) string {
	return base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf("%d|%s", c.CapturedAt.UnixNano(), c.ID)))
}

func DecodeCursor(s string) (*Cursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, err
	}
	parts := strings.SplitN(string(raw), "|", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("bad cursor")
	}
	ns, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return nil, err
	}
	return &Cursor{CapturedAt: time.Unix(0, ns).UTC(), ID: parts[1]}, nil
}

// whereClause builds the dynamic WHERE for filters, returning the SQL fragment
// and the positional args. startArg is the first $N to use.
func (f ListFilter) whereClause(startArg int) (string, []any) {
	conds := []string{}
	args := []any{}
	n := startArg
	add := func(cond string, v any) {
		conds = append(conds, fmt.Sprintf(cond, n))
		args = append(args, v)
		n++
	}
	if f.CrisisID != "" {
		add("crisis_id = $%d", f.CrisisID)
	} else if len(f.CrisisIDs) > 0 {
		// Scope-limited 'all' (regional analyst): restrict to the caller's crises.
		// Empty CrisisID + empty CrisisIDs intentionally adds NO crisis filter
		// (org-wide '*' => every crisis).
		add("crisis_id = ANY($%d)", f.CrisisIDs)
	}
	if len(f.Damage) > 0 {
		add("damage = ANY($%d)", f.Damage)
	}
	if len(f.Verification) > 0 {
		add("verification = ANY($%d)", f.Verification)
	}
	if f.Q != "" {
		// place ILIKE OR id prefix OR infra contains — trigram-backed on place.
		conds = append(conds, fmt.Sprintf("(place ILIKE '%%' || $%d || '%%' OR id ILIKE $%d || '%%' OR $%d = ANY(infra_types))", n, n, n))
		args = append(args, f.Q)
		n++
	}
	if f.Mine && f.SubmitterID != nil {
		add("submitter_id = $%d::uuid", *f.SubmitterID) // cast: SubmitterID is the uuid as text; keeps idx_reports_submitter
	}
	if f.BuildingID != nil {
		add("building_id = $%d", *f.BuildingID)
	}
	if f.Adm1Pcode != nil {
		add("adm1_pcode = $%d", *f.Adm1Pcode)
	}
	if f.Adm2Pcode != nil {
		add("adm2_pcode = $%d", *f.Adm2Pcode)
	}
	if f.Adm3Pcode != nil {
		add("adm3_pcode = $%d", *f.Adm3Pcode)
	}
	if f.Cluster != nil {
		add("$%d = ANY(clusters)", *f.Cluster)
	}
	if f.BBox != nil {
		b := *f.BBox
		conds = append(conds, fmt.Sprintf("geom && ST_MakeEnvelope($%d,$%d,$%d,$%d,4326)", n, n+1, n+2, n+3))
		args = append(args, b[0], b[1], b[2], b[3])
		n += 4
	}
	if len(conds) == 0 {
		return "", args
	}
	return "WHERE " + strings.Join(conds, " AND "), args
}

// List returns a page of reports (keyset by captured_at DESC, id DESC), the
// filtered total, and the unfiltered crisis grand total.
func (s *Reports) List(ctx context.Context, f ListFilter) (items []model.Report, total, grandTotal int, nextCursor *string, err error) {
	where, args := f.whereClause(1)

	// items query: optional keyset cursor predicate appended.
	itemSQL := "SELECT " + reportSelect + " FROM reports " + where
	keysetArgs := append([]any{}, args...)
	if f.Cursor != nil {
		n := len(keysetArgs) + 1
		pred := fmt.Sprintf("(captured_at, id) < ($%d, $%d)", n, n+1)
		if where == "" {
			itemSQL += " WHERE " + pred
		} else {
			itemSQL += " AND " + pred
		}
		keysetArgs = append(keysetArgs, f.Cursor.CapturedAt, f.Cursor.ID)
	}
	itemSQL += " ORDER BY captured_at DESC, id DESC"
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	itemSQL += fmt.Sprintf(" LIMIT %d", limit+1)
	if f.Cursor == nil && f.Offset > 0 {
		itemSQL += fmt.Sprintf(" OFFSET %d", f.Offset)
	}

	rows, err := s.pool.Query(ctx, itemSQL, keysetArgs...)
	if err != nil {
		return nil, 0, 0, nil, err
	}
	items, err = scanReports(rows)
	if err != nil {
		return nil, 0, 0, nil, err
	}
	if len(items) > limit {
		last := items[limit-1]
		items = items[:limit]
		c := EncodeCursor(Cursor{CapturedAt: last.CapturedAt, ID: last.ID})
		nextCursor = &c
	}

	// filtered total
	if err = s.pool.QueryRow(ctx, "SELECT count(*) FROM reports "+where, args...).Scan(&total); err != nil {
		return nil, 0, 0, nil, err
	}
	// grand total = unfiltered count within the LIST's crisis scope (single crisis,
	// the caller's finite scope, or — org-wide — every report). It deliberately
	// ignores the per-request damage/verification/q/etc filters so the UI can show
	// "showing N of GRAND".
	switch {
	case f.CrisisID != "":
		if err = s.pool.QueryRow(ctx, "SELECT count(*) FROM reports WHERE crisis_id = $1", f.CrisisID).Scan(&grandTotal); err != nil {
			return nil, 0, 0, nil, err
		}
	case len(f.CrisisIDs) > 0:
		if err = s.pool.QueryRow(ctx, "SELECT count(*) FROM reports WHERE crisis_id = ANY($1)", f.CrisisIDs).Scan(&grandTotal); err != nil {
			return nil, 0, 0, nil, err
		}
	default:
		if err = s.pool.QueryRow(ctx, "SELECT count(*) FROM reports").Scan(&grandTotal); err != nil {
			return nil, 0, 0, nil, err
		}
	}
	return items, total, grandTotal, nextCursor, nil
}

func (s *Reports) GetByID(ctx context.Context, id string) (*model.Report, error) {
	row := s.pool.QueryRow(ctx, "SELECT "+reportSelect+" FROM reports WHERE id = $1", id)
	r, err := scanReport(row)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &r, nil
}

func (s *Reports) GetByIdempotencyKey(ctx context.Context, key string) (*model.Report, error) {
	row := s.pool.QueryRow(ctx, "SELECT "+reportSelect+" FROM reports WHERE idempotency_key = $1", key)
	r, err := scanReport(row)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &r, nil
}

// SetPhotoURL records the URL of an uploaded report photo. Returns whether the report exists.
func (s *Reports) SetPhotoURL(ctx context.Context, id, url string) (bool, error) {
	tag, err := s.pool.Exec(ctx, "UPDATE reports SET photo_url = $2, updated_at = now() WHERE id = $1", id, url)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// CrisisIDOf returns the (possibly empty) crisis_id a report belongs to, plus
// found=false if the report does not exist. crisis_id is nullable (pending
// reports have none) so an empty string is a valid, found result. Used to scope
// analyst mutations (verification/task) to the caller's crisis.
func (s *Reports) CrisisIDOf(ctx context.Context, id string) (crisisID string, found bool, err error) {
	err = s.pool.QueryRow(ctx, "SELECT COALESCE(crisis_id, '') FROM reports WHERE id = $1", id).Scan(&crisisID)
	if err != nil {
		if err == pgx.ErrNoRows {
			return "", false, nil
		}
		return "", false, err
	}
	return crisisID, true, nil
}

// PhotoServeInfo returns the facts the photo SERVE path needs to authorize a
// download: whether the report exists, its verification state (anonymous callers may
// only fetch verified photos), and its crisis_id (to scope authenticated analysts).
// found=false => no such report.
func (s *Reports) PhotoServeInfo(ctx context.Context, id string) (found bool, verification, crisisID string, err error) {
	err = s.pool.QueryRow(ctx,
		"SELECT verification, COALESCE(crisis_id, '') FROM reports WHERE id = $1", id).
		Scan(&verification, &crisisID)
	if err != nil {
		if err == pgx.ErrNoRows {
			return false, "", "", nil
		}
		return false, "", "", err
	}
	return true, verification, crisisID, nil
}

// PhotoUploadAuth is what the photo handler needs to decide whether an anonymous
// upload is allowed for a report (see handler.UploadPhoto).
type PhotoUploadAuth struct {
	Found      bool
	DeviceID   *string   // the creating device's anonymous id (submitters.anonymous_id), nil if none stored
	HasPhoto   bool      // a photo_url is already recorded
	CapturedAt time.Time // capture time (newest of captured_at/created_at) for the recency gate
}

// PhotoUploadInfo loads the facts needed to authorize an anonymous photo upload:
// the creating device id (resolved submitter_id -> submitters.anonymous_id), whether
// a photo already exists, and the capture/creation time for the recency fallback.
func (s *Reports) PhotoUploadInfo(ctx context.Context, id string) (PhotoUploadAuth, error) {
	var a PhotoUploadAuth
	var dev *string
	var photoURL *string
	var captured, created time.Time
	err := s.pool.QueryRow(ctx, `
		SELECT (SELECT anonymous_id FROM submitters s WHERE s.id = r.submitter_id),
		       r.photo_url, r.captured_at, r.created_at
		FROM reports r WHERE r.id = $1`, id).Scan(&dev, &photoURL, &captured, &created)
	if err != nil {
		if err == pgx.ErrNoRows {
			return PhotoUploadAuth{Found: false}, nil
		}
		return PhotoUploadAuth{}, err
	}
	a.Found = true
	a.DeviceID = dev
	a.HasPhoto = photoURL != nil && *photoURL != ""
	a.CapturedAt = captured
	if created.After(captured) {
		a.CapturedAt = created
	}
	return a, nil
}

// WithdrawOutcome reports the result of a reporter-initiated takedown.
type WithdrawOutcome struct {
	Found    bool
	Owned    bool    // the caller's device matches the report's creating device
	PhotoURL *string // set when a photo was attached, so the handler can erase the file too
}

// WithdrawReport ERASES a report at its reporter's request (data-subject takedown). In
// one transaction it verifies the caller is the creating device (submitters.anonymous_id),
// nulls any inbound supersedes references (the self-FK has no ON DELETE), deletes the
// report (the verification-audit rows cascade), and records a NON-PII row in
// report_withdrawals for accountability. The report is truly erased — it vanishes from
// every read path. callerDevice is the caller's X-Device-Id. Returns Found=false (→404)
// or Owned=false (→403) WITHOUT mutating anything; a report with no recorded device can
// never be proven-owned, so it is never withdrawable anonymously.
func (s *Reports) WithdrawReport(ctx context.Context, id, callerDevice string) (WithdrawOutcome, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return WithdrawOutcome{}, err
	}
	defer tx.Rollback(ctx)

	var dev, photoURL, submitterID *string
	var crisisID string
	err = tx.QueryRow(ctx, `
		SELECT (SELECT anonymous_id FROM submitters s WHERE s.id = r.submitter_id),
		       r.photo_url, r.submitter_id::text, COALESCE(r.crisis_id, '')
		FROM reports r WHERE r.id = $1 FOR UPDATE`, id).Scan(&dev, &photoURL, &submitterID, &crisisID)
	if err != nil {
		if err == pgx.ErrNoRows {
			return WithdrawOutcome{Found: false}, nil
		}
		return WithdrawOutcome{}, err
	}
	if dev == nil || *dev == "" || callerDevice == "" || callerDevice != *dev {
		return WithdrawOutcome{Found: true, Owned: false}, nil
	}
	if _, err = tx.Exec(ctx, "UPDATE reports SET supersedes_report_id = NULL WHERE supersedes_report_id = $1", id); err != nil {
		return WithdrawOutcome{}, err
	}
	if _, err = tx.Exec(ctx, "DELETE FROM reports WHERE id = $1", id); err != nil {
		return WithdrawOutcome{}, err
	}
	if _, err = tx.Exec(ctx,
		"INSERT INTO report_withdrawals (report_id, submitter_id, crisis_id) VALUES ($1, $2::uuid, NULLIF($3, ''))",
		id, submitterID, crisisID); err != nil {
		return WithdrawOutcome{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return WithdrawOutcome{}, err
	}
	return WithdrawOutcome{Found: true, Owned: true, PhotoURL: photoURL}, nil
}

// LatestPerBuilding returns one report per building (max captured_at) plus all
// building-less reports, sorted newest first — the map-pin set. Scoping is by
// crisis (when crisisID != "") AND/OR viewport bbox — so a location-first client
// in one region never touches another region's rows (GIST-indexed). verifiedOnly
// restricts to verified reports (the public/anonymous map visibility tier).
// At least one of crisisID or bbox must be set (never an unbounded full scan).
func (s *Reports) LatestPerBuilding(ctx context.Context, crisisID string, bbox *[4]float64, verifiedOnly bool) ([]model.Report, error) {
	if crisisID == "" && bbox == nil {
		return nil, fmt.Errorf("latest-per-building requires crisisId or bbox")
	}
	conds := []string{}
	args := []any{}
	n := 1
	if crisisID != "" {
		conds = append(conds, fmt.Sprintf("crisis_id = $%d", n))
		args = append(args, crisisID)
		n++
	}
	if bbox != nil {
		b := *bbox
		conds = append(conds, fmt.Sprintf("geom && ST_MakeEnvelope($%d,$%d,$%d,$%d,4326)", n, n+1, n+2, n+3))
		args = append(args, b[0], b[1], b[2], b[3])
		n += 4
	}
	if verifiedOnly {
		conds = append(conds, "verification = 'verified'")
	}
	inner := "SELECT r.*, row_number() OVER (PARTITION BY building_id ORDER BY captured_at DESC, id DESC) AS rn FROM reports r WHERE " + strings.Join(conds, " AND ")
	sql := "SELECT " + reportSelect + " FROM (" + inner + ") q WHERE q.building_id IS NULL OR q.rn = 1 ORDER BY captured_at DESC, id DESC"
	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return scanReports(rows)
}

// publicTileSnapDegrees is the grid step (in WGS84 degrees, ~110m at the equator)
// that the PUBLIC tile tier snaps point geometry to, matching the ~3-decimal
// coarsening used by handler.publicProjection. Keeping these consistent means a
// report's exact position is never recoverable from the public vector tiles either.
const publicTileSnapDegrees = 0.001

// MapTileMVT renders a Mapbox Vector Tile (z/x/y) of reports — the scalable map
// source. At high zoom (z >= 13) it returns latest-per-building POINTS; at low zoom
// it returns grid-aggregated CLUSTER counts (so a 500k-report crisis never ships
// 500k features to a client). Scoped by crisis. `publicTier` is the anonymous/public
// visibility tier: it restricts to VERIFIED reports AND snaps point geometry to the
// public ~110m grid (consistent with publicProjection) so exact reporter positions
// are never emitted at high zoom. In-scope analysts (publicTier=false) keep exact
// geometry and all statuses. Returns raw .mvt protobuf bytes.
func (s *Reports) MapTileMVT(ctx context.Context, z, x, y int, crisisID string, publicTier bool) ([]byte, error) {
	conds := "r.geom && ST_Transform(b.b3857, 4326)"
	var mvt []byte
	if z >= 13 {
		args := []any{z, x, y}
		n := 4
		if crisisID != "" {
			conds += fmt.Sprintf(" AND r.crisis_id = $%d", n)
			args = append(args, crisisID)
			n++
		}
		if publicTier {
			conds += " AND r.verification = 'verified'"
		}
		// Public tier: snap the point to the ~110m public grid (in 4326) BEFORE the
		// 3857 transform so high-zoom tiles never expose an exact reporter position.
		// Analysts keep the exact geometry.
		geomExpr := "ST_Transform(latest.geom,3857)"
		if publicTier {
			geomExpr = fmt.Sprintf("ST_Transform(ST_SnapToGrid(latest.geom, %g), 3857)", publicTileSnapDegrees)
		}
		sql := `
			WITH b AS (SELECT ST_TileEnvelope($1,$2,$3) AS b3857),
			src AS (
			  SELECT r.id, r.damage, r.verification, r.geom, r.building_id,
			         row_number() OVER (PARTITION BY r.building_id ORDER BY r.captured_at DESC, r.id DESC) AS rn
			  FROM reports r, b WHERE ` + conds + `),
			latest AS (SELECT * FROM src WHERE building_id IS NULL OR rn = 1),
			mvtgeom AS (
			  SELECT ST_AsMVTGeom(` + geomExpr + `, b.b3857, 4096, 64, true) AS geom,
			         latest.id, latest.damage, latest.verification
			  FROM latest, b)
			SELECT COALESCE(ST_AsMVT(mvtgeom.*, 'reports', 4096, 'geom'), ''::bytea)
			FROM mvtgeom WHERE geom IS NOT NULL`
		if err := s.pool.QueryRow(ctx, sql, args...).Scan(&mvt); err != nil {
			return nil, err
		}
		return mvt, nil
	}

	// low zoom → aggregate into a grid (~32 cells across the tile)
	cell := 40075016.686 / math.Pow(2, float64(z)) / 32.0
	args := []any{z, x, y, cell}
	n := 5
	if crisisID != "" {
		conds += fmt.Sprintf(" AND r.crisis_id = $%d", n)
		args = append(args, crisisID)
		n++
	}
	if publicTier {
		conds += " AND r.verification = 'verified'"
	}
	sql := `
		WITH b AS (SELECT ST_TileEnvelope($1,$2,$3) AS b3857),
		pts AS (
		  SELECT ST_Transform(r.geom,3857) AS g,
		         CASE r.damage_tier WHEN 'complete' THEN 2 WHEN 'partial' THEN 1 ELSE 0 END AS rank
		  FROM reports r, b WHERE ` + conds + `),
		grid AS (
		  SELECT ST_SnapToGrid(g, $4) AS cell, count(*)::int AS n, max(rank) AS worst
		  FROM pts GROUP BY ST_SnapToGrid(g, $4)),
		mvtgeom AS (
		  SELECT ST_AsMVTGeom(grid.cell, b.b3857, 4096, 0, true) AS geom, grid.n, grid.worst
		  FROM grid, b)
		SELECT COALESCE(ST_AsMVT(mvtgeom.*, 'clusters', 4096, 'geom'), ''::bytea)
		FROM mvtgeom WHERE geom IS NOT NULL`
	if err := s.pool.QueryRow(ctx, sql, args...).Scan(&mvt); err != nil {
		return nil, err
	}
	return mvt, nil
}

// BuildingTimeline returns the real per-building version history (asc by capture).
// verifiedOnly restricts the chain to verified entries — the public/anonymous
// visibility tier — so an anonymous caller can never enumerate pending/flagged
// reports for a building. Note/reporter stripping for the public tier is applied by
// the handler (it owns the projection policy).
func (s *Reports) BuildingTimeline(ctx context.Context, buildingID string, verifiedOnly bool) (*model.BuildingTimeline, error) {
	cond := ""
	if verifiedOnly {
		cond = " AND verification = 'verified'"
	}
	sql := `SELECT id, damage, captured_at,
	               GREATEST(0, FLOOR(EXTRACT(EPOCH FROM (now() - captured_at)) / 60))::int AS age_min,
	               version, COALESCE(desc_translated, desc_original, '') AS note,
	               COALESCE((SELECT anonymous_id FROM submitters s WHERE s.id = r.submitter_id), 'community') AS by
	        FROM reports r WHERE building_id = $1` + cond + ` ORDER BY captured_at ASC, id ASC`
	rows, err := s.pool.Query(ctx, sql, buildingID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tl := &model.BuildingTimeline{BuildingID: buildingID, Versions: []model.BuildingVersion{}}
	for rows.Next() {
		var v model.BuildingVersion
		if err := rows.Scan(&v.ReportID, &v.Damage, &v.At, &v.AgeMin, &v.V, &v.Note, &v.By); err != nil {
			return nil, err
		}
		tl.Versions = append(tl.Versions, v)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(tl.Versions) == 0 {
		return nil, nil
	}
	// mark current = last (latest capture)
	last := &tl.Versions[len(tl.Versions)-1]
	last.IsCurrent = true
	tl.Current = &last.Damage
	return tl, nil
}

// AreaGroups groups reports by place with the worst damage in each, count desc.
// "Worst" is ranked by the 3-tier rollup (minimal<partial<complete) via damage_tier,
// NOT the 5-level grade — otherwise a tier-3 'partial'/'complete' report (the default
// capture scale) would rank as 0 and a true worst could be missed. The returned
// `worst` is the raw grade of the worst-tier report; `worstTier` is its rollup tier.
// A stable tie-break on the raw grade keeps results deterministic across scales.
// verifiedOnly restricts the counts to verified reports — the public/anonymous
// visibility tier — so the public aggregate stays coherent with the verified-only
// public map/pins (a pending/flagged report is never countable from the anon tier).
func (s *Reports) AreaGroups(ctx context.Context, crisisID string, verifiedOnly bool) ([]model.AreaGroup, error) {
	cond := ""
	if verifiedOnly {
		cond = " AND verification = 'verified'"
	}
	sql := `
		WITH ranked AS (
		  SELECT place,
		         damage, damage_tier,
		         CASE damage_tier WHEN 'complete' THEN 2 WHEN 'partial' THEN 1 ELSE 0 END AS tier_rank
		  FROM reports WHERE crisis_id = $1 AND place <> ''` + cond + `
		)
		SELECT place AS area, count(*) AS cnt,
		       (ARRAY_AGG(damage      ORDER BY tier_rank DESC, damage DESC))[1] AS worst,
		       (ARRAY_AGG(damage_tier ORDER BY tier_rank DESC, damage DESC))[1] AS worst_tier
		FROM ranked
		GROUP BY place ORDER BY cnt DESC, area ASC`
	rows, err := s.pool.Query(ctx, sql, crisisID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.AreaGroup{}
	for rows.Next() {
		var g model.AreaGroup
		if err := rows.Scan(&g.Area, &g.Count, &g.Worst, &g.WorstTier); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// Reports is the reports SQL store.
type Reports struct{ pool *pgxpool.Pool }

func NewReports(pool *pgxpool.Pool) *Reports { return &Reports{pool: pool} }

// CountRecentBySubmitter returns how many reports the given submitter has CREATED
// (by created_at, server clock) within the trailing window. This is the durable,
// restart-proof backstop for the per-device submit rate limit (anti-abuse): it
// counts rows already committed to the DB, so it survives process restarts and
// cannot be bypassed by a fresh in-memory bucket. created_at (not the
// client-supplied captured_at) is used so a bad actor cannot dodge the limit by
// back-dating captures.
func (s *Reports) CountRecentBySubmitter(ctx context.Context, submitterID string, since time.Time) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx,
		"SELECT count(*) FROM reports WHERE submitter_id = $1::uuid AND created_at >= $2",
		submitterID, since).Scan(&n)
	return n, err
}

// FindDuplicateBySubmitter detects a genuine near-duplicate from the SAME submitter
// that is NOT just an idempotency replay (same id / idempotency-key is already
// handled by the UPSERT): another report submitted within dupRadiusMeters and
// dupWindowSeconds of this one. Candidates exclude FOOTPRINT reports
// (building_source='footprint', or the legacy "fp-" id prefix) on the prior side:
// re-reporting a tapped footprint is the per-building version chain's job, never a
// duplicate. Building-less pins AND synthetic GPS-grid "b-" building ids — which
// are derived from the pin itself — both count as candidates (the caller applies
// the same footprint exemption to the INCOMING report, see service.isFootprintReport).
//
// It returns the existing report id (newest match) so the caller can reject with
// 409 referencing it. excludeID skips the row currently being inserted.
// found=false => not a dup.
func (s *Reports) FindDuplicateBySubmitter(
	ctx context.Context, submitterID string,
	lat, lng float64, capturedAt time.Time, dupRadiusMeters, dupWindowSeconds float64, excludeID string,
) (existingID string, found bool, err error) {
	pt := "ST_SetSRID(ST_MakePoint($3,$2),4326)::geography"
	// $4 must be cast explicitly: in `$4 - interval` the server would otherwise
	// infer $4 itself as interval, and the statement fails to prepare at all
	// (timestamptz >= interval) — i.e. the guard would 500 every guarded submit.
	err = s.pool.QueryRow(ctx, `
		SELECT id FROM reports
		WHERE submitter_id = $1::uuid AND id <> $6
		  AND (building_id IS NULL
		       OR (COALESCE(building_source, '') <> 'footprint' AND building_id NOT LIKE 'fp-%'))
		  AND captured_at >= $4::timestamptz - make_interval(secs => $5)
		  AND ST_DWithin(geom::geography, `+pt+`, $7)
		ORDER BY captured_at DESC, id DESC LIMIT 1`,
		submitterID, lat, lng, capturedAt, dupWindowSeconds, excludeID, dupRadiusMeters).Scan(&existingID)
	if err != nil {
		if err == pgx.ErrNoRows {
			return "", false, nil
		}
		return "", false, err
	}
	return existingID, true, nil
}
