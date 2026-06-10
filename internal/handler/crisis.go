package handler

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
)

// GET /api/v1/crises?status=active,proposed
func (h *Handlers) ListCrises(w http.ResponseWriter, r *http.Request) {
	var statuses []string
	if s := strings.TrimSpace(r.URL.Query().Get("status")); s != "" {
		for _, v := range strings.Split(s, ",") {
			if v = strings.TrimSpace(v); v != "" {
				statuses = append(statuses, v)
			}
		}
	}
	items, err := h.d.Crises.List(r.Context(), statuses)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// GET /api/v1/crises/near?lat=&lng=&radiusKm=
// Location-first launch: which active/proposed crises are near the user?
func (h *Handlers) NearbyCrises(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	lat, err1 := strconv.ParseFloat(q.Get("lat"), 64)
	lng, err2 := strconv.ParseFloat(q.Get("lng"), 64)
	if err1 != nil || err2 != nil || lat < -90 || lat > 90 || lng < -180 || lng > 180 {
		writeErr(w, http.StatusBadRequest, "bad_request", "valid lat & lng required")
		return
	}
	radiusKm := 50.0
	if v, err := strconv.ParseFloat(q.Get("radiusKm"), 64); err == nil && v > 0 {
		radiusKm = v
	}
	items, err := h.d.Crises.Near(r.Context(), lat, lng, radiusKm)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "count": len(items)})
}

// PATCH /api/v1/crises/{id}/status  body: {"status":"active"|"dismissed"|"closed"}
// Analyst confirms (active) or rejects (dismissed) an emergent proposal.
//
// Flipping a crisis's LIFECYCLE (confirm/dismiss/close/reopen) is a senior decision:
// it is restricted to regional_analyst / crisis_admin via CanManageCrisis(). A
// field_validator or co_analyst keeps verify/task within scope but may NOT change a
// crisis's status; an external_viewer cannot reach this mutator at all.
func (h *Handlers) SetCrisisStatus(w http.ResponseWriter, r *http.Request) {
	if u := UserFromContext(r.Context()); u == nil || !u.CanManageCrisis() {
		writeErr(w, http.StatusForbidden, "forbidden", "only a regional analyst or crisis admin can change a crisis's status")
		return
	}
	var body struct {
		Status string `json:"status"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "invalid body")
		return
	}
	switch body.Status {
	case "active", "dismissed", "closed", "proposed":
	default:
		writeErr(w, http.StatusBadRequest, "bad_request", "status must be active|dismissed|closed|proposed")
		return
	}
	crisisID := chi.URLParam(r, "id")
	// Crisis-scope the mutation: an analyst may only flip the status of a crisis in scope.
	if !scopeAllowsCrisis(r, crisisID) {
		writeErr(w, http.StatusForbidden, "out_of_scope", "your role cannot modify this crisis")
		return
	}
	c, err := h.d.Crises.SetCrisisStatus(r.Context(), crisisID, body.Status)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "update failed")
		return
	}
	if c == nil {
		writeErr(w, http.StatusNotFound, "not_found", "crisis not found")
		return
	}
	writeJSON(w, http.StatusOK, c)
}

// GET /api/v1/crises/active
func (h *Handlers) ActiveCrisis(w http.ResponseWriter, r *http.Request) {
	c, err := h.d.Crises.Active(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	if c == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

// GET /api/v1/crises/{id}
func (h *Handlers) GetCrisis(w http.ResponseWriter, r *http.Request) {
	c, err := h.d.Crises.Get(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	if c == nil {
		writeErr(w, http.StatusNotFound, "not_found", "crisis not found")
		return
	}
	writeJSON(w, http.StatusOK, c)
}

type mapGeometry struct {
	Type        string     `json:"type"`
	Coordinates [2]float64 `json:"coordinates"`
}
type mapFeature struct {
	Type       string         `json:"type"`
	Geometry   mapGeometry    `json:"geometry"`
	Properties map[string]any `json:"properties"`
}
type mapFC struct {
	Type     string       `json:"type"`
	Features []mapFeature `json:"features"`
}

// GET /api/v1/map/features — latest-per-building points as GeoJSON for the map.
func (h *Handlers) MapFeatures(w http.ResponseWriter, r *http.Request) {
	bbox, err := qBBox(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_bbox", err.Error())
		return
	}
	// Public tier (anonymous OR external_viewer): verified reports only, coarsened
	// coordinates, no submitter identity (props already omit it). Real analyst roles
	// see all statuses at full precision (still crisis-scoped).
	pub := isPublicTier(r)
	cid, err := h.crisisID(r)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "could not resolve crisis scope")
		return
	}
	reports, err := h.d.Reports.LatestPerBuilding(r.Context(), cid, bbox, pub)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	if pub {
		reports = publicProjectAll(reports)
	}
	fc := mapFC{Type: "FeatureCollection", Features: make([]mapFeature, 0, len(reports))}
	for _, rep := range reports {
		// Skip location-unresolved (landmark-only) reports: they have no point to
		// place on the map. They remain visible in the report list / export instead.
		if rep.Lat == nil || rep.Lng == nil {
			continue
		}
		props := map[string]any{
			"id":           rep.ID,
			"damage":       rep.Damage,
			"verification": rep.Verification,
		}
		if rep.BuildingID != nil {
			props["buildingId"] = *rep.BuildingID
		}
		fc.Features = append(fc.Features, mapFeature{
			Type:       "Feature",
			Geometry:   mapGeometry{Type: "Point", Coordinates: [2]float64{*rep.Lng, *rep.Lat}},
			Properties: props,
		})
	}
	w.Header().Set("Content-Type", "application/geo+json")
	writeJSON(w, http.StatusOK, fc)
}
