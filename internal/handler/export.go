package handler

import (
	"net/http"

	"github.com/stepanok/beacon-server/internal/model"
	"github.com/stepanok/beacon-server/internal/service"
	"github.com/stepanok/beacon-server/internal/store"
)

// GET /api/v1/reports/export?format=geojson|csv|gpkg|kml|shapefile
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

	// Stream every matching row from a DB cursor straight to the response: at
	// crisis scale (100k–500k) the prior materialize-then-encode path peaked at
	// multi-GB RSS and would OOM the host. RAM now stays at one row (text formats)
	// or a temp file on disk (GPKG/Shapefile binary containers). NOTE: headers are
	// committed before the first row, so a mid-stream query error can no longer be
	// turned into a clean HTTP error code — it truncates the download instead (the
	// query itself is a single SELECT, so this is a remote edge case).
	src := func(yield func(*model.Report) error) error {
		return h.d.Reports.ExportEach(r.Context(), f, yield)
	}

	switch format {
	case "geojson":
		w.Header().Set("Content-Type", "application/geo+json")
		w.Header().Set("Content-Disposition", `attachment; filename="beacon-reports.geojson"`)
		w.WriteHeader(http.StatusOK)
		_ = service.StreamGeoJSON(w, src)
	case "csv":
		keys, err := h.d.Reports.ModularKeysRaw(r.Context(), f)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "internal", "export query failed")
			return
		}
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="beacon-reports.csv"`)
		w.WriteHeader(http.StatusOK)
		_ = service.StreamCSV(w, src, service.CSVExtraColumns(keys))
	case "gpkg":
		keys, err := h.d.Reports.ModularKeysRaw(r.Context(), f)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "internal", "export query failed")
			return
		}
		// GPKG builds to a temp file first, so a build error surfaces before any
		// body bytes are written (the status is still settable until io.Copy starts).
		w.Header().Set("Content-Type", "application/geopackage+sqlite3")
		w.Header().Set("Content-Disposition", `attachment; filename="beacon-reports.gpkg"`)
		if err := service.StreamGPKG(w, src, service.GPKGExtraColumns(keys)); err != nil {
			// Best-effort: if nothing was written yet the status is still settable.
			writeErr(w, http.StatusInternalServerError, "internal", "gpkg build failed")
			return
		}
	case "kml":
		w.Header().Set("Content-Type", "application/vnd.google-earth.kml+xml")
		w.Header().Set("Content-Disposition", `attachment; filename="beacon-reports.kml"`)
		w.WriteHeader(http.StatusOK)
		_ = service.StreamKML(w, src)
	case "shapefile", "shp":
		w.Header().Set("Content-Type", "application/zip")
		w.Header().Set("Content-Disposition", `attachment; filename="beacon-reports.shp.zip"`)
		if err := service.StreamShapefile(w, src); err != nil {
			writeErr(w, http.StatusInternalServerError, "internal", "shapefile build failed")
			return
		}
	default:
		writeErr(w, http.StatusBadRequest, "bad_format", "format must be geojson|csv|gpkg|kml|shapefile")
	}
}
