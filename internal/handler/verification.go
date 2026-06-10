package handler

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/stepanok/beacon-server/internal/model"
	"github.com/stepanok/beacon-server/internal/store"
)

// PATCH /api/v1/reports/{id}/verification — analyst decision (auth enforced by router).
func (h *Handlers) PatchVerification(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req model.VerificationRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_json", err.Error())
		return
	}
	// `status` is the canonical body key; `verification` is the legacy alias older
	// dashboards still send (status wins when both are present).
	status := req.Status
	if status == "" {
		status = req.Verification
	}
	if !containsStr(model.Verifications, status) {
		writeErr(w, http.StatusBadRequest, "validation", "status must be pending|verified|flagged")
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
	updated, err := h.d.Reports.UpdateVerification(r.Context(), id, status, actor, req.Note, req.Force)
	if err != nil {
		// Photo gate: verifying a photo-less report needs the explicit force=true
		// override (which is recorded in the audit trail as forced).
		if errors.Is(err, store.ErrPhotoRequired) {
			writeErr(w, http.StatusConflict, "photo_required", "cannot verify a report without a photo; pass force=true to override")
			return
		}
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
