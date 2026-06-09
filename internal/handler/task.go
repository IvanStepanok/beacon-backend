package handler

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/stepanok/beacon-server/internal/model"
)

// PATCH /api/v1/reports/{id}/task — analyst dispatch: advance status, assign,
// set severity, route to clusters, or close with a disposition (auth by router).
func (h *Handlers) PatchTask(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req model.TaskRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_json", err.Error())
		return
	}
	if req.TaskStatus != nil && !containsStr(model.TaskStatuses, *req.TaskStatus) {
		writeErr(w, http.StatusBadRequest, "validation", "invalid taskStatus")
		return
	}
	if req.Severity != nil && !containsStr(model.Severities, *req.Severity) {
		writeErr(w, http.StatusBadRequest, "validation", "invalid severity")
		return
	}
	if req.Disposition != nil && !containsStr(model.Dispositions, *req.Disposition) {
		writeErr(w, http.StatusBadRequest, "validation", "invalid disposition")
		return
	}
	if req.Clusters != nil {
		for _, c := range *req.Clusters {
			if !containsStr(model.Clusters, c) {
				writeErr(w, http.StatusBadRequest, "validation", "invalid cluster: "+c)
				return
			}
		}
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
	updated, err := h.d.Reports.UpdateTask(r.Context(), id, req, actor)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "task update failed")
		return
	}
	if updated == nil {
		writeErr(w, http.StatusNotFound, "not_found", "report not found")
		return
	}
	writeJSON(w, http.StatusOK, updated)
}
