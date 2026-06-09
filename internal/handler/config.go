package handler

import "net/http"

const damageScaleKey = "damage_scale"

// GET /api/v1/config — public client config. damageScale drives the capture UI:
//   "tier3"  → reporters pick the required 3 tiers (minimal/partial/complete)
//   "ems98"  → reporters pick the 5-level EMS-98 grade (none…destroyed)
func (h *Handlers) GetConfig(w http.ResponseWriter, r *http.Request) {
	scale, _ := h.d.Settings.Get(r.Context(), damageScaleKey, "tier3")
	writeJSON(w, http.StatusOK, map[string]any{"damageScale": scale})
}

// PATCH /api/v1/config — flips a GLOBAL setting (the damage_scale) that applies to
// ALL clients and crises. Restricted to senior oversight roles (Regional Bureau /
// Crisis Bureau); field validators, CO analysts and viewers get 403.
func (h *Handlers) PatchConfig(w http.ResponseWriter, r *http.Request) {
	if u := UserFromContext(r.Context()); u == nil || !u.CanManageGlobalConfig() {
		writeErr(w, http.StatusForbidden, "forbidden", "global config requires a Regional Bureau or Crisis Bureau role")
		return
	}
	var body struct {
		DamageScale string `json:"damageScale"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "invalid body")
		return
	}
	if body.DamageScale != "" {
		if body.DamageScale != "tier3" && body.DamageScale != "ems98" {
			writeErr(w, http.StatusBadRequest, "bad_request", "damageScale must be tier3 or ems98")
			return
		}
		if err := h.d.Settings.Set(r.Context(), damageScaleKey, body.DamageScale); err != nil {
			writeErr(w, http.StatusInternalServerError, "internal", "update failed")
			return
		}
	}
	scale, _ := h.d.Settings.Get(r.Context(), damageScaleKey, "tier3")
	writeJSON(w, http.StatusOK, map[string]any{"damageScale": scale})
}
