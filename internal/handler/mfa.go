package handler

import (
	"net/http"
	"strings"
	"time"

	"github.com/stepanok/beacon-server/internal/auth"
	"github.com/stepanok/beacon-server/internal/crypto"
)

// mfaCodeRequest is the body for verify/disable: a 6-digit TOTP code.
type mfaCodeRequest struct {
	Code string `json:"code"`
}

// EnrollMFA (POST /api/v1/auth/mfa/enroll) generates a new TOTP secret for the
// authenticated analyst, stores it ENCRYPTED as pending (not yet enabled), and returns
// the otpauth:// URI + base32 secret for the authenticator app. MFA only turns on once
// the analyst confirms a code via /verify.
func (h *Handlers) EnrollMFA(w http.ResponseWriter, r *http.Request) {
	u := UserFromContext(r.Context())
	if u == nil {
		writeErr(w, http.StatusUnauthorized, "unauthorized", "login required")
		return
	}
	if len(h.d.DataEncryptionKey) != crypto.KeyLen {
		writeErr(w, http.StatusServiceUnavailable, "mfa_unavailable", "server is not configured for MFA (no encryption key)")
		return
	}
	secret, err := auth.GenerateTOTPSecret()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "could not generate secret")
		return
	}
	enc, err := crypto.SealString(h.d.DataEncryptionKey, secret)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "could not secure secret")
		return
	}
	if err := h.d.Users.SetMFAPending(r.Context(), u.ID, enc); err != nil {
		writeErr(w, http.StatusInternalServerError, "db", "could not store secret")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"secret":     secret,
		"otpauthUri": auth.TOTPProvisioningURI(secret, u.Email, "Beacon"),
	})
}

// VerifyMFA (POST /api/v1/auth/mfa/verify) confirms a code against the pending secret
// and, on success, enables MFA for the account.
func (h *Handlers) VerifyMFA(w http.ResponseWriter, r *http.Request) {
	u := UserFromContext(r.Context())
	if u == nil {
		writeErr(w, http.StatusUnauthorized, "unauthorized", "login required")
		return
	}
	var req mfaCodeRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_json", err.Error())
		return
	}
	if len(h.d.DataEncryptionKey) != crypto.KeyLen {
		writeErr(w, http.StatusServiceUnavailable, "mfa_unavailable", "server is not configured for MFA")
		return
	}
	_, enc, err := h.d.Users.GetMFAByID(r.Context(), u.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "db", "lookup failed")
		return
	}
	if enc == "" {
		writeErr(w, http.StatusBadRequest, "mfa_not_enrolled", "no pending MFA secret — call enroll first")
		return
	}
	secret, derr := crypto.OpenString(h.d.DataEncryptionKey, enc)
	if derr != nil || !auth.ValidateTOTP(secret, strings.TrimSpace(req.Code), time.Now()) {
		writeErr(w, http.StatusUnauthorized, "mfa_invalid", "invalid authenticator code")
		return
	}
	if err := h.d.Users.EnableMFA(r.Context(), u.ID); err != nil {
		writeErr(w, http.StatusInternalServerError, "db", "could not enable MFA")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"mfaEnabled": true})
}

// DisableMFA (POST /api/v1/auth/mfa/disable) turns MFA off — but only after the caller
// proves possession with a current code, so a hijacked session cannot silently strip it.
func (h *Handlers) DisableMFA(w http.ResponseWriter, r *http.Request) {
	u := UserFromContext(r.Context())
	if u == nil {
		writeErr(w, http.StatusUnauthorized, "unauthorized", "login required")
		return
	}
	var req mfaCodeRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_json", err.Error())
		return
	}
	enabled, enc, err := h.d.Users.GetMFAByID(r.Context(), u.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "db", "lookup failed")
		return
	}
	if !enabled || enc == "" {
		writeJSON(w, http.StatusOK, map[string]bool{"mfaEnabled": false})
		return
	}
	if len(h.d.DataEncryptionKey) != crypto.KeyLen {
		writeErr(w, http.StatusServiceUnavailable, "mfa_unavailable", "server is not configured for MFA")
		return
	}
	secret, derr := crypto.OpenString(h.d.DataEncryptionKey, enc)
	if derr != nil || !auth.ValidateTOTP(secret, strings.TrimSpace(req.Code), time.Now()) {
		writeErr(w, http.StatusUnauthorized, "mfa_invalid", "invalid authenticator code")
		return
	}
	if err := h.d.Users.DisableMFA(r.Context(), u.ID); err != nil {
		writeErr(w, http.StatusInternalServerError, "db", "could not disable MFA")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"mfaEnabled": false})
}
