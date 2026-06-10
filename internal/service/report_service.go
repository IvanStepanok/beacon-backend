// Package service holds business logic: validation, normalization, the idempotent
// submit + per-building versioning transaction, stats assembly and export.
package service

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stepanok/beacon-server/internal/boundary"
	"github.com/stepanok/beacon-server/internal/model"
	"github.com/stepanok/beacon-server/internal/store"
	"github.com/stepanok/beacon-server/internal/translate"
)

// ValidationError → 400/422 at the handler.
type ValidationError struct{ Msg string }

func (e ValidationError) Error() string { return e.Msg }

// RateLimitError → 429 at the handler. Raised when a single device/submitter has
// created too many reports within the rolling window (anti-abuse, Requirement #3).
type RateLimitError struct{ Msg string }

func (e RateLimitError) Error() string { return e.Msg }

// DuplicateError → 409 at the handler. Raised when a submission is a genuine
// near-duplicate of an existing report by the same submitter (anti-abuse). It
// references the pre-existing report so the client can point the user at it.
type DuplicateError struct {
	Msg        string
	ExistingID string
}

func (e DuplicateError) Error() string { return e.Msg }

// Anti-abuse submit guards (Requirement #3). Tuned to be generous for a genuine
// field reporter walking a damaged street, while throttling automated spam.
const (
	// Rate limit: a device/submitter may create at most these many reports per
	// window. Two windows are enforced (both DB-backed): a short burst window and a
	// longer sustained window. Over either → HTTP 429.
	rateLimitBurstMax        = 5                // max reports …
	rateLimitBurstWindow     = time.Minute      // … per this short window
	rateLimitSustainedMax    = 20               // max reports …
	rateLimitSustainedWindow = 10 * time.Minute // … per this longer window

	// Dedup: a pin from the same submitter within this radius AND time of a previous
	// pin is treated as a duplicate (409). Only a REAL tapped footprint is exempt
	// (buildingSource=="footprint", or the legacy "fp-" id prefix): re-reporting a
	// footprint is the per-building version chain's job (NextVersionForBuilding
	// supersedes/merges it). Synthetic GPS-grid "b-" building ids are derived from
	// the pin itself, so they get the same dedup as building-less pins.
	dedupRadiusMeters  = 25.0
	dedupWindowSeconds = 600.0 // 10 minutes
)

type ReportService struct {
	pool       *pgxpool.Pool
	reports    *store.Reports
	admin      *store.Admin
	crises     *store.Crises
	translator *translate.Client
	boundaries *boundary.Loader // nil when boundary loading is disabled
}

func NewReportService(pool *pgxpool.Pool, reports *store.Reports, admin *store.Admin, crises *store.Crises, translator *translate.Client, boundaries *boundary.Loader) *ReportService {
	return &ReportService{pool: pool, reports: reports, admin: admin, crises: crises, translator: translator, boundaries: boundaries}
}

func contains(set []string, v string) bool {
	for _, s := range set {
		if s == v {
			return true
		}
	}
	return false
}

