package handler

import (
	"net/http"
	"os"
	"path/filepath"

	"github.com/go-chi/chi/v5"
)

// WithdrawReport lets a reporter ERASE their OWN report (data-subject takedown right).
// The route is anonymous (mobile uses X-Device-Id, no JWT) but bound to the creating
// device: the caller's X-Device-Id MUST match the device that submitted the report
// (403 otherwise; 404 if no such report). On success the report row is erased AND its
// stored photo file (if any) deleted — true erasure, not hiding. A non-PII row is kept
// in report_withdrawals for accountability (see store.WithdrawReport / DPIA).
func (h *Handlers) WithdrawReport(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		writeErr(w, http.StatusBadRequest, "bad_id", "missing report id")
		return
	}
	caller := deviceID(r)
	if caller == "" {
		writeErr(w, http.StatusBadRequest, "device_id_required", "X-Device-Id header is required to withdraw a report")
		return
	}
	out, err := h.d.Reports.WithdrawReport(r.Context(), id, caller)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "db", "withdraw failed")
		return
	}
	if !out.Found {
		writeErr(w, http.StatusNotFound, "not_found", "no such report")
		return
	}
	if !out.Owned {
		writeErr(w, http.StatusForbidden, "forbidden", "only the reporting device may withdraw this report")
		return
	}
	// Erase the stored photo too (the most sensitive artifact) — best-effort, after the
	// row is gone. photoFileName is defined in photo.go (same package).
	_ = os.Remove(filepath.Join(h.d.PhotoDir, photoFileName(id)))
	writeJSON(w, http.StatusOK, map[string]any{"withdrawn": true, "id": id})
}
