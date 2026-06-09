package handler

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/stepanok/beacon-server/internal/model"
)

// PATCH /api/v1/reports/{id}/verification — analyst decision (auth enforced by router).
func (h *Handlers) PatchVerification(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req model.VerificationRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_json", err.Error())
		return
	}
	if !containsStr(model.Verifications, req.Verification) {
		writeErr(w, http.StatusBadRequest, "validation", "verification must be pending|verified|flagged")
		return
	}
	// Crisis-scope the mutation: load the report, resolve its crisis, enforce scope.
	switch h.scopeReportMutation(r, id) {
	case scopeNotFound:
		writeErr(w, http.StatusNotFound, "not_found", "report not found")
		return
	case scopeForbidden:
		writeErr(w, http.StatusForbidden, "out_of_scope", "your role cannot modify this crisis")
		return
	case scopeInternal:
		writeErr(w, http.StatusInternalServerError, "internal", "scope check failed")
		return
	}
	actor := "analyst"
	if u := UserFromContext(r.Context()); u != nil {
		actor = u.Email
	}
	updated, err := h.d.Reports.UpdateVerification(r.Context(), id, req.Verification, actor)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "update failed")
		return
	}
	if updated == nil {
		writeErr(w, http.StatusNotFound, "not_found", "report not found")
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func containsStr(set []string, v string) bool {
	for _, s := range set {
		if s == v {
			return true
		}
	}
	return false
}
