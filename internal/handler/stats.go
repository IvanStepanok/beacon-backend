package handler

import (
	"net/http"

	"github.com/stepanok/beacon-server/internal/model"
)

// GET /api/v1/stats/overview — all aggregates computed in SQL.
func (h *Handlers) StatsOverview(w http.ResponseWriter, r *http.Request) {
	cid, ok, err := h.scopedCrisis(r)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "could not resolve crisis scope")
		return
	}
	if !ok {
		writeErr(w, http.StatusForbidden, "out_of_scope", "your role cannot access this crisis")
		return
	}
	overview, err := h.d.StatsSvc.Overview(r.Context(), cid)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "stats failed")
		return
	}
	// Aggregate counts are non-sensitive, but the embedded Recent[] is a list of full
	// reports — coarsen it (verified-only) for the low-trust viewer tier so it can't be
	// used to read raw coords/PII, consistent with the rest of the viewer-tier lockdown.
	if u := UserFromContext(r.Context()); u != nil && u.IsViewerTier() {
		projected := make([]model.Report, 0, len(overview.Recent))
		for _, rep := range overview.Recent {
			if rep.Verification == "verified" {
				projected = append(projected, publicProjection(rep))
			}
		}
		overview.Recent = projected
	}
	writeJSON(w, http.StatusOK, overview)
}
