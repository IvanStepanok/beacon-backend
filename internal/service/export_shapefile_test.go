package service

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"math"
	"testing"
	"time"

	"github.com/stepanok/beacon-server/internal/model"
)

// TestToShapefile_RoundTrip produces a shapefile ZIP, re-reads its members, and
// verifies the .shp header (file code 9994, shape type Point), that the single
// RESOLVED report became exactly one POINT record at the right X/Y (the unresolved
// one is skipped, never emitted at 0,0), and the .dbf record count agrees.
func TestToShapefile_RoundTrip(t *testing.T) {
	lat, lng := 36.2021, 36.1601
	resolved := model.Report{
		ID: "r-1", DamageTier: "partial", Verification: "verified",
		InfraTypes: []string{"residential"}, CrisisNature: []string{"earthquake"},
		Lat: &lat, Lng: &lng, LocationResolved: true,
		CapturedAt: time.Now().UTC(),
	}
	landmark := "by the central market"
	unresolved := model.Report{ID: "r-2", DamageTier: "minimal", LocationResolved: false, Landmark: &landmark}

	zipBytes, err := ToShapefile([]model.Report{resolved, unresolved})
	if err != nil {
		t.Fatalf("ToShapefile: %v", err)
	}

	zr, err := zip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	if err != nil {
		t.Fatalf("zip open: %v", err)
	}
	files := map[string][]byte{}
	for _, f := range zr.File {
		rc, _ := f.Open()
		buf := new(bytes.Buffer)
		_, _ = buf.ReadFrom(rc)
		_ = rc.Close()
		files[f.Name] = buf.Bytes()
	}
	for _, want := range []string{"beacon-reports.shp", "beacon-reports.shx", "beacon-reports.dbf", "beacon-reports.prj"} {
		if _, ok := files[want]; !ok {
			t.Errorf("missing shapefile member %q", want)
		}
	}

	shp := files["beacon-reports.shp"]
	if len(shp) < 100 {
		t.Fatalf("shp too short: %d bytes", len(shp))
	}
	if fc := binary.BigEndian.Uint32(shp[0:]); fc != 9994 {
		t.Errorf("shp file code = %d, want 9994", fc)
	}
	if st := binary.LittleEndian.Uint32(shp[32:]); st != 1 {
		t.Errorf("shp shape type = %d, want 1 (Point)", st)
	}

	// Exactly ONE record (the unresolved report must be skipped): .shx is 8 bytes/record.
	if got := (len(files["beacon-reports.shx"]) - 100) / 8; got != 1 {
		t.Errorf("shx record count = %d, want 1 (unresolved report must be skipped)", got)
	}

	// First record content (after the 100-byte header + 8-byte record header).
	off := 100 + 8
	gotType := binary.LittleEndian.Uint32(shp[off:])
	gotX := math.Float64frombits(binary.LittleEndian.Uint64(shp[off+4:]))
	gotY := math.Float64frombits(binary.LittleEndian.Uint64(shp[off+12:]))
	if gotType != 1 || gotX != lng || gotY != lat {
		t.Errorf("point = type %d (%v,%v), want 1 (%v,%v)", gotType, gotX, gotY, lng, lat)
	}

	if n := binary.LittleEndian.Uint32(files["beacon-reports.dbf"][4:]); n != 1 {
		t.Errorf("dbf record count = %d, want 1", n)
	}
}
