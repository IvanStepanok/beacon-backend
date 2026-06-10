package handler

import (
	"context"
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/stepanok/beacon-server/internal/model"
	"github.com/stepanok/beacon-server/internal/service"
	"github.com/stepanok/beacon-server/internal/store"
)

const DefaultCrisisID = "crisis-antakya"

// Deps are the wired dependencies for all handlers.
type Deps struct {
	Reports   *store.Reports
	Crises    *store.Crises
	Users     *store.Users
	Settings  *store.Settings
	ReportSvc *service.ReportService
	StatsSvc  *service.StatsService
	JWTSecret string
	PhotoDir  string
}

type Handlers struct {
	d Deps
}

func New(d Deps) *Handlers { return &Handlers{d: d} }

// ── auth context (set by api middleware, read by handlers) ───────────────

type ctxKey int

const userCtxKey ctxKey = 0

func WithUser(ctx context.Context, u *model.User) context.Context {
	return context.WithValue(ctx, userCtxKey, u)
}

// UserFromContext returns the authenticated analyst, or nil for anonymous requests.
func UserFromContext(ctx context.Context) *model.User {
	u, _ := ctx.Value(userCtxKey).(*model.User)
	return u
}

// isPublicTier reports whether the request is in the LOW-TRUST visibility tier:
// anonymous/public callers AND the authenticated external_viewer role. The public
// tier sees verified reports only and gets the coarsened projection / geometry. Only
// the real analyst roles (field_validator/co_analyst/regional_analyst/crisis_admin)
// are NOT public-tier and see all statuses at full precision.
func isPublicTier(r *http.Request) bool {
	u := UserFromContext(r.Context())
	return u == nil || u.IsViewerTier()
}

// publicCoordDecimals coarsens public coordinates to ~3 decimal places (~110m,
// building-block level) so anonymous responses never expose an exact reporter
// position. Authenticated analysts keep full precision.
const publicCoordDecimals = 3

func coarsen(x float64) float64 {
	p := math.Pow(10, publicCoordDecimals)
	return math.Round(x*p) / p
}

// publicProjection returns a copy of a report safe to expose to LOW-TRUST callers
// (anonymous mobile/public AND the authenticated external_viewer role). It keeps ONLY
// the fields a public damage map needs — coarsened location, damage + damageTier,
// verification, possiblyDamaged, place, admin P-codes, version, timestamps — and the
// photoUrl ONLY when the report is verified. Everything else is cleared:
//
//   - identity / location precision: submitterId, what3words (~3m), plusCode,
//     free-text landmark, buildingId, buildingSource, infraName, gpsAccuracy — each
//     would de-coarsen the ~110m grid or de-anonymize the reporter (flat fields AND
//     the nested location);
//   - operational / PII / analyst-only fields: description (free-text + reporter
//     language, can carry PII), clusters, the raw AI level/confidence, the modular
//     blob, and the anonymization object (operational metadata, not public-facing).
//
// Authenticated analyst roles never go through this projection and keep full precision.
func publicProjection(rep model.Report) model.Report {
	// Identity + exact location. Coarsen only a RESOLVED point; a location-unresolved
	// report has nil coords and must stay lat:null/lng:null (never coarsened 0,0).
	rep.SubmitterID = nil
	if rep.Lat != nil {
		c := coarsen(*rep.Lat)
		rep.Lat = &c
	}
	if rep.Lng != nil {
		c := coarsen(*rep.Lng)
		rep.Lng = &c
	}
	rep.What3Words = nil
	rep.PlusCode = nil
	rep.Landmark = nil
	rep.BuildingID = nil
	rep.BuildingSource = nil
	rep.GPSAccuracyMeters = nil
	rep.Location.Lat = rep.Lat
	rep.Location.Lng = rep.Lng
	rep.Location.What3Words = nil
	rep.Location.PlusCode = nil
	rep.Location.Landmark = nil
	rep.Location.BuildingID = nil
	rep.Location.BuildingSource = nil
	rep.Location.GPSAccuracyMeters = nil

	// Operational / PII / analyst-only fields. infraName is reporter free-text that
	// names a specific building — combined with the coarsened point it could
	// de-coarsen the grid, so it is stripped like description/landmark.
	rep.InfraName = nil
	rep.Description = nil
	rep.Clusters = []string{}
	rep.AILevel = nil
	rep.AIConfidence = nil
	rep.Modular = nil
	rep.Anonymization = model.Anonymization{}

	// photoUrl is public only for verified reports; otherwise an unverified report's
	// image (which the photo handler also gates) must not be advertised.
	if rep.Verification != "verified" {
		rep.PhotoURL = nil
	}
	return rep
}

// publicProjectAll applies publicProjection to a slice (in place) for anonymous map sets.
func publicProjectAll(reps []model.Report) []model.Report {
	for i := range reps {
		reps[i] = publicProjection(reps[i])
	}
	return reps
}

// scopedCrisis resolves the requested crisis (explicit ?crisisId, else newest active)
// and enforces the caller's scope. ok=false means the authenticated analyst is out of
// scope (=> 403). A non-nil err means resolving the default scope failed (=> 500).
func (h *Handlers) scopedCrisis(r *http.Request) (cid string, ok bool, err error) {
	cid, err = h.crisisID(r)
	if err != nil {
		return "", false, err
	}
	if u := UserFromContext(r.Context()); u != nil && !u.ScopeAllows(cid) {
		return "", false, nil
	}
	return cid, true, nil
}

