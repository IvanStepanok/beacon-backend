// Package boundary fills admin_areas with administrative boundaries so every report
// gets an Area (admin region) reverse-geocoded — globally, automatically.
//
// Strategy (matching the tight prod box):
//   - A global ADM0 baseline (embedded Natural Earth, public domain) is loaded once
//     at startup. It gives every point a COUNTRY immediately and is the point→ISO3
//     resolver for the lazy step.
//   - ADM1 (regions/oblasts/states) are fetched LAZILY per country from geoBoundaries
//     (gbOpen) the first time a report lands in that country — then existing reports
//     there are re-geocoded. Each country is fetched at most once.
//
// geoBoundaries gives admin NAMES + a stable shapeID (stored as the pcode), not
// official OCHA P-codes — that authoritative layer (HDX COD-AB, source='cod') can be
// layered on later; ResolveAdmin already ranks 'cod' above 'geoboundaries'.
package boundary

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stepanok/beacon-server/internal/store"
)

//go:embed data/countries.geojson
var countriesFS embed.FS

const (
	sourceBaseline = "naturalearth"
	sourceGB       = "geoboundaries"
	gbAPIBase      = "https://www.geoboundaries.org/api/current/gbOpen"
	userAgent      = "BeaconBackend/1.0 (+https://beacon-api.stepanok.com)"
)

// Loader populates admin_areas. Safe for concurrent use; each country is fetched at
// most once (in-flight guard + DB gate).
type Loader struct {
	pool   *pgxpool.Pool
	admin  *store.Admin
	client *http.Client
	log    *slog.Logger

	mu       sync.Mutex
	inflight map[string]bool
}

func New(pool *pgxpool.Pool, admin *store.Admin, log *slog.Logger) *Loader {
	return &Loader{
		pool:     pool,
		admin:    admin,
		client:   &http.Client{Timeout: 90 * time.Second},
		log:      log,
		inflight: map[string]bool{},
	}
}

// ── minimal GeoJSON / geoBoundaries shapes ─────────────────────────────

type featureColl struct {
	Features []geoFeature `json:"features"`
}
type geoFeature struct {
	Properties map[string]any  `json:"properties"`
	Geometry   json.RawMessage `json:"geometry"`
}

// gbMeta is the geoBoundaries metadata response (numeric fields are JSON strings —
// keep them as strings). Prefer the simplified geometry to spare the small server.
type gbMeta struct {
	GjDownloadURL             string `json:"gjDownloadURL"`
	SimplifiedGeometryGeoJSON string `json:"simplifiedGeometryGeoJSON"`
	BoundaryYearRepresented   string `json:"boundaryYearRepresented"`
	LicenseDetail             string `json:"licenseDetail"`
}

func (m gbMeta) downloadURL() string {
	if u := strings.TrimSpace(m.SimplifiedGeometryGeoJSON); u != "" {
		return u
	}
	return strings.TrimSpace(m.GjDownloadURL)
}
func (m gbMeta) version() string {
	if m.BoundaryYearRepresented != "" {
		return "gb:" + m.BoundaryYearRepresented
	}
	return "gb"
}

// ── public API ─────────────────────────────────────────────────────────

// LoadCountriesBaseline loads the embedded Natural Earth ADM0 set as level-0 rows
// (pcode = ISO3). Idempotent — a no-op once present.
func (l *Loader) LoadCountriesBaseline(ctx context.Context) error {
	var n int
	if err := l.pool.QueryRow(ctx, "SELECT count(*) FROM admin_areas WHERE source=$1", sourceBaseline).Scan(&n); err == nil && n > 0 {
		return nil
	}
	raw, err := countriesFS.ReadFile("data/countries.geojson")
	if err != nil {
		return fmt.Errorf("read embedded countries: %w", err)
	}
	var coll featureColl
	if err := json.Unmarshal(raw, &coll); err != nil {
		return fmt.Errorf("parse embedded countries: %w", err)
	}
	loaded := 0
	for _, f := range coll.Features {
		a3, _ := f.Properties["a3"].(string)
		name, _ := f.Properties["name"].(string)
		if a3 == "" || name == "" || len(f.Geometry) == 0 {
			continue
		}
		if err := store.UpsertAdminAreaGeoJSON(ctx, l.pool, a3, 0, name, nil, a3, sourceBaseline, "ne_110m", f.Geometry); err != nil {
			l.log.Warn("baseline country load failed", "iso3", a3, "err", err)
			continue
		}
		loaded++
	}
	l.log.Info("admin ADM0 baseline loaded", "countries", loaded)
	return nil
}