// normalize fills defaults and reconciles flat/nested + alias fields into a Report.
func normalize(req model.SubmitReportRequest, submitterID *string) (model.Report, error) {
	r := model.Report{}
	r.ID = req.ID
	r.IdempotencyKey = req.IdempotencyKey
	if r.IdempotencyKey == "" && r.ID != "" {
		r.IdempotencyKey = "idem-" + r.ID
	}
	// crisis_id is NO LONGER defaulted here. An explicit client value (e.g. when
	// browsing/contributing to a specific crisis) is honored; "auto" or empty means
	// "let the server decide by space+time" — resolved in Submit(). Empty => pending.
	r.CrisisID = req.CrisisID
	if r.CrisisID == "auto" {
		r.CrisisID = ""
	}
	r.SubmitterID = submitterID

	r.Damage = req.Damage
	r.PossiblyDamaged = req.PossiblyDamaged
	r.Debris = req.Debris
	if r.Debris == "" {
		r.Debris = "unsure"
	}
	r.Verification = "pending"

	r.InfraTypes = req.InfraTypes
	if len(r.InfraTypes) == 0 {
		r.InfraTypes = req.Infra // alias
	}
	if r.InfraTypes == nil {
		r.InfraTypes = []string{}
	}
	r.InfraOtherDetail = req.InfraOtherDetail
	r.InfraName = req.InfraName
	r.CrisisNature = req.CrisisNature
	if len(r.CrisisNature) == 0 {
		r.CrisisNature = req.Crisis // alias
	}
	// NO hazard default: an empty crisisNature stays empty — the server must never
	// fabricate an "earthquake" the reporter did not assert.
	if r.CrisisNature == nil {
		r.CrisisNature = []string{}
	}

	// location (C4): a report has EITHER a resolved point (GPS fix or tapped footprint)
	// OR is location-unresolved (landmark-only). NEVER emit 0,0 (Null Island): an
	// unresolved report stores NULL lat/lng + locationResolved=false and keeps the
	// landmark. The caller's locationResolved flag wins; if omitted it defaults to
	// resolved, but the coords must then be present (validated below).
	resolved := true
	if req.LocationResolved != nil {
		resolved = *req.LocationResolved
	}
	// Derive candidate coords from flat lat/lng else nested location.
	var lat, lng *float64
	switch {
	case req.Lat != nil && req.Lng != nil:
		lat, lng = req.Lat, req.Lng
	case req.Location != nil && req.Location.Lat != nil && req.Location.Lng != nil:
		lat, lng = req.Location.Lat, req.Location.Lng
	}
	if resolved && lat != nil && lng != nil {
		r.Lat, r.Lng = lat, lng
		r.LocationResolved = true
	} else {
		// Unresolved (explicit) OR no usable coords supplied: never store 0,0.
		r.Lat, r.Lng = nil, nil
		r.LocationResolved = false
	}
	pick := func(flat *string, nested func() *string) *string {
		if flat != nil {
			return flat
		}
		if req.Location != nil {
			return nested()
		}
		return nil
	}
	r.BuildingID = pick(req.BuildingID, func() *string { return req.Location.BuildingID })
	r.BuildingSource = pick(req.BuildingSource, func() *string { return req.Location.BuildingSource })
	// buildingSource is a TRUST claim, not free metadata: "footprint" exempts the
	// report from the near-dup guard (isFootprintReport), so it must never be stored
	// verbatim. Only the one defined value is accepted; anything else — fabricated
	// strings included — normalizes to nil. The legacy "fp-" id-prefix exemption for
	// older clients is unaffected.
	if r.BuildingSource != nil && *r.BuildingSource != "footprint" {
		r.BuildingSource = nil
	}
	// plusCode is canonical; the legacy what3words key (which always carried a plus
	// code) is accepted as a fallback for older clients. One value is stored
	// (plus_code) and responses emit it under BOTH keys (see store.scanReport).
	r.PlusCode = pick(req.PlusCode, func() *string { return req.Location.PlusCode })
	if r.PlusCode == nil {
		r.PlusCode = pick(req.What3Words, func() *string { return req.Location.What3Words })
	}
	r.What3Words = r.PlusCode
	r.Landmark = pick(req.Landmark, func() *string { return req.Location.Landmark })
	// Accuracy: the C4 wire field `accuracyMeters` is an alias; coalesce
	// accuracyMeters || gpsAccuracyMeters || location.gpsAccuracyMeters.
	r.GPSAccuracyMeters = pick2(req.AccuracyMeters, req.GPSAccuracyMeters, req.Location)

	r.Place = req.Place
	// place sanitation: "Your location" is a client UI placeholder, not a real place
	// name — store the empty value instead (this schema's "no place"; AreaGroups
	// already treats '' as absent).
	if strings.EqualFold(strings.TrimSpace(r.Place), "your location") {
		r.Place = ""
	}
	r.Description = req.Description
	r.AILevel = req.AILevel
	r.AIConfidence = req.AIConfidence
	r.Photos = req.Photos
	if r.Photos == nil {
		r.Photos = []model.PhotoRef{}
	}
	var size int64
	if req.SizeBytes != nil {
		size = *req.SizeBytes
	} else {
		for _, p := range r.Photos {
			size += p.SizeBytes
		}
	}
	r.SizeBytes = size
	r.Modular = req.Modular
	if req.Anonymization != nil {
		r.Anonymization = *req.Anonymization
	} else {
		r.Anonymization = model.DefaultAnonymization()
	}
	// Honesty: face/plate blurring is NOT implemented, so the server must never
	// store/emit these as true — even if a client claims them. ExifStripped is left
	// as supplied (it IS real on mobile).
	r.Anonymization.FacesBlurred = false
	r.Anonymization.PlatesBlurred = false
	r.Sync = req.Sync
	r.Synced = true // it reached the server
	if req.CapturedAt != nil {
		r.CapturedAt = req.CapturedAt.UTC()
	} else {
		r.CapturedAt = time.Now().UTC()
	}
	r.Version = 1

	// Tasking axis: a new report enters the dispatch queue. Life-safety at intake
	// puts it on the fast lane (severity life_safety); analysts can elevate later.
	r.TaskStatus = "new"
	r.LifeSafety = req.LifeSafety
	if req.LifeSafety {
		r.Severity = "life_safety"
	} else {
		r.Severity = "routine"
	}
	r.Clusters = req.Clusters
	if r.Clusters == nil {
		r.Clusters = []string{}
	}
	ref := "ANT-" + r.ID
	if len(r.ID) > 8 {
		ref = "ANT-" + r.ID[:8]
	}
	r.TaskRef = &ref
	return r, nil
}

