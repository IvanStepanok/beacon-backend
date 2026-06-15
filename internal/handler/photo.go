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

	"github.com/stepanok/beacon-server/internal/crypto"
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

	// Read the whole (bounded) upload so metadata can be stripped before it is stored.
	data, err := io.ReadAll(io.LimitReader(io.MultiReader(bytes.NewReader(head), file), maxPhotoBytes+1))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_upload", "could not read the uploaded image")
		return
	}
	if len(data) > maxPhotoBytes {
		writeErr(w, http.StatusRequestEntityTooLarge, "too_large", "image exceeds the size limit")
		return
	}
	// Defense-in-depth: strip EXIF/XMP (GPS, device make/model, capture timestamp) +
	// comments server-side for JPEG. The mobile client already strips EXIF, but the
	// server must NOT trust the client's `exifStripped` flag — a non-Beacon client
	// (the API is anonymous) could upload a GPS-tagged photo and lie about it.
	data = stripJPEGMetadata(data)

	// Encrypt the photo AT REST (AES-256-GCM) when a key is configured. The stored bytes
	// carry a magic header so reads transparently handle both encrypted and legacy
	// plaintext files. Photos are the highest-sensitivity payload (faces, locations).
	if len(h.d.DataEncryptionKey) == crypto.KeyLen {
		enc, encErr := crypto.Seal(h.d.DataEncryptionKey, data)
		if encErr != nil {
			writeErr(w, http.StatusInternalServerError, "store", "cannot secure photo")
			return
		}
		data = enc
	}

	if err := os.MkdirAll(h.d.PhotoDir, 0o755); err != nil {
		writeErr(w, http.StatusInternalServerError, "store", "cannot create photo directory")
		return
	}
	path := filepath.Join(h.d.PhotoDir, photoFileName(id))
	if err := os.WriteFile(path, data, 0o600); err != nil {
		writeErr(w, http.StatusInternalServerError, "store", "cannot write photo")
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
	raw, err := os.ReadFile(path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	// Decrypt at-rest photos transparently; serve any legacy plaintext (no magic) as-is.
	if crypto.IsEncrypted(raw) {
		if len(h.d.DataEncryptionKey) != crypto.KeyLen {
			http.NotFound(w, r) // encrypted on disk but no key to read it → treat as absent
			return
		}
		dec, derr := crypto.Open(h.d.DataEncryptionKey, raw)
		if derr != nil {
			http.NotFound(w, r) // tampered/garbled → never serve raw ciphertext
			return
		}
		raw = dec
	}
	w.Header().Set("Content-Type", "image/jpeg")
	// Per-report visibility depends on auth/verification → don't let shared caches
	// serve it to the wrong tier.
	w.Header().Set("Cache-Control", "private, max-age=86400")
	_, _ = w.Write(raw)
}

// photoFileName maps a report id to a safe on-disk filename (ids are simple, but be defensive).
func photoFileName(id string) string {
	safe := strings.NewReplacer("/", "_", "\\", "_", "..", "_", " ", "_").Replace(id)
	return safe + ".jpg"
}

// stripJPEGMetadata losslessly removes the privacy-sensitive metadata segments from a
// JPEG — APP1 (EXIF + XMP: GPS coordinates, device make/model, capture timestamp) and
// COM (free-text comments) — by walking the segment markers and copying everything
// EXCEPT those segments. It does NOT re-encode (no quality loss, no CPU cost) and
// preserves rendering-relevant segments (APP0/JFIF, APP2/ICC, quantization/Huffman
// tables, the compressed scan). Non-JPEG or malformed input is returned unchanged
// (PNG/WEBP carry far less location metadata and are passed through as-is).
func stripJPEGMetadata(b []byte) []byte {
	if len(b) < 4 || b[0] != 0xFF || b[1] != 0xD8 { // not a JPEG (no SOI)
		return b
	}
	out := make([]byte, 0, len(b))
	out = append(out, 0xFF, 0xD8) // SOI
	i := 2
	for i+1 < len(b) {
		if b[i] != 0xFF { // lost marker alignment — copy the remainder verbatim, defensively
			return append(out, b[i:]...)
		}
		marker := b[i+1]
		// SOS begins the entropy-coded scan; everything from here to EOI is copied as-is.
		// EOI ends the image. Neither has a length we should parse.
		if marker == 0xDA || marker == 0xD9 {
			return append(out, b[i:]...)
		}
		// Length-bearing segment: 2-byte big-endian length follows the marker (incl. itself).
		if i+3 >= len(b) {
			return append(out, b[i:]...)
		}
		segLen := int(b[i+2])<<8 | int(b[i+3])
		if segLen < 2 || i+2+segLen > len(b) {
			return append(out, b[i:]...) // malformed length — bail out, copy remainder
		}
		// Drop ONLY the metadata carriers: APP1 (EXIF/XMP) and COM (comment). Keep all
		// other APPn (e.g. APP0 JFIF, APP2 ICC color) so the image still renders correctly.
		if marker != 0xE1 && marker != 0xFE {
			out = append(out, b[i:i+2+segLen]...)
		}
		i += 2 + segLen
	}
	return out
}