// EnsureForPoint resolves a point's country and, if that country has no ADM1 regions
// loaded yet, fetches them (then re-geocodes). Best-effort; safe to call in a
// goroutine on the submit path.
func (l *Loader) EnsureForPoint(ctx context.Context, lng, lat float64) {
	chain, err := l.admin.ResolveAdmin(ctx, lng, lat)
	if err != nil || chain == nil || chain.Adm0 == nil {
		return
	}
	// NB: no "already has a region" early-return — a report may have a geoBoundaries/seed region
	// but still need the official COD-AB upgrade. EnsureCountry is a cheap no-op once COD is loaded.
	if err := l.EnsureCountry(ctx, chain.Adm0.Pcode); err != nil {
		l.log.Warn("ensure country (point) failed", "iso3", chain.Adm0.Pcode, "err", err)
	}
}

// SweepExisting (startup) ensures ADM1 coverage for every country that already has
// reports lacking a region — so historical reports get an Area too.
func (l *Loader) SweepExisting(ctx context.Context) {
	pts, err := l.admin.ReportsMissingRegion(ctx)
	if err != nil {
		l.log.Warn("boundary sweep: list failed", "err", err)
		return
	}
	seen := map[string]bool{}
	for _, p := range pts {
		chain, err := l.admin.ResolveAdmin(ctx, p.Lng, p.Lat)
		if err != nil || chain == nil || chain.Adm0 == nil {
			continue
		}
		iso3 := chain.Adm0.Pcode
		if seen[iso3] {
			continue
		}
		seen[iso3] = true
		if err := l.EnsureCountry(ctx, iso3); err != nil {
			l.log.Warn("boundary sweep: ensure country failed", "iso3", iso3, "err", err)
		}
	}
}

// EnsureCODCoverage (startup) ensures every country that has reports gets its official COD-AB
// P-codes loaded (skipping countries already covered) — so existing reports tagged earlier via
// geoBoundaries/seed are upgraded to authoritative, join-ready P-codes + ADM2. EnsureCountry tries
// COD first and falls back to geoBoundaries, so non-COD countries are still covered.
func (l *Loader) EnsureCODCoverage(ctx context.Context) {
	countries, err := l.admin.ReportedCountries(ctx)
	if err != nil {
		l.log.Warn("COD coverage: list reported countries failed", "err", err)
		return
	}
	for _, iso3 := range countries {
		if n, err := l.admin.AreaCountByISO3(ctx, iso3, sourceCOD); err == nil && n > 0 {
			continue // already has its COD layer
		}
		if err := l.EnsureCountry(ctx, iso3); err != nil {
			l.log.Warn("COD coverage: ensure country failed", "iso3", iso3, "err", err)
		}
	}
}

// EnsureCountry fetches geoBoundaries ADM1 for a country (once) and re-geocodes
// reports that can now be tagged with a region.
func (l *Loader) EnsureCountry(ctx context.Context, iso3 string) error {
	iso3 = strings.ToUpper(strings.TrimSpace(iso3))
	if len(iso3) != 3 {
		return nil
	}
	l.mu.Lock()
	if l.inflight[iso3] {
		l.mu.Unlock()
		return nil
	}
	l.inflight[iso3] = true
	l.mu.Unlock()
	defer func() {
		l.mu.Lock()
		delete(l.inflight, iso3)
		l.mu.Unlock()
	}()

	// Prefer official OCHA COD-AB P-codes (ADM1 + ADM2); ResolveAdmin ranks 'cod' highest, so a
	// covered country's reports get authoritative, join-ready P-codes. Only fall back to
	// geoBoundaries (ADM1 names + shapeIDs) when the country isn't published as a COD.
	if n, err := l.ensureCOD(ctx, iso3); err != nil {
		l.log.Warn("COD-AB load failed; falling back to geoBoundaries", "iso3", iso3, "err", err)
	} else if n > 0 {
		l.regeocodeCountry(ctx, iso3) // freshly loaded → upgrade ALL the country's reports to official P-codes + ADM2
		return nil
	}
	// COD already present (loaded earlier) → done; do NOT also fetch geoBoundaries.
	if c, err := l.admin.AreaCountByISO3(ctx, iso3, sourceCOD); err == nil && c > 0 {
		return nil
	}

	if n, err := l.admin.AreaCountByISO3(ctx, iso3, sourceGB); err == nil && n > 0 {
		return nil // already loaded
	}

	meta, err := l.fetchMeta(ctx, iso3, "ADM1")
	if err != nil {
		return err
	}
	url := meta.downloadURL()
	if url == "" {
		return fmt.Errorf("geoBoundaries %s ADM1: no download url", iso3)
	}
	coll, err := l.fetchFeatureColl(ctx, url)
	if err != nil {
		return err
	}

	parent := iso3 // the baseline ADM0 row's pcode
	ver := meta.version()
	loaded := 0
	for _, f := range coll.Features {
		shapeID, _ := f.Properties["shapeID"].(string)
		name, _ := f.Properties["shapeName"].(string)
		if shapeID == "" || len(f.Geometry) == 0 {
			continue
		}
		if strings.TrimSpace(name) == "" {
			name = iso3 + " region"
		}
		pcode := "GB:" + shapeID
		if err := store.UpsertAdminAreaGeoJSON(ctx, l.pool, pcode, 1, name, &parent, iso3, sourceGB, ver, f.Geometry); err != nil {
			l.log.Warn("geoBoundaries ADM1 row failed", "iso3", iso3, "shapeID", shapeID, "err", err)
			continue
		}
		loaded++
	}
	l.log.Info("geoBoundaries ADM1 loaded", "iso3", iso3, "regions", loaded, "version", ver, "license", meta.LicenseDetail)
	if loaded > 0 {
		l.regeocodeMissing(ctx)
	}
	return nil
}

