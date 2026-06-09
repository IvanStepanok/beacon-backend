package handler

import (
	"net/http"
)

const demoAnonymousID = "A4-92K"

func (h *Handlers) anonymousID(r *http.Request) string {
	if d := deviceID(r); d != "" {
		return d
	}
	return demoAnonymousID
}

// GET /api/v1/profile — anonymous reporter profile (get-or-create by device id).
func (h *Handlers) GetProfile(w http.ResponseWriter, r *http.Request) {
	p, err := h.d.Crises.GetOrCreateProfile(r.Context(), h.anonymousID(r))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "profile failed")
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// AwardPoints is RETIRED (anti-gaming, Requirement #3). The endpoint used to let a
// caller add arbitrary points to its own profile — a trivial gaming vector. Points
// are now SERVER-DERIVED from verified reports (see store.GetOrCreateProfile), so
// there is nothing to award. The route is kept only to return a clear, stable 410
// Gone for any stale client that still POSTs to it. It performs no work and never
// mutates a profile.
//
// POST /api/v1/profile/points — GONE.
func (h *Handlers) AwardPoints(w http.ResponseWriter, _ *http.Request) {
	writeErr(w, http.StatusGone, "gone",
		"points are earned automatically from verified reports and can no longer be awarded via this endpoint")
}
