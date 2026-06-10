package handler

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/stepanok/beacon-server/internal/model"
	"github.com/stepanok/beacon-server/internal/store"
)

var errBadBBox = errors.New("bbox must be minLng,minLat,maxLng,maxLat")

// POST /api/v1/reports — idempotent submit.
func (h *Handlers) SubmitReport(w http.ResponseWriter, r *http.Request) {
	var req model.SubmitReportRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_json", err.Error())
		return
	}
	// A device identity is REQUIRED to submit: it stamps the anonymous submitter
	// (so "My Reports" works without de-anonymizing the reporter) AND it is what the
	// per-device rate-limit + dedup guards key on. Without it, a caller could post
	// unlimited near-identical reports with a NULL submitter, escaping both guards.
	d := deviceID(r)
	if d == "" {
		writeErr(w, http.StatusBadRequest, "device_id_required", "X-Device-Id header is required to submit a report")
		return
	}
	var submitterID *string
	if sid, err := h.d.Crises.ResolveSubmitterID(r.Context(), d); err == nil {
		submitterID = &sid
	}
	stored, created, err := h.d.ReportSvc.Submit(r.Context(), req, submitterID)
	if err != nil {
		mapServiceError(w, err)
		return
	}
	status := http.StatusOK // idempotent replay
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, stored)
}

// GET /api/v1/reports — list + filter + paginate.
func (h *Handlers) ListReports(w http.ResponseWriter, r *http.Request) {
	bbox, err := qBBox(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_bbox", err.Error())
		return
	}
	cid, cids, ok := h.listScope(r)
	if !ok {
		writeErr(w, http.StatusForbidden, "out_of_scope", "your role cannot access this crisis")
		return
	}
	f := store.ListFilter{
		CrisisID:     cid,
		CrisisIDs:    cids,
		Damage:       qList(r, "damage"),
		Verification: qList(r, "verification"),
		Q:            r.URL.Query().Get("q"),
		Mine:         qBool(r, "mine"),
		BuildingID:   qStrPtr(r, "buildingId"),
		Adm1Pcode:    qStrPtr(r, "adm1Pcode"),
		Adm2Pcode:    qStrPtr(r, "adm2Pcode"),
		Adm3Pcode:    qStrPtr(r, "adm3Pcode"),
		Cluster:      qStrPtr(r, "cluster"),
		BBox:         bbox,
		Limit:        qInt(r, "pageSize", 50),
		Offset:       qInt(r, "page", 0) * qInt(r, "pageSize", 50),
	}
	if f.Limit > 200 {
		f.Limit = 200
	}
	// Low-trust viewer tier (external_viewer) reaches this route via requireAnalyst, but
	// must NOT be able to bulk-pull raw reports: force verified-only here and coarsen the
	// items below (mirrors the per-report / map / tile / export lockdown).
	u := UserFromContext(r.Context())
	viewerTier := u != nil && u.IsViewerTier()
	if viewerTier {
		f.Verification = []string{"verified"}
	}
	// mine=true requires an identity; resolve X-Device-Id → submitter uuid rather
	// than silently returning everyone's reports.
	if f.Mine {
		d := deviceID(r)
		if d == "" {
			writeErr(w, http.StatusBadRequest, "validation", "mine=true requires the X-Device-Id header")
			return
		}
		sid, err := h.d.Crises.ResolveSubmitterID(r.Context(), d)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "internal", "identity resolve failed")
			return
		}
		f.SubmitterID = &sid
	}
	if cur := r.URL.Query().Get("cursor"); cur != "" {
		c, err := store.DecodeCursor(cur)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "bad_cursor", "invalid cursor")
			return
		}
		f.Cursor = c
		f.Offset = 0
	}

	items, total, grand, next, err := h.d.Reports.List(r.Context(), f)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "list failed")
		return
	}
	if viewerTier {
		items = publicProjectAll(items)
	}
	writeJSON(w, http.StatusOK, model.ListResponse{Items: items, Total: total, GrandTotal: grand, NextCursor: next})
}