// ── internals ──────────────────────────────────────────────────────────

// regeocodeCountry re-resolves EVERY report in a country and re-stamps its admin chain — used
// after a COD-AB load so reports already tagged via geoBoundaries/seed upgrade to the official
// P-codes (and gain ADM2). Unlike regeocodeMissing, it updates reports that already have a region.
func (l *Loader) regeocodeCountry(ctx context.Context, iso3 string) {
	pts, err := l.admin.ReportsInCountry(ctx, iso3)
	if err != nil {
		l.log.Warn("COD re-geocode: list failed", "iso3", iso3, "err", err)
		return
	}
	fixed := 0
	for _, p := range pts {
		chain, err := l.admin.ResolveAdmin(ctx, p.Lng, p.Lat)
		if err != nil || chain == nil {
			continue
		}
		if err := l.admin.UpdateReportAdmin(ctx, p.ID, chain); err != nil {
			l.log.Warn("COD re-geocode: update failed", "report", p.ID, "err", err)
			continue
		}
		fixed++
	}
	if fixed > 0 {
		l.log.Info("re-geocoded reports to COD-AB P-codes", "iso3", iso3, "count", fixed)
	}
}

func (l *Loader) regeocodeMissing(ctx context.Context) {
	pts, err := l.admin.ReportsMissingRegion(ctx)
	if err != nil {
		l.log.Warn("re-geocode: list failed", "err", err)
		return
	}
	fixed := 0
	for _, p := range pts {
		chain, err := l.admin.ResolveAdmin(ctx, p.Lng, p.Lat)
		if err != nil || chain == nil || chain.Adm1 == nil {
			continue // still no region — leave as-is
		}
		if err := l.admin.UpdateReportAdmin(ctx, p.ID, chain); err != nil {
			l.log.Warn("re-geocode: update failed", "report", p.ID, "err", err)
			continue
		}
		fixed++
	}
	if fixed > 0 {
		l.log.Info("re-geocoded reports with a newly-loaded region", "count", fixed)
	}
}

func (l *Loader) fetchMeta(ctx context.Context, iso3, level string) (gbMeta, error) {
	var m gbMeta
	url := fmt.Sprintf("%s/%s/%s/", gbAPIBase, iso3, level)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return m, err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := l.client.Do(req)
	if err != nil {
		return m, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return m, fmt.Errorf("geoBoundaries meta %s/%s: HTTP %d", iso3, level, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return m, err
	}
	body = bytes.TrimSpace(body)
	// The API returns an object for a fully-specified path, but tolerate an array.
	if len(body) > 0 && body[0] == '[' {
		var arr []gbMeta
		if err := json.Unmarshal(body, &arr); err != nil {
			return m, err
		}
		if len(arr) == 0 {
			return m, fmt.Errorf("geoBoundaries meta %s/%s: empty", iso3, level)
		}
		return arr[0], nil
	}
	if err := json.Unmarshal(body, &m); err != nil {
		return m, err
	}
	return m, nil
}

func (l *Loader) fetchFeatureColl(ctx context.Context, url string) (*featureColl, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := l.client.Do(req) // default client follows the github→media LFS redirect
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("geoBoundaries geojson: HTTP %d", resp.StatusCode)
	}
	var coll featureColl
	if err := json.NewDecoder(resp.Body).Decode(&coll); err != nil {
		return nil, fmt.Errorf("decode geojson: %w", err)
	}
	return &coll, nil
}
