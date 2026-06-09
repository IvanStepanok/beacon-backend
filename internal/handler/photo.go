package handler

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

const maxPhotoBytes = 12 << 20 // 12 MB

// firstUploadWindow caps how long after capture/creation an anonymous device with
// NO stored identity may still attach the report's first photo. Bounds the window
// in which a report whose creator is unknown can be claimed by any caller.
const firstUploadWindow = 30 * time.Minute

// sniffImage validates that the first bytes are a real image we accept (JPEG, PNG,
// WEBP). Returns false for anything else so non-image payloads are rejected (415).
func sniffImage(b []byte) bool {
	switch {
	case len(b) >= 3 && b[0] == 0xFF && b[1] == 0xD8 && b[2] == 0xFF: // JPEG SOI
		return true
	case len(b) >= 8 && bytes.Equal(b[:8], []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}): // PNG
		return true
	case len(b) >= 12 && bytes.Equal(b[0:4], []byte("RIFF")) && bytes.Equal(b[8:12], []byte("WEBP")): // WEBP (RIFF....WEBP)
		return true
	default:
		return false
	}
}

// UploadPhoto stores a report's captured image (multipart "file") on the photo volume
// and records its URL. The route is anonymous (mobile uses X-Device-Id, no JWT), but
// the upload is BOUND two ways so a stranger can't overwrite someone else's photo:
//   - content sniffing: only real JPEG/PNG/WEBP bytes are accepted (415 otherwise);
//   - ownership: if the report stored its creating device id (submitter), the caller's
//     X-Device-Id MUST match it (403 otherwise). If no device id was stored, only a
//     FIRST upload (no photo yet) within firstUploadWindow of capture is allowed.
func (h *Handlers) UploadPhoto(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		writeErr(w, http.StatusBadRequest, "bad_id", "missing report id")
		return
	}

	// Authorize the upload against the report's identity/state BEFORE reading the body.
	auth, err := h.d.Reports.PhotoUploadInfo(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "db", "lookup failed")
		return
	}
	if !auth.Found {
		writeErr(w, http.StatusNotFound, "not_found", "no such report")
		return
	}
	caller := deviceID(r)
	if auth.DeviceID != nil && *auth.DeviceID != "" {
		// Report has a known creating device → caller must prove they are it.
		if caller == "" || caller != *auth.DeviceID {
			writeErr(w, http.StatusForbidden, "forbidden", "only the reporting device may upload this photo")
			return
		}
	} else {
		// No creating device recorded → allow first-upload-only, and only briefly.
		if auth.HasPhoto {
			writeErr(w, http.StatusForbidden, "forbidden", "photo already present for this report")
			return
		}
		if time.Since(auth.CapturedAt) > firstUploadWindow {
			writeErr(w, http.StatusForbidden, "forbidden", "upload window for this report has closed")
			return
		}
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxPhotoBytes+4096)
	if err := r.ParseMultipartForm(maxPhotoBytes); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_upload", "invalid or too-large multipart upload")
		return
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "no_file", "missing 'file' part")
		return
	}
	defer file.Close()

	// Sniff the first 512 bytes for a real image signature; reject anything else.
	head := make([]byte, 512)
	n, _ := io.ReadFull(file, head)
	head = head[:n]
	if !sniffImage(head) {
		writeErr(w, http.StatusUnsupportedMediaType, "unsupported_media", "file is not a JPEG/PNG/WEBP image")
		return
	}

	if err := os.MkdirAll(h.d.PhotoDir, 0o755); err != nil {
		writeErr(w, http.StatusInternalServerError, "store", "cannot create photo directory")
		return
	}
	path := filepath.Join(h.d.PhotoDir, photoFileName(id))
	out, err := os.Create(path)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "store", "cannot write photo")
		return
	}
	defer out.Close()
	// Write back the sniffed head, then stream the remainder.
	if _, err := out.Write(head); err != nil {
		writeErr(w, http.StatusInternalServerError, "store", "photo write failed")
		return
	}
	if _, err := io.Copy(out, file); err != nil {
		writeErr(w, http.StatusInternalServerError, "store", "photo write failed")
		return
	}

	url := fmt.Sprintf("/api/v1/reports/%s/photo", id)
	ok, err := h.d.Reports.SetPhotoURL(r.Context(), id, url)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "db", "cannot record photo")
		return
	}
	if !ok {
		_ = os.Remove(path)
		writeErr(w, http.StatusNotFound, "not_found", "no such report")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"photoUrl": url})
}

// GetPhoto serves a previously-uploaded report photo.
//
// Visibility mirrors the report projection policy:
//   - anonymous / low-trust external_viewer: served ONLY when the report's
//     verification == 'verified' (an unverified photo 404s so its existence/content
//     is never leaked); external_viewer must also be in scope for the report's crisis;
//   - real analyst roles (field_validator/co_analyst/regional_analyst/crisis_admin):
//     may fetch any photo for a crisis IN THEIR SCOPE (any status).
//
// Authorization runs BEFORE the file is opened, and an unauthorized/out-of-scope
// request returns a plain 404 (never reveals whether the photo file exists).
func (h *Handlers) GetPhoto(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	found, verification, crisisID, err := h.d.Reports.PhotoServeInfo(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "lookup failed")
		return
	}
	if !found {
		http.NotFound(w, r)
		return
	}

	u := UserFromContext(r.Context())
	switch {
	case u == nil:
		// Anonymous: verified photos only.
		if verification != "verified" {
			http.NotFound(w, r)
			return
		}
	case u.IsViewerTier():
		// External viewer: verified only AND must be in scope for the crisis.
		if verification != "verified" {
			http.NotFound(w, r)
			return
		}
		inScope := (crisisID == "" && u.ScopeAll()) || (crisisID != "" && u.ScopeAllows(crisisID))
		if !inScope {
			http.NotFound(w, r)
			return
		}
	default:
		// Real analyst: any status, but only within their crisis scope (a pending
		// report with no crisis is visible only to org-wide scope holders).
		inScope := (crisisID == "" && u.ScopeAll()) || (crisisID != "" && u.ScopeAllows(crisisID))
		if !inScope {
			http.NotFound(w, r)
			return
		}
	}

	path := filepath.Join(h.d.PhotoDir, photoFileName(id))
	f, err := os.Open(path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "image/jpeg")
	// Per-report visibility depends on auth/verification → don't let shared caches
	// serve it to the wrong tier.
	w.Header().Set("Cache-Control", "private, max-age=86400")
	_, _ = io.Copy(w, f)
}

// photoFileName maps a report id to a safe on-disk filename (ids are simple, but be defensive).
func photoFileName(id string) string {
	safe := strings.NewReplacer("/", "_", "\\", "_", "..", "_", " ", "_").Replace(id)
	return safe + ".jpg"
}
