package service

import (
	"math"
	"testing"

	"github.com/stepanok/beacon-server/internal/model"
)

// TestNormalize_NoCrisisNatureFabrication locks the no-fabrication rule: an empty
// crisisNature stays EMPTY — the server must never invent an "earthquake" the
// reporter did not assert.
func TestNormalize_NoCrisisNatureFabrication(t *testing.T) {
	r, err := normalize(model.SubmitReportRequest{ID: "r-1", Damage: "partial", Lat: f64(36.2), Lng: f64(36.16)}, nil)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if len(r.CrisisNature) != 0 {
		t.Errorf("empty crisisNature must stay empty, got %v", r.CrisisNature)
	}
	if r.CrisisNature == nil {
		t.Errorf("crisisNature must normalize to an empty slice (NOT NULL column), got nil")
	}

	// An explicit value is kept verbatim.
	r, _ = normalize(model.SubmitReportRequest{ID: "r-2", Damage: "partial", Lat: f64(36.2), Lng: f64(36.16),
		CrisisNature: []string{"flood"}}, nil)
	if len(r.CrisisNature) != 1 || r.CrisisNature[0] != "flood" {
		t.Errorf("explicit crisisNature must be kept, got %v", r.CrisisNature)
	}
}

// TestNormalize_PlaceSanitation locks the "Your location" placeholder rule: the
// client UI placeholder is never stored as a place (case-insensitive, trimmed).
func TestNormalize_PlaceSanitation(t *testing.T) {
	cases := map[string]string{
		"Your location":    "",
		"  your location ": "",
		"YOUR LOCATION":    "",
		"Antakya":          "Antakya",
		"Your locations":   "Your locations", // not the placeholder — kept
		"":                 "",
	}
	for in, want := range cases {
		r, err := normalize(model.SubmitReportRequest{ID: "r-1", Damage: "partial", Lat: f64(36.2), Lng: f64(36.16), Place: in}, nil)
		if err != nil {
			t.Fatalf("normalize(%q): %v", in, err)
		}
		if r.Place != want {
			t.Errorf("place %q normalized to %q, want %q", in, r.Place, want)
		}
	}
}

// TestNormalize_PlusCodeFallback locks the plus_code consolidation: plusCode is
// canonical, the legacy what3words submit key is a fallback, and the normalized
// report carries the same value under both fields.
func TestNormalize_PlusCodeFallback(t *testing.T) {
	base := model.SubmitReportRequest{ID: "r-1", Damage: "partial", Lat: f64(36.2), Lng: f64(36.16)}

	req := base
	req.PlusCode = strPtr2("8G7F6526+VC")
	req.What3Words = strPtr2("legacy-value")
	r, _ := normalize(req, nil)
	if r.PlusCode == nil || *r.PlusCode != "8G7F6526+VC" {
		t.Errorf("plusCode must win over what3words, got %v", r.PlusCode)
	}

	req = base
	req.What3Words = strPtr2("8G7F6526+VC")
	r, _ = normalize(req, nil)
	if r.PlusCode == nil || *r.PlusCode != "8G7F6526+VC" {
		t.Errorf("legacy what3words must fall back into plusCode, got %v", r.PlusCode)
	}
	if r.What3Words == nil || *r.What3Words != "8G7F6526+VC" {
		t.Errorf("what3words must alias the same value, got %v", r.What3Words)
	}
}

// TestIsFootprintReport locks the near-dup exemption: ONLY a real tapped footprint
// (buildingSource=="footprint" or the legacy "fp-" id prefix) skips the 25m/10min
// guard; synthetic GPS-grid "b-" ids and everything else stay guarded.
func TestIsFootprintReport(t *testing.T) {
	cases := []struct {
		name     string
		building *string
		source   *string
		want     bool
	}{
		{"no building", nil, nil, false},
		{"gps-grid b- id", strPtr2("b-362000-361600"), nil, false},
		{"arbitrary id", strPtr2("osm-12345"), nil, false},
		{"legacy fp- prefix", strPtr2("fp-12345"), nil, true},
		{"buildingSource footprint", strPtr2("b-362000-361600"), strPtr2("footprint"), true},
		{"buildingSource non-footprint", strPtr2("b-362000-361600"), strPtr2("gps_grid"), false},
		{"footprint source without building", nil, strPtr2("footprint"), true},
	}
	for _, c := range cases {
		r := model.Report{BuildingID: c.building, BuildingSource: c.source}
		if got := isFootprintReport(r); got != c.want {
			t.Errorf("%s: isFootprintReport = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestPinContainmentLimitKm locks the containment threshold: max(r*1.5, r+5).
func TestPinContainmentLimitKm(t *testing.T) {
	cases := map[float64]float64{40: 60, 10: 15, 2: 7, 0.5: 5.5}
	for radius, want := range cases {
		if got := pinContainmentLimitKm(radius); math.Abs(got-want) > 1e-9 {
			t.Errorf("pinContainmentLimitKm(%v) = %v, want %v", radius, got, want)
		}
	}
}

// TestPinOutsideContainment exercises the stale-pin detection with real distances
// (1 degree of latitude ≈ 111.2 km).
func TestPinOutsideContainment(t *testing.T) {
	crisis := model.Crisis{CenterLat: 36.2, CenterLng: 36.16, RadiusKm: 40} // limit = 60 km

	// ~55.6 km north: inside the 60 km limit → pin kept.
	if pinOutsideContainment(36.7, 36.16, crisis) {
		t.Errorf("point ~55km out (limit 60km) must keep the pin")
	}
	// ~78 km north: beyond the limit → pin ignored.
	if !pinOutsideContainment(36.9, 36.16, crisis) {
		t.Errorf("point ~78km out (limit 60km) must drop the pin")
	}

	// Small crisis (radius 2 → limit 7 km): 5 km in, 8 km out.
	small := model.Crisis{CenterLat: 36.2, CenterLng: 36.16, RadiusKm: 2}
	if pinOutsideContainment(36.245, 36.16, small) { // ~5 km
		t.Errorf("point ~5km out (limit 7km) must keep the pin")
	}
	if !pinOutsideContainment(36.272, 36.16, small) { // ~8 km
		t.Errorf("point ~8km out (limit 7km) must drop the pin")
	}

	// No usable radius → never un-pin.
	if pinOutsideContainment(0, 0, model.Crisis{CenterLat: 36.2, CenterLng: 36.16, RadiusKm: 0}) {
		t.Errorf("a crisis without a radius must never drop the pin")
	}
}

func TestHaversineKm(t *testing.T) {
	// One degree of latitude ≈ 111.19 km everywhere.
	if d := haversineKm(36.0, 36.0, 37.0, 36.0); math.Abs(d-111.19) > 0.5 {
		t.Errorf("1° latitude = %v km, want ≈111.19", d)
	}
	// Identical points → 0.
	if d := haversineKm(36.2, 36.16, 36.2, 36.16); d != 0 {
		t.Errorf("zero distance, got %v", d)
	}
}

// strPtr2 avoids clashing with the export test's strPtr helper.
func strPtr2(s string) *string { return &s }
