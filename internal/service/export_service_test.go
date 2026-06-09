package service

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stepanok/beacon-server/internal/model"
)

func f64(v float64) *float64 { return &v }

// fixtureResolved is a fully-resolved report exercising the C2 export contract.
func fixtureResolved() model.Report {
	return model.Report{
		ID:               "r-1",
		Damage:           "severe",
		DamageTier:       "partial",
		InfraTypes:       []string{"residential"},
		CrisisNature:     []string{"earthquake"},
		CapturedAt:       time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC),
		Lat:              f64(36.2),
		Lng:              f64(36.16),
		LocationResolved: true,
		Modular:          json.RawMessage(`{"electricity":"none","healthServices":"partial","pressingNeeds":["water","shelter"]}`),
	}
}

func TestToGeoJSON_C2Properties(t *testing.T) {
	body, err := ToGeoJSON([]model.Report{fixtureResolved()})
	if err != nil {
		t.Fatalf("ToGeoJSON: %v", err)
	}
	s := string(body)

	wantContains := []string{
		`"damage_classification": "Partial"`,
		`"infrastructure_type": "residential"`,
		`"hazard_type": "earthquake"`,
		`"timestamp": "2026-06-09T12:00:00Z"`,
		`"electricity": "none"`,
		`"health_services": "partial"`,
		`"pressing_needs": "water;shelter"`,
	}
	for _, w := range wantContains {
		if !strings.Contains(s, w) {
			t.Errorf("GeoJSON missing %q\n--- got ---\n%s", w, s)
		}
	}

	// Coordinates must be [lng, lat].
	if !strings.Contains(s, "36.16") || !strings.Contains(s, "36.2") {
		t.Errorf("GeoJSON missing lng/lat coordinates\n%s", s)
	}

	// The mislabelled P-code property names must be ABSENT.
	for _, forbidden := range []string{"Pcode", "adm1Pcode", "#adm"} {
		if strings.Contains(s, forbidden) {
			t.Errorf("GeoJSON must not contain %q\n%s", forbidden, s)
		}
	}
}

func TestToGeoJSON_UnresolvedGeometryNull(t *testing.T) {
	r := fixtureResolved()
	r.Lat, r.Lng, r.LocationResolved = nil, nil, false
	r.Landmark = strPtr("Old mosque, Saray St")

	body, err := ToGeoJSON([]model.Report{r})
	if err != nil {
		t.Fatalf("ToGeoJSON: %v", err)
	}
	s := string(body)
	if !strings.Contains(s, `"geometry": null`) {
		t.Errorf("unresolved report must serialize geometry:null\n%s", s)
	}
	if strings.Contains(s, "[0,0]") || strings.Contains(s, "[\n        0,\n        0") {
		t.Errorf("unresolved report must NOT emit [0,0]\n%s", s)
	}
}

func TestToCSV_C2HeadersAndUnresolved(t *testing.T) {
	resolved := fixtureResolved()
	unresolved := fixtureResolved()
	unresolved.ID = "r-2"
	unresolved.Lat, unresolved.Lng, unresolved.LocationResolved = nil, nil, false
	unresolved.Landmark = strPtr("Old mosque")

	out := string(ToCSV([]model.Report{resolved, unresolved}))
	lines := strings.Split(out, "\n")
	if len(lines) < 4 {
		t.Fatalf("expected header + hxl + 2 rows, got %d lines:\n%s", len(lines), out)
	}
	header := lines[0]
	for _, col := range []string{"latitude", "longitude", "timestamp", "damage_classification", "infrastructure_type", "hazard_type", "electricity", "health_services", "pressing_needs", "admin1_shapeid"} {
		if !strings.Contains(header, col) {
			t.Errorf("CSV header missing column %q\nheader: %s", col, header)
		}
	}
	// HXL row must NOT assert P-codes.
	if strings.Contains(lines[1], "#adm") && strings.Contains(lines[1], "+code") {
		t.Errorf("HXL row must not use #adm*+code (asserts P-code): %s", lines[1])
	}

	// Resolved row carries coords.
	if !strings.Contains(lines[2], "36.2") || !strings.Contains(lines[2], "36.16") {
		t.Errorf("resolved CSV row should carry lat/lng: %s", lines[2])
	}
	// damage_classification value is title-cased.
	if !strings.Contains(lines[2], "Partial") {
		t.Errorf("resolved CSV row should carry damage_classification Partial: %s", lines[2])
	}
	// Unresolved row: latitude/longitude cells blank (",," after the id).
	unresolvedRow := lines[3]
	if !strings.HasPrefix(unresolvedRow, "r-2,,,") {
		t.Errorf("unresolved CSV row should have blank lat/lng cells: %q", unresolvedRow)
	}
}