// GET /api/v1/reports/{id}
func (h *Handlers) GetReport(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	rep, err := h.d.Reports.GetByID(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "lookup failed")
		return
	}
	if rep == nil {
		writeErr(w, http.StatusNotFound, "not_found", "report not found")
		return
	}

	u := UserFromContext(r.Context())

	// Anonymous/public callers may only see VERIFIED reports, and only the locked-down
	// projection (no submitterId, coarsened coords, no operational/PII fields). An
	// unverified report 404s for them so its existence/position is never leaked.
	if u == nil {
		if rep.Verification != "verified" {
			writeErr(w, http.StatusNotFound, "not_found", "report not found")
			return
		}
		pub := publicProjection(*rep)
		writeJSON(w, http.StatusOK, &pub)
		return
	}

	// Authenticated: enforce CRISIS SCOPE — a caller without scope for the report's
	// crisis gets a 404 (so out-of-scope reports' existence/position is not leaked),
	// mirroring scopeReportMutation. A pending report (crisis_id empty) is visible
	// only to org-wide scope holders.
	if rep.CrisisID == "" {
		if !u.ScopeAll() {
			writeErr(w, http.StatusNotFound, "not_found", "report not found")
			return
		}
	} else if !u.ScopeAllows(rep.CrisisID) {
		writeErr(w, http.StatusNotFound, "not_found", "report not found")
		return
	}

	// Role-aware projection: a low-trust viewer (external_viewer) gets the SAME
	// coarsened public projection even when authenticated and in scope, and may only
	// see verified reports. Only the real analyst roles get full precision + all
	// statuses (raw coords, submitterId, operational fields).
	if u.IsViewerTier() {
		if rep.Verification != "verified" {
			writeErr(w, http.StatusNotFound, "not_found", "report not found")
			return
		}
		pub := publicProjection(*rep)
		writeJSON(w, http.StatusOK, &pub)
		return
	}

	writeJSON(w, http.StatusOK, rep)
}

// GET /api/v1/reports/latest-per-building?crisisId=&bbox=
// Location-first: crisisId is OPTIONAL. With a bbox the map pins are scoped to the
// viewport (no forced default crisis) — a user anywhere sees only what's near them.
// At least one of crisisId or bbox is required (never an unbounded scan).
func (h *Handlers) LatestPerBuilding(w http.ResponseWriter, r *http.Request) {
	bbox, err := qBBox(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_bbox", err.Error())
		return
	}
	crisisID := r.URL.Query().Get("crisisId")
	if crisisID == "" && bbox == nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "crisisId or bbox required")
		return
	}
	// Public tier (anonymous OR external_viewer): verified reports only + coarsened
	// public projection. Real analyst roles see all statuses at full precision
	// (still crisis/bbox-scoped).
	pub := isPublicTier(r)
	items, err := h.d.Reports.LatestPerBuilding(r.Context(), crisisID, bbox, pub)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	if pub {
		// Strip submitterId/PII + coarsen coords for the public map pins.
		items = publicProjectAll(items)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// GET /api/v1/tiles/reports/{z}/{x}/{y}.mvt?crisisId=
// Scalable Mapbox Vector Tile source: clustered counts at low zoom, latest-per-
// building points at high zoom. Public callers get verified reports only.
func (h *Handlers) ReportTile(w http.ResponseWriter, r *http.Request) {
	z, e1 := strconv.Atoi(chi.URLParam(r, "z"))
	x, e2 := strconv.Atoi(chi.URLParam(r, "x"))
	y, e3 := strconv.Atoi(strings.TrimSuffix(chi.URLParam(r, "y"), ".mvt"))
	if e1 != nil || e2 != nil || e3 != nil || z < 0 || z > 24 {
		writeErr(w, http.StatusBadRequest, "bad_tile", "invalid z/x/y")
		return
	}
	// Public tier (anonymous OR the low-trust external_viewer) gets verified reports
	// only AND coarsened point geometry at high zoom; only the real analyst roles see
	// all statuses at exact precision.
	mvt, err := h.d.Reports.MapTileMVT(r.Context(), z, x, y, r.URL.Query().Get("crisisId"), isPublicTier(r))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "tile render failed")
		return
	}
	w.Header().Set("Content-Type", "application/vnd.mapbox-vector-tile")
	w.Header().Set("Cache-Control", "public, max-age=30")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(mvt)
}

// GET /api/v1/reports/area-groups
// Public aggregate. For the public tier (anonymous OR external_viewer) the counts
// cover VERIFIED reports only — mirroring /map/features — so the public page never
// shows area totals that disagree with (or leak beyond) the verified-only public
// map. Real analyst roles keep the full all-statuses counts.
func (h *Handlers) AreaGroups(w http.ResponseWriter, r *http.Request) {
	cid, err := h.crisisID(r)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "could not resolve crisis scope")
		return
	}
	groups, err := h.d.Reports.AreaGroups(r.Context(), cid, isPublicTier(r))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": groups})
}

// GET /api/v1/buildings/{buildingId}/timeline
// Public endpoint. For the public tier (anonymous OR external_viewer) it is
// restricted to VERIFIED entries only, and each entry's free-text note + reporter
// identity are stripped (the public projection policy: no notes/PII, no reporter id).
// Real analyst roles see the full chain (all statuses, notes + reporter).
func (h *Handlers) BuildingTimeline(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "buildingId")
	pub := isPublicTier(r)
	tl, err := h.d.Reports.BuildingTimeline(r.Context(), id, pub)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	if tl == nil {
		writeErr(w, http.StatusNotFound, "not_found", "no history for building")
		return
	}
	if pub {
		// Strip operational/PII: drop the free-text note and reporter identity so the
		// public chain shows only the damage progression + timestamps.
		for i := range tl.Versions {
			tl.Versions[i].Note = ""
			tl.Versions[i].By = "community"
		}
	}
	writeJSON(w, http.StatusOK, tl)
}
