package handler

import (
	"net/http"
	"strings"
	"time"

	"github.com/stepanok/beacon-server/internal/auth"
	"github.com/stepanok/beacon-server/internal/crypto"
	"github.com/stepanok/beacon-server/internal/model"
)

// POST /api/v1/auth/login — email + password (+ TOTP code when MFA is enabled) →
// signed JWT + the user profile.
func (h *Handlers) Login(w http.ResponseWriter, r *http.Request) {
	var req model.LoginRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_json", err.Error())
		return
	}
	user, hash, mfaEnabled, encSecret, err := h.d.Users.GetByEmail(r.Context(), req.Email)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "login failed")
		return
	}
	// Same response for unknown email and wrong password (no user enumeration).
	if user == nil || !auth.CheckPassword(hash, req.Password) {
		writeErr(w, http.StatusUnauthorized, "invalid_credentials", "invalid email or password")
		return
	}
	// Second factor: an MFA-enabled account must present a valid TOTP code. The password
	// is already verified here, so returning "mfa_required" is not an enumeration risk —
	// it only tells an already-authenticated caller to supply the code.
	if mfaEnabled {
		if len(h.d.DataEncryptionKey) != crypto.KeyLen {
			// Can't decrypt the stored secret → cannot verify the second factor → fail closed.
			writeErr(w, http.StatusInternalServerError, "internal", "mfa verification unavailable")
			return
		}
		if strings.TrimSpace(req.MfaCode) == "" {
			writeErr(w, http.StatusUnauthorized, "mfa_required", "this account requires a 6-digit authenticator code")
			return
		}
		secret, derr := crypto.OpenString(h.d.DataEncryptionKey, encSecret)
		if derr != nil || !auth.ValidateTOTP(secret, req.MfaCode, time.Now()) {
			writeErr(w, http.StatusUnauthorized, "mfa_invalid", "invalid authenticator code")
			return
		}
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
