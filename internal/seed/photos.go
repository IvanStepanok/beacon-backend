package seed

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/stepanok/beacon-server/internal/model"
)

// Embedded demo evidence photos: genuinely free-licensed ground-level shots of
// the 2023 Kahramanmaraş-earthquake damage in Hatay (per-file author/license/
// source in photos/ATTRIBUTION.md; all public-domain VOA or CC BY-SA 4.0,
// downscaled to ≤1200px). At seed time they are written into PHOTO_DIR — the
// same directory POST /reports/{id}/photo stores uploads in — so a seeded
// report's photoUrl serves a real image. Without this the demo showed VERIFIED
// reports with no photo at all, contradicting the verification photo gate.
//
//go:embed photos/*.jpg
var photosFS embed.FS

// seedPhoto is one embedded demo photo; SizeBytes is the REAL embedded file
// size (seeded reports must never claim a fabricated payload size).
type seedPhoto struct {
	name string
	data []byte
}

// loadSeedPhotos reads every embedded photo, sorted by name so the round-robin
// assignment is deterministic across builds and platforms.
func loadSeedPhotos() ([]seedPhoto, error) {
	entries, err := photosFS.ReadDir("photos")
	if err != nil {
		return nil, err
	}
	out := make([]seedPhoto, 0, len(entries))
	for _, e := range entries {
		data, err := photosFS.ReadFile("photos/" + e.Name())
		if err != nil {
			return nil, fmt.Errorf("embedded photo %s: %w", e.Name(), err)
		}
		out = append(out, seedPhoto{name: e.Name(), data: data})
	}
	sort.Slice(out, func(a, b int) bool { return out[a].name < out[b].name })
	return out, nil
}

// assignSeedPhotos attaches embedded photos to the seeded reports, round-robin:
//
//   - every VERIFIED report gets one (the photo gate means verified-without-
//     evidence must never appear, least of all in the demo);
//   - pending/flagged reports at even slice indices get one too (a realistic
//     mix — most field reports carry a photo), while the odd ones stay
//     photo-less so the photo-gate 409/force flow stays demonstrable.
//
// photoUrl follows the live upload contract (/api/v1/reports/{id}/photo) and
// SizeBytes becomes the photo's real embedded size; a photo-less report gets
// SizeBytes 0, exactly what the live submit path computes for zero photos.
// Index-keyed (no rnd() calls), so the golden parity sequence is untouched.
func assignSeedPhotos(reps []model.Report, photos []seedPhoto) {
	if len(photos) == 0 {
		return
	}
	next := 0
	for i := range reps {
		r := &reps[i]
		if r.Verification != "verified" && i%2 != 0 {
			r.Photos = []model.PhotoRef{}
			r.SizeBytes = 0
			continue
		}
		p := photos[next%len(photos)]
		next++
		url := fmt.Sprintf("/api/v1/reports/%s/photo", r.ID)
		r.PhotoURL = &url
		r.Photos = []model.PhotoRef{{LocalPath: p.name, RemoteURL: &url, SizeBytes: int64(len(p.data))}}
		r.SizeBytes = int64(len(p.data))
	}
}

// installSeedPhoto writes a report's assigned photo into photoDir under the
// id-derived filename the photo handler serves (handler.photoFileName: seed ids
// are plain numerics, so it is simply "<id>.jpg").
func installSeedPhoto(photoDir string, r model.Report, photos []seedPhoto) error {
	if r.PhotoURL == nil || len(r.Photos) == 0 {
		return nil
	}
	for _, p := range photos {
		if p.name != r.Photos[0].LocalPath {
			continue
		}
		if err := os.MkdirAll(photoDir, 0o755); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(photoDir, r.ID+".jpg"), p.data, 0o644)
	}
	return fmt.Errorf("report %s references unknown seed photo %q", r.ID, r.Photos[0].LocalPath)
}
