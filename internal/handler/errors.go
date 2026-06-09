// Package handler holds the chi HTTP handlers: decode → call service → encode,
// mapping domain errors to status codes via a small JSON error envelope.
package handler

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/stepanok/beacon-server/internal/service"
)

type errEnvelope struct {
	Error   string `json:"error"`
	Message string `json:"message,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

func writeErr(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, errEnvelope{Error: code, Message: msg})
}

// decodeJSON reads a JSON body with a 2 MiB size cap; unknown fields are
// tolerated (the lenient flat+nested alias/superset contract). Decode errors are
// returned as safe, generic messages — the raw json.Decoder text (which leaks Go
// type/field names) is never echoed to the client.
func decodeJSON(r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(nil, r.Body, 2<<20) // 2 MiB
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(dst); err != nil {
		switch {
		case errors.Is(err, io.EOF):
			return errors.New("empty request body")
		default:
			var syntaxErr *json.SyntaxError
			var typeErr *json.UnmarshalTypeError
			var maxErr *http.MaxBytesError
			switch {
			case errors.As(err, &maxErr):
				return errors.New("request body too large")
			case errors.As(err, &syntaxErr), errors.As(err, &typeErr):
				return errors.New("malformed JSON body")
			default:
				return errors.New("invalid request body")
			}
		}
	}
	return nil
}

// mapServiceError converts service/store errors to an HTTP status + envelope.
func mapServiceError(w http.ResponseWriter, err error) {
	var ve service.ValidationError
	var rle service.RateLimitError
	var de service.DuplicateError
	switch {
	case errors.As(err, &ve):
		writeErr(w, http.StatusBadRequest, "validation", ve.Error())
	case errors.As(err, &rle):
		// 429: too many submits from this device within the window (anti-abuse).
		writeErr(w, http.StatusTooManyRequests, "rate_limited", rle.Error())
	case errors.As(err, &de):
		// 409: genuine near-duplicate; reference the existing report id so the client
		// can route the user to it instead of creating another near-identical row.
		writeJSON(w, http.StatusConflict, struct {
			Error      string `json:"error"`
			Message    string `json:"message,omitempty"`
			ExistingID string `json:"existingId,omitempty"`
		}{Error: "duplicate", Message: de.Error(), ExistingID: de.ExistingID})
	case errors.Is(err, service.ErrUnsupportedFormat):
		writeErr(w, http.StatusNotImplemented, "unsupported_format", err.Error())
	default:
		writeErr(w, http.StatusInternalServerError, "internal", "internal error")
	}
}