func TestToKML_SkipsUnresolved(t *testing.T) {
	resolved := fixtureResolved()
	unresolved := fixtureResolved()
	unresolved.ID = "r-2"
	unresolved.Lat, unresolved.Lng, unresolved.LocationResolved = nil, nil, false
	unresolved.Landmark = strPtr("Old mosque")

	out := string(ToKML([]model.Report{resolved, unresolved}))
	if !strings.Contains(out, "<name>r-1</name>") {
		t.Errorf("KML should contain resolved placemark r-1\n%s", out)
	}
	if strings.Contains(out, "<name>r-2</name>") {
		t.Errorf("KML should SKIP unresolved placemark r-2\n%s", out)
	}
	if !strings.Contains(out, "<coordinates>36.16,36.2</coordinates>") {
		t.Errorf("KML coordinates should be lng,lat\n%s", out)
	}
}

// TestToGPKG_AdminShapeidColumns locks the GPKG relabel: the reports table must use
// admin2_shapeid/admin3_shapeid (geoBoundaries shapeIDs / seed codes), never a column
// name containing "pcode" — which would assert an OCHA provenance Beacon doesn't have.
// Reopening the produced file also proves the INSERT was renamed in lockstep (a stale
// adm*_pcode INSERT would make ToGPKG itself fail with "no such column").
func TestToGPKG_AdminShapeidColumns(t *testing.T) {
	r := fixtureResolved()
	r.Adm2Pcode = strPtr("TR6303")
	r.Adm3Pcode = strPtr("TR630305")

	body, err := ToGPKG([]model.Report{r})
	if err != nil {
		t.Fatalf("ToGPKG: %v", err)
	}
	path := filepath.Join(t.TempDir(), "out.gpkg")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatalf("write gpkg: %v", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open gpkg: %v", err)
	}
	defer db.Close()

	var ddl string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name='reports'`).Scan(&ddl); err != nil {
		t.Fatalf("read reports DDL: %v", err)
	}
	if strings.Contains(strings.ToLower(ddl), "pcode") {
		t.Errorf("GPKG reports DDL must not contain %q:\n%s", "pcode", ddl)
	}
	for _, col := range []string{"admin2_shapeid", "admin3_shapeid"} {
		if !strings.Contains(ddl, col) {
			t.Errorf("GPKG reports DDL missing column %q:\n%s", col, ddl)
		}
	}

	var adm2, adm3 string
	if err := db.QueryRow(`SELECT admin2_shapeid, admin3_shapeid FROM reports WHERE id = 'r-1'`).Scan(&adm2, &adm3); err != nil {
		t.Fatalf("read renamed admin columns: %v", err)
	}
	if adm2 != "TR6303" || adm3 != "TR630305" {
		t.Errorf("admin shapeid values = %q/%q, want TR6303/TR630305", adm2, adm3)
	}
}

func TestTitleTier(t *testing.T) {
	cases := map[string]string{"minimal": "Minimal", "partial": "Partial", "complete": "Complete", "": "Minimal", "weird": "Minimal"}
	for in, want := range cases {
		if got := titleTier(in); got != want {
			t.Errorf("titleTier(%q) = %q, want %q", in, got, want)
		}
	}
}

func strPtr(s string) *string { return &s }
