package service

import (
	"strings"
	"testing"

	"github.com/stepanok/beacon-server/internal/model"
)

// TestSubmitValidation locks the submit gate (normalize → validate), table-
// driven over the same lenient inbound shape both clients POST. A submission
// needs an id, a damage grade from either vocabulary, at least one infrastructure
// type, a crisis nature, and EITHER a resolved point OR a landmark; every enum
// field is closed (bad values are 400s, never silently stored). No DB — these are
// the pure pre-storage gates.
func TestSubmitValidation(t *testing.T) {
	// ok is a minimal valid request; each case mutates a copy.
	ok := func() model.SubmitReportRequest {
		return model.SubmitReportRequest{ID: "r-1", Damage: "partial", Lat: f64(36.2), Lng: f64(36.16), InfraTypes: []string{"residential"}, CrisisNature: []string{"earthquake"}}
	}

	cases := []struct {
		name    string
		mutate  func(*model.SubmitReportRequest)
		wantErr string // substring of the ValidationError; "" = must pass
	}{
		{"valid 3-tier", func(r *model.SubmitReportRequest) {}, ""},
		{"valid 5-level", func(r *model.SubmitReportRequest) { r.Damage = "severe" }, ""},
		{"missing id", func(r *model.SubmitReportRequest) { r.ID = "" }, "id is required"},
		{"missing damage", func(r *model.SubmitReportRequest) { r.Damage = "" }, "damage must be one of"},
		{"bad damage enum", func(r *model.SubmitReportRequest) { r.Damage = "catastrophic" }, "damage must be one of"},
		{"bad debris enum", func(r *model.SubmitReportRequest) { r.Debris = "maybe" }, "debris must be one of"},
		{"bad infraType", func(r *model.SubmitReportRequest) { r.InfraTypes = []string{"residential", "castle"} }, "invalid infraType"},
		{"missing infraType", func(r *model.SubmitReportRequest) { r.InfraTypes = nil }, "infrastructure type is required"},
		{"bad crisisNature", func(r *model.SubmitReportRequest) { r.CrisisNature = []string{"meteor"} }, "invalid crisisNature"},
		{"missing crisisNature", func(r *model.SubmitReportRequest) { r.CrisisNature = nil }, "crisis nature is required"},
		{"bad cluster", func(r *model.SubmitReportRequest) { r.Clusters = []string{"shelter"} }, "invalid cluster"},
		{"bad aiLevel", func(r *model.SubmitReportRequest) { r.AILevel = strPtr2("totaled") }, "aiLevel must be a damage level"},
		{"lat out of range", func(r *model.SubmitReportRequest) { r.Lat = f64(95) }, "lat/lng out of range"},
		{"lng out of range", func(r *model.SubmitReportRequest) { r.Lng = f64(181) }, "lat/lng out of range"},
		// Location-or-landmark: no coords and no landmark is rejected; a landmark
		// alone (location-unresolved) passes; a half-point (lat only) without a
		// landmark is rejected too (never silently stored as 0,0).
		{"no location, no landmark", func(r *model.SubmitReportRequest) { r.Lat, r.Lng = nil, nil }, "requires a landmark"},
		{"landmark only", func(r *model.SubmitReportRequest) {
			r.Lat, r.Lng = nil, nil
			r.Landmark = strPtr2("collapsed bridge by the market")
		}, ""},
		{"lat without lng, no landmark", func(r *model.SubmitReportRequest) { r.Lng = nil }, "requires a landmark"},
		{"explicit unresolved without landmark", func(r *model.SubmitReportRequest) {
			f := false
			r.LocationResolved = &f
			r.Landmark = nil
		}, "requires a landmark"},
	}

	for _, c := range cases {
		req := ok()
		c.mutate(&req)
		r, err := normalize(req, nil)
		if err == nil {
			err = validate(r)
		}
		switch {
		case c.wantErr == "" && err != nil:
			t.Errorf("%s: unexpected error %v", c.name, err)
		case c.wantErr != "" && err == nil:
			t.Errorf("%s: expected error containing %q, got nil", c.name, c.wantErr)
		case c.wantErr != "" && !strings.Contains(err.Error(), c.wantErr):
			t.Errorf("%s: error %q does not contain %q", c.name, err, c.wantErr)
		}
		if c.wantErr != "" {
			if _, isValidation := err.(ValidationError); !isValidation {
				t.Errorf("%s: error is %T, want ValidationError", c.name, err)
			}
		}
	}
}