// pick2 coalesces the GPS accuracy from (in order) the C4 `accuracyMeters` alias,
// the flat `gpsAccuracyMeters`, then the nested location's gpsAccuracyMeters.
func pick2(alias, flat *float64, loc *model.ReportLocation) *float64 {
	if alias != nil {
		return alias
	}
	if flat != nil {
		return flat
	}
	if loc != nil {
		return loc.GPSAccuracyMeters
	}
	return nil
}

// isFootprintReport reports whether a report's building came from a REAL tapped
// footprint polygon: buildingSource=="footprint" (current clients) or the "fp-"
// building-id prefix (legacy clients that predate buildingSource). Synthetic
// GPS-grid "b-" ids — derived from the pin location, not a building tap — and any
// other ids are NOT footprints, so they do not bypass the near-dup guard.
func isFootprintReport(r model.Report) bool {
	if r.BuildingSource != nil && *r.BuildingSource == "footprint" {
		return true
	}
	return r.BuildingID != nil && strings.HasPrefix(*r.BuildingID, "fp-")
}

// pinContainmentLimitKm is how far from a crisis's center a pinned report may fall
// before the pin is ignored: max(radius*1.5, radius+5) km — proportional slack for
// large crises, a fixed 5 km floor of slack for small ones.
func pinContainmentLimitKm(radiusKm float64) float64 {
	return math.Max(radiusKm*1.5, radiusKm+5.0)
}

// pinOutsideContainment reports whether a resolved point is too far from a pinned
// crisis's center to plausibly belong to it (a stale pin from a previously browsed
// crisis). A crisis without a usable radius (radiusKm <= 0) never un-pins.
func pinOutsideContainment(lat, lng float64, c model.Crisis) bool {
	if c.RadiusKm <= 0 {
		return false
	}
	return haversineKm(lat, lng, c.CenterLat, c.CenterLng) > pinContainmentLimitKm(c.RadiusKm)
}

// haversineKm is the great-circle distance between two WGS84 points in km.
func haversineKm(lat1, lng1, lat2, lng2 float64) float64 {
	const earthRadiusKm = 6371.0
	toRad := func(deg float64) float64 { return deg * math.Pi / 180 }
	dLat := toRad(lat2 - lat1)
	dLng := toRad(lng2 - lng1)
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(toRad(lat1))*math.Cos(toRad(lat2))*math.Sin(dLng/2)*math.Sin(dLng/2)
	return 2 * earthRadiusKm * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
}

