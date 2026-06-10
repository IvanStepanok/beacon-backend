package handler

import (
	"net/http"

	"github.com/stepanok/beacon-server/internal/service"
	"github.com/stepanok/beacon-server/internal/store"
)

// GET /api/v1/reports/export?format=geojson|csv|gpkg|kml
//
// Bulk export carries exact coordinates + the full per-report row (incl. building
// ids and operational columns), so it is restricted to the REAL analyst roles
// (field_validator / co_analyst / regional_analyst / crisis_admin). The low-trust
// external_viewer is DENIED (403): a viewer can browse the coarsened public map but
// cannot pull the raw, de-anonymizable dataset. (Decision: deny rather than emit a
// separate coarsened CSV — a viewer's read need is already met by the public map
// endpoints, which serve the coarsened/verified-only projection.)
func (h *Handlers) ExportReports(w http.ResponseWriter, r *http.Request) {
	if u := UserFromContext(r.Context()); u != nil && u.IsViewerTier() {
		writeErr(w, http.StatusForbidden, "forbidden", "your role cannot export raw report data")
		return
	}
	format := r.URL.Query().Get("format")
	if format == "" {
		format = "geojson"
	}
	bbox, err := qBBox(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_bbox", err.Error())
		return
	}
	cid, ok, err := h.scopedCrisis(r)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "could not resolve crisis scope")
		return
	}
	if !ok {
		writeErr(w, http.StatusForbidden, "out_of_scope", "your role cannot access this crisis")
		return
	}
	f := store.ListFilter{
		CrisisID:     cid,
		Damage:       qList(r, "damage"),
		Verification: qList(r, "verification"),
		Q:            r.URL.Query().Get("q"),
		Adm2Pcode:    qStrPtr(r, "adm2Pcode"),
		Adm3Pcode:    qStrPtr(r, "adm3Pcode"),
		BBox:         bbox,
	}

	reports, err := h.d.Reports.ExportRows(r.Context(), f)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "export query failed")
		return
	}

	switch format {
	case "geojson":
		body, err := service.ToGeoJSON(reports)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "internal", "encode failed")
			return
		}
		w.Header().Set("Content-Type", "application/geo+json")
		w.Header().Set("Content-Disposition", `attachment; filename="beacon-reports.geojson"`)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	case "csv":
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="beacon-reports.csv"`)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(service.ToCSV(reports))
	case "gpkg":
		body, err := service.ToGPKG(reports)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "internal", "gpkg build failed")
			return
		}
		w.Header().Set("Content-Type", "application/geopackage+sqlite3")
		w.Header().Set("Content-Disposition", `attachment; filename="beacon-reports.gpkg"`)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	case "kml":
		w.Header().Set("Content-Type", "application/vnd.google-earth.kml+xml")
		w.Header().Set("Content-Disposition", `attachment; filename="beacon-reports.kml"`)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(service.ToKML(reports))
	default:
		writeErr(w, http.StatusBadRequest, "bad_format", "format must be geojson|csv|gpkg|kml")
	}
}