// listScope is the crisis-scope decision for the report LIST endpoint only. Unlike
// scopedCrisis (which defaults to the newest active crisis), the list shows reports
// across EVERY crisis the caller is scoped to when no ?crisisId is given:
//
//   - explicit ?crisisId=X  -> ({X}, nil, ok) when in scope, else ok=false (403).
//   - no crisisId, org-wide '*' scope -> ("", nil, ok): no crisis filter (every crisis).
//   - no crisisId, finite scope -> ("", <scope list>, ok): crisis_id = ANY(scope).
//   - no crisisId, anonymous/nil user (should not happen behind requireAnalyst, but
//     handled fail-safe) -> the single DefaultCrisisID, so a missing identity can
//     never dump the whole multi-crisis dataset.
//
// Only ListReports uses this; stats/export/area-groups/map keep scopedCrisis +
// crisisID and their newest-active-crisis default.
func (h *Handlers) listScope(r *http.Request) (crisisID string, crisisIDs []string, ok bool) {
	if v := strings.TrimSpace(r.URL.Query().Get("crisisId")); v != "" {
		if u := UserFromContext(r.Context()); u != nil && !u.ScopeAllows(v) {
			return "", nil, false
		}
		return v, nil, true
	}
	u := UserFromContext(r.Context())
	if u == nil {
		return DefaultCrisisID, nil, true // fail-safe: no identity => single default crisis
	}
	if u.ScopeAll() {
		return "", nil, true // org-wide: no crisis filter, list everything
	}
	if len(u.CrisisScope) == 0 {
		return DefaultCrisisID, nil, true // no scope grants => single default crisis
	}
	return "", append([]string{}, u.CrisisScope...), true
}

// scopeAllowsCrisis enforces the caller's crisis scope for an explicit crisis id
// (e.g. the {id} on /crises/{id}/status). Anonymous callers never reach mutators,
// so a nil user is treated as allowed (the mutator middleware already gated auth).
func scopeAllowsCrisis(r *http.Request, crisisID string) bool {
	u := UserFromContext(r.Context())
	if u == nil {
		return true
	}
	return u.ScopeAllows(crisisID)
}

// mutationScopeResult is the outcome of resolving + scope-checking a report a
// mutator is about to change.
type mutationScopeResult int

const (
	scopeOK mutationScopeResult = iota
	scopeNotFound
	scopeForbidden
	scopeInternal
)

// scopeReportMutation loads the target report's crisis_id and enforces that the
// authenticated mutator is allowed to act on that crisis. Reports with no crisis
// yet (pending, crisis_id NULL) are visible only to org-wide scope holders.
func (h *Handlers) scopeReportMutation(r *http.Request, reportID string) mutationScopeResult {
	crisisID, found, err := h.d.Reports.CrisisIDOf(r.Context(), reportID)
	if err != nil {
		return scopeInternal
	}
	if !found {
		return scopeNotFound
	}
	u := UserFromContext(r.Context())
	if u == nil {
		return scopeOK // mutator middleware already enforced auth
	}
	if crisisID == "" {
		// Pending/unassigned report: only org-wide scope may touch it.
		if u.ScopeAll() {
			return scopeOK
		}
		return scopeForbidden
	}
	if !u.ScopeAllows(crisisID) {
		return scopeForbidden
	}
	return scopeOK
}

// ── query helpers ───────────────────────────────────────────────────────

// crisisID resolves the crisis scope for a single-crisis read endpoint. An explicit
// ?crisisId wins. When omitted, it resolves the NEWEST ACTIVE crisis (the same crisis
// /crises/active returns) so omitted-scope reads (stats/map/area-groups/export) stay
// coherent with the dashboard header — instead of silently pinning to the hardcoded
// DefaultCrisisID. Falls back to DefaultCrisisID only if there is no active crisis at
// all, so a freshly-seeded / empty DB never errors out.
func (h *Handlers) crisisID(r *http.Request) (string, error) {
	if v := strings.TrimSpace(r.URL.Query().Get("crisisId")); v != "" {
		return v, nil
	}
	id, err := h.d.Crises.ActiveID(r.Context())
	if err != nil {
		return "", err
	}
	if id == "" {
		return DefaultCrisisID, nil // no active crisis: last-resort fail-safe
	}
	return id, nil
}

// qList parses repeatable or comma-separated values: ?k=a,b or ?k=a&k=b.
func qList(r *http.Request, key string) []string {
	raw := r.URL.Query()[key]
	out := []string{}
	for _, v := range raw {
		for _, p := range strings.Split(v, ",") {
			if s := strings.TrimSpace(p); s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}

func qInt(r *http.Request, key string, def int) int {
	if v := r.URL.Query().Get(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func qBool(r *http.Request, key string) bool {
	b, _ := strconv.ParseBool(r.URL.Query().Get(key))
	return b
}

// qBBox parses "minLng,minLat,maxLng,maxLat".
func qBBox(r *http.Request) (*[4]float64, error) {
	raw := strings.TrimSpace(r.URL.Query().Get("bbox"))
	if raw == "" {
		return nil, nil
	}
	parts := strings.Split(raw, ",")
	if len(parts) != 4 {
		return nil, errBadBBox
	}
	var b [4]float64
	for i, p := range parts {
		f, err := strconv.ParseFloat(strings.TrimSpace(p), 64)
		if err != nil {
			return nil, errBadBBox
		}
		b[i] = f
	}
	return &b, nil
}

func qStrPtr(r *http.Request, key string) *string {
	if v := strings.TrimSpace(r.URL.Query().Get(key)); v != "" {
		return &v
	}
	return nil
}

func deviceID(r *http.Request) string { return strings.TrimSpace(r.Header.Get("X-Device-Id")) }
