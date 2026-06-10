package handler

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/stepanok/beacon-server/internal/model"
	"github.com/stepanok/beacon-server/internal/service"
)

// GET /api/v1/form-schema?crisisId=X — PUBLIC (like /crises): the anonymous
// mobile capture flow downloads the modular form definition before submitting.
// Returns the built-in Appendix-1 sections with the crisis's stored overrides
// (required/disabled) applied; with no ?crisisId the newest active crisis's
// form is returned (the same default scope as the other public reads). An
// unknown crisis id resolves to no overrides, i.e. the plain defaults.
func (h *Handlers) GetFormSchema(w http.ResponseWriter, r *http.Request) {
	cid, err := h.crisisID(r)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "could not resolve crisis scope")
		return
	}
	ov, err := h.d.Crises.FormOverrides(r.Context(), cid)
	if err != nil {
		// FAIL OPEN: this is the public, offline-first capture path — a corrupt
		// stored form_overrides row (unmarshal/scan failure) must never block
		// reporting with a 500. Log it and serve the built-in default sections
		// (ov=nil ⇒ no overrides), the same fallback an unknown crisis id gets.
		slog.Error("form-schema: overrides load failed, serving defaults", "crisisId", cid, "err", err)
		ov = nil
	}
	writeJSON(w, http.StatusOK, model.FormSchema{Sections: service.ApplyFormOverrides(service.DefaultFormSections(), ov)})
}

// PATCH /api/v1/crises/{id}/form  body: {"required":[...],"disabled":[...]}
// Adjusts which modular sections a crisis's capture form requires or hides.
//
// Like SetCrisisStatus this is a senior, crisis-scoped decision: shaping what
// EVERY reporter in a crisis is asked is restricted to regional_analyst /
// crisis_admin via CanManageCrisis(), and the crisis must be in the caller's
// scope. Responds with the crisis's resolved schema (same shape as GET
// /form-schema) so a dashboard can re-render immediately.
func (h *Handlers) PatchCrisisForm(w http.ResponseWriter, r *http.Request) {
	if u := UserFromContext(r.Context()); u == nil || !u.CanManageCrisis() {
		writeErr(w, http.StatusForbidden, "forbidden", "only a regional analyst or crisis admin can adjust a crisis's form")
		return
	}
	var body model.FormOverrides
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_json", err.Error())
		return
	}
	// Normalize omitted lists to empty so the stored jsonb is always two arrays.
	if body.Required == nil {
		body.Required = []string{}
	}
	if body.Disabled == nil {
		body.Disabled = []string{}
	}
	if err := service.ValidateFormOverrides(body); err != nil {
		writeErr(w, http.StatusBadRequest, "validation", err.Error())
		return
	}
	crisisID := chi.URLParam(r, "id")
	// Crisis-scope the mutation: an analyst may only adjust a crisis in scope.
	if !scopeAllowsCrisis(r, crisisID) {
		writeErr(w, http.StatusForbidden, "out_of_scope", "your role cannot modify this crisis")
		return
	}
	found, err := h.d.Crises.SetFormOverrides(r.Context(), crisisID, body)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "update failed")
		return
	}
	if !found {
		writeErr(w, http.StatusNotFound, "not_found", "crisis not found")
		return
	}
	writeJSON(w, http.StatusOK, model.FormSchema{Sections: service.ApplyFormOverrides(service.DefaultFormSections(), &body)})
}
