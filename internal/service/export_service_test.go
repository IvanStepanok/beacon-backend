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

// fixtureResolved is a fully-resolved report exercising the C2 export contract,
// including the named infrastructure, plus code, free-text description and a
// DYNAMIC modular section (shelterCondition) beyond the stable three.
func fixtureResolved() model.Report {
	return model.Report{
		ID:               "r-1",
		Damage:           "partial",
		DamageTier:       "partial",
		InfraTypes:       []string{"residential"},
		InfraName:        strPtr("Cumhuriyet Primary School"),
		CrisisNature:     []string{"earthquake"},
		CapturedAt:       time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC),
		Lat:              f64(36.2),
		Lng:              f64(36.16),
		LocationResolved: true,
		PlusCode:         strPtr("8G7F6526+VC"),
		Description:      &model.ReportDescription{Original: "duvar çöktü", Translated: "wall collapsed"},
		Modular:          json.RawMessage(`{"electricity":"none","healthServices":"partial","pressingNeeds":["water","shelter","basic_services"],"pressingNeedsOther":"generator fuel","shelterCondition":"damaged"}`),
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
		`"infrastructure_name": "Cumhuriyet Primary School"`,
		`"hazard_type": "earthquake"`,
		`"timestamp": "2026-06-09T12:00:00Z"`,
		`"electricity": "none"`,
		`"health_services": "partial"`,
		`"pressing_needs": "water;shelter;basic_services"`,
		// Dynamic modular sections flatten automatically (camelCase → snake_case).
		`"pressing_needs_other": "generator fuel"`,
		`"shelter_condition": "damaged"`,
		`"description": "wall collapsed"`,
		`"plus_code": "8G7F6526+VC"`,
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

// TestToGeoJSON_EmptyHazardTolerated locks the no-fabrication rule downstream: a
// report with NO hazard exports hazard_type "" (and the stable modular columns
// stay present/empty) — never a fabricated "earthquake", never a panic.
func TestToGeoJSON_EmptyHazardTolerated(t *testing.T) {
	r := fixtureResolved()
	r.CrisisNature = []string{}
	r.Modular = nil

	body, err := ToGeoJSON([]model.Report{r})
	if err != nil {
		t.Fatalf("ToGeoJSON: %v", err)
	}
	s := string(body)
	if !strings.Contains(s, `"hazard_type": ""`) {
		t.Errorf("empty hazard must export as \"\"\n%s", s)
	}
	if strings.Contains(s, "earthquake") {
		t.Errorf("empty hazard must NOT be fabricated as earthquake\n%s", s)
	}
	for _, w := range []string{`"electricity": ""`, `"health_services": ""`, `"pressing_needs": ""`} {
		if !strings.Contains(s, w) {
			t.Errorf("stable modular column missing for nil modular: %q\n%s", w, s)
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
	for _, col := range []string{"latitude", "longitude", "timestamp", "damage_classification", "infrastructure_type", "infrastructure_name", "hazard_type", "electricity", "health_services", "pressing_needs", "description", "plus_code", "admin1_shapeid", "pressing_needs_other", "shelter_condition"} {
		if !strings.Contains(header, col) {
			t.Errorf("CSV header missing column %q\nheader: %s", col, header)
		}
	}
	// Dynamic modular columns are APPENDED after the fixed schema (sorted), so the
	// stable column positions never shift under existing consumers.
	if !strings.HasSuffix(header, "admin3_shapeid,pressing_needs_other,shelter_condition") {
		t.Errorf("dynamic modular columns must be appended after the fixed schema: %s", header)
	}
	// HXL row must NOT assert P-codes, and must tag the dynamic columns.
	if strings.Contains(lines[1], "#adm") && strings.Contains(lines[1], "+code") {
		t.Errorf("HXL row must not use #adm*+code (asserts P-code): %s", lines[1])
	}
	if !strings.Contains(lines[1], "#indicator+shelter_condition") {
		t.Errorf("HXL row missing dynamic column tag: %s", lines[1])
	}

	// Resolved row carries coords.
	if !strings.Contains(lines[2], "36.2") || !strings.Contains(lines[2], "36.16") {
		t.Errorf("resolved CSV row should carry lat/lng: %s", lines[2])
	}
	// damage_classification value is title-cased.
	if !strings.Contains(lines[2], "Partial") {
		t.Errorf("resolved CSV row should carry damage_classification Partial: %s", lines[2])
	}
	// New row fields: named infrastructure, description, plus code, dynamic extras.
	for _, w := range []string{"Cumhuriyet Primary School", "wall collapsed", "8G7F6526+VC", "generator fuel", "damaged"} {
		if !strings.Contains(lines[2], w) {
			t.Errorf("resolved CSV row missing %q: %s", w, lines[2])
		}
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
	for _, col := range []string{"admin2_shapeid", "admin3_shapeid", "infrastructure_name", "description", "plus_code", "electricity", "health_services", "pressing_needs", "shelter_condition"} {
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

	var infraName, desc, plusCode, shelter string
	if err := db.QueryRow(`SELECT infrastructure_name, description, plus_code, shelter_condition FROM reports WHERE id = 'r-1'`).
		Scan(&infraName, &desc, &plusCode, &shelter); err != nil {
		t.Fatalf("read new columns: %v", err)
	}
	if infraName != "Cumhuriyet Primary School" || desc != "wall collapsed" || plusCode != "8G7F6526+VC" || shelter != "damaged" {
		t.Errorf("new column values = %q/%q/%q/%q", infraName, desc, plusCode, shelter)
	}
}

// TestExport_ReservedModularKeys locks the reserved-name guard: a malicious modular
// blob claiming the GPKG feature table's own columns ({"geom":…,"fid":…}) must not
// break the GPKG CREATE TABLE (a one-report DoS) — the keys are exported under an
// "x_" prefix instead, consistently across GPKG and CSV (GeoJSON/KML share
// flattenModular, so the rename holds there too).
func TestExport_ReservedModularKeys(t *testing.T) {
	r := fixtureResolved()
	r.Modular = json.RawMessage(`{"geom":"x","fid":"y"}`)

	flat := flattenModular(r.Modular)
	if flat["x_geom"] != "x" || flat["x_fid"] != "y" {
		t.Errorf("reserved keys must be kept under x_: got x_geom=%q x_fid=%q", flat["x_geom"], flat["x_fid"])
	}
	for _, forbidden := range []string{"geom", "fid"} {
		if _, ok := flat[forbidden]; ok {
			t.Errorf("flattenModular must never emit the reserved column %q", forbidden)
		}
	}

	// GPKG: the export must succeed, geom must stay the single BLOB geometry
	// column, and the renamed x_ columns must carry the values.
	body, err := ToGPKG([]model.Report{r})
	if err != nil {
		t.Fatalf("ToGPKG with reserved modular keys: %v", err)
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
	if got := strings.Count(ddl, "geom"); got != 2 { // "geom BLOB" + "x_geom TEXT"
		t.Errorf("DDL must contain geom exactly twice (geom BLOB + x_geom TEXT), got %d:\n%s", got, ddl)
	}
	var xg, xf string
	if err := db.QueryRow(`SELECT x_geom, x_fid FROM reports WHERE id = 'r-1'`).Scan(&xg, &xf); err != nil {
		t.Fatalf("read x_ columns: %v", err)
	}
	if xg != "x" || xf != "y" {
		t.Errorf("x_ column values = %q/%q, want x/y", xg, xf)
	}

	// CSV: same renamed columns, appended after the fixed schema.
	lines := strings.Split(string(ToCSV([]model.Report{r})), "\n")
	if !strings.HasSuffix(lines[0], "admin3_shapeid,x_fid,x_geom") {
		t.Errorf("CSV header must append the renamed x_ columns: %s", lines[0])
	}
}

// TestFlattenModular locks the dynamic flattening rules: stable sections always
// present, camelCase → snake_case, arrays ";"-joined, unsafe keys dropped.
func TestFlattenModular(t *testing.T) {
	flat := flattenModular(json.RawMessage(`{"healthServices":"partial","pressingNeeds":["water","basic_services"],"pressingNeedsOther":"fuel","roadAccess":"blocked","bad key!":"x"}`))
	want := map[string]string{
		"electricity":          "", // stable section, unanswered → present + empty
		"health_services":      "partial",
		"pressing_needs":       "water;basic_services",
		"pressing_needs_other": "fuel",
		"road_access":          "blocked",
	}
	for k, v := range want {
		if flat[k] != v {
			t.Errorf("flattenModular[%q] = %q, want %q", k, flat[k], v)
		}
	}
	if _, ok := flat["bad key!"]; ok {
		t.Errorf("unsafe modular key must be dropped from export columns")
	}
	// nil / null blobs still yield the stable three.
	for _, raw := range []json.RawMessage{nil, json.RawMessage(`null`)} {
		flat := flattenModular(raw)
		if len(flat) != 3 {
			t.Errorf("flattenModular(%s) = %v, want only the 3 stable sections", string(raw), flat)
		}
	}
}

func TestCamelToSnake(t *testing.T) {
	cases := map[string]string{"electricity": "electricity", "healthServices": "health_services", "pressingNeedsOther": "pressing_needs_other"}
	for in, want := range cases {
		if got := camelToSnake(in); got != want {
			t.Errorf("camelToSnake(%q) = %q, want %q", in, got, want)
		}
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