func validate(r model.Report) error {
	if r.ID == "" {
		return ValidationError{"id is required (client-supplied for idempotency)"}
	}
	if !contains(model.DamageValuesAll, r.Damage) {
		return ValidationError{fmt.Sprintf("damage must be one of %v", model.DamageValuesAll)}
	}
	if !contains(model.DebrisStates, r.Debris) {
		return ValidationError{fmt.Sprintf("debris must be one of %v", model.DebrisStates)}
	}
	// Infrastructure type + crisis nature are core multi-select questions the brief
	// requires users to answer to submit (challenge.md). Enforce them server-side so
	// the contract holds for ANY client, not only the Beacon app. (Photo is a core
	// indicator too, but it arrives via the two-phase upload after submit — its
	// completeness is enforced at verification by the photo-gate, not here.)
	if len(r.InfraTypes) == 0 {
		return ValidationError{"at least one infrastructure type is required"}
	}
	for _, it := range r.InfraTypes {
		if !contains(model.InfraTypesAll, it) {
			return ValidationError{fmt.Sprintf("invalid infraType %q", it)}
		}
	}
	if len(r.CrisisNature) == 0 {
		return ValidationError{"crisis nature is required"}
	}
	for _, cn := range r.CrisisNature {
		if !contains(model.CrisisNatures, cn) {
			return ValidationError{fmt.Sprintf("invalid crisisNature %q", cn)}
		}
	}
	if r.AILevel != nil && !contains(model.DamageValuesAll, *r.AILevel) {
		return ValidationError{"aiLevel must be a damage level"}
	}
	for _, c := range r.Clusters {
		if !contains(model.Clusters, c) {
			return ValidationError{fmt.Sprintf("invalid cluster %q", c)}
		}
	}
	// Location validity (C4): a resolved report MUST carry an in-range point; a
	// location-unresolved report MUST carry a non-empty landmark instead.
	if r.LocationResolved {
		if r.Lat == nil || r.Lng == nil {
			return ValidationError{"resolved report requires lat/lng"}
		}
		if *r.Lat < -90 || *r.Lat > 90 || *r.Lng < -180 || *r.Lng > 180 {
			return ValidationError{"lat/lng out of range"}
		}
	} else if r.Landmark == nil || *r.Landmark == "" {
		return ValidationError{"location-unresolved report requires a landmark"}
	}
	return nil
}

// enforceSubmitGuards applies the two DB-backed anti-abuse checks for a NEW report
// from a known submitter (Requirement #3):
//
//  1. RATE LIMIT — count the submitter's recently-created reports (by server
//     created_at) over both the burst and sustained windows. Over either cap → 429.
//     Counting committed rows in the DB makes this robust across process restarts
//     (an in-memory bucket alone would reset on every redeploy).
//  2. DEDUP — reject a near-duplicate (same submitter, within dedupRadiusMeters +
//     dedupWindowSeconds) with 409 referencing the existing report. Only a REAL
//     tapped footprint (buildingSource=="footprint" / legacy "fp-" id prefix) is
//     exempt: the per-building version chain already supersedes/merges those (see
//     Submit). Synthetic GPS-grid "b-" ids get the check like building-less pins.
func (s *ReportService) enforceSubmitGuards(ctx context.Context, submitterID string, r model.Report) error {
	now := time.Now().UTC()

	burst, err := s.reports.CountRecentBySubmitter(ctx, submitterID, now.Add(-rateLimitBurstWindow))
	if err != nil {
		return err
	}
	if burst >= rateLimitBurstMax {
		return RateLimitError{Msg: fmt.Sprintf("rate limit: at most %d reports per %s", rateLimitBurstMax, rateLimitBurstWindow)}
	}
	sustained, err := s.reports.CountRecentBySubmitter(ctx, submitterID, now.Add(-rateLimitSustainedWindow))
	if err != nil {
		return err
	}
	if sustained >= rateLimitSustainedMax {
		return RateLimitError{Msg: fmt.Sprintf("rate limit: at most %d reports per %s", rateLimitSustainedMax, rateLimitSustainedWindow)}
	}

	// Radius dedup for every resolved pin EXCEPT a real tapped footprint (footprint
	// re-reports are merged via the version chain). Skip it entirely for a
	// location-unresolved report — it has no point to ST_DWithin against.
	if !isFootprintReport(r) && r.Lat != nil && r.Lng != nil {
		existingID, found, err := s.reports.FindDuplicateBySubmitter(
			ctx, submitterID, *r.Lat, *r.Lng, r.CapturedAt,
			dedupRadiusMeters, dedupWindowSeconds, r.ID)
		if err != nil {
			return err
		}
		if found {
			return DuplicateError{
				Msg:        fmt.Sprintf("duplicate of an existing nearby report (%s) submitted moments ago", existingID),
				ExistingID: existingID,
			}
		}
	}
	return nil
}