// TestSubmitValidation_BuildingSourceWhitelist locks the buildingSource trust
// gate in normalize(): "footprint" is the ONLY accepted value (it exempts the
// report from the near-dup guard, so it must never be client-controlled beyond
// that one claim); any other string — flat or nested — normalizes to nil. The
// legacy "fp-" id prefix keeps its footprint exemption independently.
func TestSubmitValidation_BuildingSourceWhitelist(t *testing.T) {
	mk := func(mutate func(*model.SubmitReportRequest)) model.Report {
		req := model.SubmitReportRequest{ID: "r-bs", Damage: "partial", Lat: f64(36.2), Lng: f64(36.16)}
		mutate(&req)
		r, err := normalize(req, nil)
		if err != nil {
			t.Fatalf("normalize: %v", err)
		}
		return r
	}

	// Accepted verbatim: the single defined value.
	r := mk(func(req *model.SubmitReportRequest) { req.BuildingSource = strPtr2("footprint") })
	if r.BuildingSource == nil || *r.BuildingSource != "footprint" {
		t.Errorf("buildingSource footprint must be kept, got %v", r.BuildingSource)
	}
	if !isFootprintReport(r) {
		t.Errorf("buildingSource footprint must keep the dedup exemption")
	}

	// Everything else → nil (including case variants: the enum is closed).
	for _, bad := range []string{"satellite", "grid", "Footprint", "FOOTPRINT", ""} {
		r := mk(func(req *model.SubmitReportRequest) { req.BuildingSource = strPtr2(bad) })
		if r.BuildingSource != nil {
			t.Errorf("buildingSource %q must normalize to nil, got %q", bad, *r.BuildingSource)
		}
		if isFootprintReport(r) {
			t.Errorf("buildingSource %q must NOT grant the dedup exemption", bad)
		}
	}

	// The nested location alias goes through the same whitelist.
	r = mk(func(req *model.SubmitReportRequest) {
		req.Location = &model.ReportLocation{BuildingSource: strPtr2("drone_guess")}
	})
	if r.BuildingSource != nil {
		t.Errorf("nested buildingSource must normalize to nil, got %q", *r.BuildingSource)
	}

	// Legacy clients without buildingSource: the "fp-" id prefix alone still
	// marks a real footprint (the exemption the dedup guard honors).
	r = mk(func(req *model.SubmitReportRequest) { req.BuildingID = strPtr2("fp-1a2b3c") })
	if r.BuildingSource != nil {
		t.Errorf("fp- id must not fabricate a buildingSource, got %q", *r.BuildingSource)
	}
	if !isFootprintReport(r) {
		t.Errorf("legacy fp- id prefix must keep the footprint exemption")
	}
}

// TestNormalize_NoEarthquakeDefault locks the no-fabrication rule at the normalize
// level: a missing crisisNature is NEVER turned into a fabricated "earthquake" — it
// normalizes to an empty (non-nil) slice. The submit gate then REJECTS that empty
// value (crisis nature is a required core question — see TestSubmitValidation), so the
// server neither invents a hazard nor silently stores a report without one.
func TestNormalize_NoEarthquakeDefault(t *testing.T) {
	r, err := normalize(model.SubmitReportRequest{ID: "r-nature", Damage: "minimal", InfraTypes: []string{"residential"}, Lat: f64(36.2), Lng: f64(36.16)}, nil)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if len(r.CrisisNature) != 0 {
		t.Errorf("missing crisisNature must NOT be defaulted to a hazard, got %v", r.CrisisNature)
	}
	if r.CrisisNature == nil {
		t.Errorf("crisisNature must normalize to an empty slice (NOT NULL column), got nil")
	}
	// Required at the submit gate: an empty hazard is rejected, never fabricated or stored.
	if err := validate(r); err == nil || !strings.Contains(err.Error(), "crisis nature is required") {
		t.Errorf("empty crisisNature must be rejected as required, got %v", err)
	}
}
