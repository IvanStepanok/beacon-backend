package handler

import (
	"net/http"
	"time"

	"github.com/stepanok/beacon-server/internal/auth"
	"github.com/stepanok/beacon-server/internal/model"
)

// POST /api/v1/auth/login — email + password → signed JWT + the user profile.
func (h *Handlers) Login(w http.ResponseWriter, r *http.Request) {
	var req model.LoginRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_json", err.Error())
		return
	}
	user, hash, err := h.d.Users.GetByEmail(r.Context(), req.Email)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "login failed")
		return
	}
	// Same response for unknown email and wrong password (no user enumeration).
	if user == nil || !auth.CheckPassword(hash, req.Password) {
		writeErr(w, http.StatusUnauthorized, "invalid_credentials", "invalid email or password")
		return
	}
	token, err := auth.Issue(*user, h.d.JWTSecret, time.Now())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "token issue failed")
		return
	}
	writeJSON(w, http.StatusOK, model.LoginResponse{Token: token, User: *user})
}

// GET /api/v1/auth/me — the current analyst (requires a valid token).
func (h *Handlers) Me(w http.ResponseWriter, r *http.Request) {
	u := UserFromContext(r.Context())
	if u == nil {
		writeErr(w, http.StatusUnauthorized, "unauthorized", "login required")
		return
	}
	writeJSON(w, http.StatusOK, u)
}