// Submit performs an idempotent insert with server-authoritative versioning.
// Returns created=false on a replay (same id or idempotency_key already stored).
func (s *ReportService) Submit(ctx context.Context, req model.SubmitReportRequest, submitterID *string) (*model.Report, bool, error) {
	r, err := normalize(req, submitterID)
	if err != nil {
		return nil, false, err
	}
	if err := validate(r); err != nil {
		return nil, false, err
	}

	// Anti-abuse guards (rate limit + dedup) run BEFORE we do any work, but only for
	// a known submitter and only when this is NOT a legitimate idempotency replay
	// (same id already stored). A replay — e.g. an offline client re-POSTing a queued
	// report — must always succeed (it inserts nothing) and must never trip the limit
	// or the dedup check against its own earlier row.
	if submitterID != nil && *submitterID != "" {
		if existing, e := s.reports.GetByID(ctx, r.ID); e == nil && existing != nil {
			// known id → replay; skip guards and fall through to the idempotent UPSERT.
		} else if e == nil {
			if err := s.enforceSubmitGuards(ctx, *submitterID, r); err != nil {
				return nil, false, err
			}
		}
	}

	// The next two steps need a resolved point; a location-unresolved (landmark-only)
	// report has nil coords, so skip reverse-geocoding and spatial crisis assignment
	// for it (it stays admin-unset and, unless the client pinned a crisis, pending).
	if r.Lat != nil && r.Lng != nil {
		lat, lng := *r.Lat, *r.Lng
		// Reverse-geocode to the admin P-code chain (the routing/join key). Best-effort:
		// a point outside known boundaries simply leaves admin unset.
		if chain, err := s.admin.ResolveAdmin(ctx, lng, lat); err == nil && chain != nil {
			r.Admin = chain
		}

		// Geographic containment for a client-pinned crisis: when the pin's crisis has
		// a center+radius and this resolved point falls far outside it (beyond
		// max(radius*1.5, radius+5) km), the pin is almost certainly stale — e.g. the
		// app kept a previously browsed crisis selected. IGNORE the pin and let the
		// normal spatial assignment below decide; the report is never rejected for
		// this. Landmark-only (no-coords) reports never reach here and keep their pin.
		if r.CrisisID != "" && s.crises != nil {
			if c, err := s.crises.Get(ctx, r.CrisisID); err == nil && c != nil && pinOutsideContainment(lat, lng, *c) {
				r.CrisisID = ""
			}
		}

		// Server-side crisis assignment by space+time (unless the client pinned one).
		// No match => crisis_id stays empty (pending); an emergent crisis may form below.
		if r.CrisisID == "" && s.crises != nil {
			if cid, err := s.crises.AssignCrisis(ctx, lat, lng, r.CapturedAt); err == nil {
				r.CrisisID = cid
				// Ground-truth activation: feed-detected (USGS/GDACS) and emergent
				// crises are born 'proposed' — the first community report assigned to
				// one is the on-the-ground confirmation that promotes it to 'active'.
				if cid != "" {
					_, _ = s.crises.ActivateIfProposed(ctx, cid)
				}
			}
		}
	}

	// Translate the reporter's free-text description into the analysts' common language
	// (self-hosted open-source MT). Best-effort: the original is always preserved, and any
	// failure simply leaves it untranslated.
	if s.translator.Enabled() && r.Description != nil && r.Description.Original != "" {
		if tr, lang, ok := s.translator.Translate(ctx, r.Description.Original); ok {
			if r.Description.OriginalLang == "" || r.Description.OriginalLang == "auto" {
				r.Description.OriginalLang = lang
			}
			if lang != s.translator.Target() && tr != r.Description.Original {
				r.Description.Translated = tr
				target := s.translator.Target()
				r.Description.TranslatedLang = &target
			}
		}
	}

	inserted := false
	hasBuilding := r.BuildingID != nil && *r.BuildingID != ""
	txErr := store.RunInTx(ctx, s.pool, func(tx pgx.Tx) error {
		if hasBuilding {
			// A tapped footprint implies a resolved point; r.Lat/r.Lng are already
			// *float64 (nil only on an unresolved report, which never carries a building).
			// Ensure the building row exists (for the FK + version lock) WITHOUT
			// touching its cached damage — that is refreshed only on a real insert.
			if err := store.UpsertBuilding(ctx, tx, model.Building{
				ID: *r.BuildingID, CrisisID: r.CrisisID, Lat: r.Lat, Lng: r.Lng, CurrentDamage: nil,
			}); err != nil {
				return err
			}
			v, sup, err := store.NextVersionForBuilding(ctx, tx, *r.BuildingID)
			if err != nil {
				return err
			}
			r.Version, r.SupersedesReportID = v, sup
		}
		ins, err := store.UpsertReport(ctx, tx, r)
		if err != nil {
			return err
		}
		inserted = ins
		// Refresh cached damage from the newest report only when we actually
		// inserted one — a replay inserts nothing, so it must not mutate it.
		if ins && hasBuilding {
			if err := store.RefreshBuildingCurrentDamage(ctx, tx, *r.BuildingID); err != nil {
				return err
			}
		}
		return nil
	})

	if txErr != nil {
		// idempotency_key unique violation (new id, seen key) → treat as replay.
		var pgErr *pgconn.PgError
		if errors.As(txErr, &pgErr) && pgErr.Code == "23505" {
			if existing, e := s.reports.GetByIdempotencyKey(ctx, r.IdempotencyKey); e == nil && existing != nil {
				return existing, false, nil
			}
		}
		return nil, false, txErr
	}

	// Emergent crisis: a brand-new report that matched no existing crisis may have
	// just completed a spatiotemporal cluster of pending reports → propose a crisis
	// and pull them in (analyst confirms later). Best-effort; never fails the submit.
	// Requires a resolved point (an unresolved report cannot anchor a spatial cluster).
	// The crisis title/area come from the admin-boundary resolve at the centroid —
	// never from a report's free-text place.
	if inserted && r.CrisisID == "" && s.crises != nil && r.Lat != nil && r.Lng != nil {
		_, _ = s.crises.DetectEmergentCrisis(ctx, *r.Lat, *r.Lng, r.CapturedAt, s.emergentAreaName)
	}

	// Lazily ensure this point's country has admin boundaries loaded, so this report
	// (and others in the country) get an Area auto-tagged. Off the hot path with a
	// detached context — never blocks or fails the submit response. Skip for an
	// unresolved report (no point to locate a country from).
	if inserted && s.boundaries != nil && r.Lat != nil && r.Lng != nil {
		lng, lat := *r.Lng, *r.Lat
		go func() {
			bg, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			s.boundaries.EnsureForPoint(bg, lng, lat)
		}()
	}

	stored, err := s.reports.GetByID(ctx, r.ID)
	if err != nil {
		return nil, false, err
	}
	return stored, inserted, nil
}

// emergentAreaName resolves the DEEPEST available admin-area name (ADM2 > ADM1 >
// ADM0) for an emergent-crisis centroid via the admin_areas reverse-geocode.
// Returns "" when the point falls outside known boundaries — DetectEmergentCrisis
// then falls back to a coordinate-based title (never a report's free-text place).
func (s *ReportService) emergentAreaName(ctx context.Context, lat, lng float64) string {
	chain, err := s.admin.ResolveAdmin(ctx, lng, lat)
	if err != nil || chain == nil {
		return ""
	}
	switch {
	case chain.Adm2 != nil:
		return chain.Adm2.Name
	case chain.Adm1 != nil:
		return chain.Adm1.Name
	case chain.Adm0 != nil:
		return chain.Adm0.Name
	}
	return ""
}
