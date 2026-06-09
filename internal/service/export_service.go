package service

import (
	"bytes"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver (no CGO) for GeoPackage export

	"github.com/stepanok/beacon-server/internal/model"
)

// Export produces interoperable formats so Beacon data drops straight into the
// humanitarian stack: GeoJSON + HXL-tagged CSV (with admin_shapeid columns) +
// GeoPackage (OGC, single offline file) + KML + a PDNA-ready damage-count aggregate
// (sector × admin pivot of report COUNTS by damage grade). NOTE: this is a
// damage-count input for a PDNA, NOT a loss/cost estimation — it carries no monetary
// or replacement-value figures.
//
// The exported admin columns are admin_shapeid (geoBoundaries shapeID / illustrative
// seed codes) — NOT official OCHA P-codes until a source=cod layer exists. They are
// still a valid join key against the admin_areas table, but must not be labelled
// "P-code", which would assert a provenance Beacon does not yet have.
//
// Per the C2 export contract, GeoJSON Feature geometry is a Point [lng, lat] in
// decimal degrees (or null when the report's location is unresolved), and properties
// carry the required gate fields: damage_classification ∈ {Minimal,Partial,Complete},
// infrastructure_type, timestamp (ISO-8601), hazard_type, and the secondary-impact
// fields electricity / health_services / pressing_needs from the modular blob.

type exportProps struct {
	ID                   string `json:"id"`
	DamageClassification string `json:"damage_classification"` // 3-tier gate value: Minimal|Partial|Complete
	Damage               string `json:"damage"`                // raw grade kept as a useful extra
	PossiblyDamaged      bool   `json:"possiblyDamaged"`
	InfrastructureType   string `json:"infrastructure_type"`
	HazardType           string `json:"hazard_type"`
	Timestamp            string `json:"timestamp"` // ISO-8601 of capturedAt
	Electricity          string `json:"electricity"`
	HealthServices       string `json:"health_services"`
	PressingNeeds        string `json:"pressing_needs"`
	Debris               string `json:"debris"`
	BuildingID           string `json:"buildingId"`
	Verification         string `json:"verification"`
	Synced               bool   `json:"synced"`
	Place                string `json:"place"`
	AccuracyMeters       string `json:"accuracy_m,omitempty"`
	Admin1ShapeID        string `json:"admin1_shapeid,omitempty"`
	Admin2ShapeID        string `json:"admin2_shapeid,omitempty"`
	Admin3ShapeID        string `json:"admin3_shapeid,omitempty"`
}

type exportGeometry struct {
	Type        string     `json:"type"`
	Coordinates [2]float64 `json:"coordinates"`
}

type exportFeature struct {
	Type string `json:"type"`
	// Geometry is a POINTER so an unresolved report serializes "geometry": null
	// (a Point [lng, lat] otherwise). Never emit [0,0] (Null Island).
	Geometry   *exportGeometry `json:"geometry"`
	Properties exportProps     `json:"properties"`
}

type exportFC struct {
	Type     string          `json:"type"`
	Features []exportFeature `json:"features"`
}

// ErrUnsupportedFormat → 501 at the handler.
var ErrUnsupportedFormat = fmt.Errorf("unsupported export format")

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// numPtr formats a *float64 as a compact decimal string, or "" when nil.
func numPtr(f *float64) string {
	if f == nil {
		return ""
	}
	return strconv.FormatFloat(*f, 'g', -1, 64)
}

// modularImpacts mirrors the [C1] modular wire/stored shape (camelCase keys). The
// EXPORT column/property names are snake_case (electricity / health_services /
// pressing_needs) — see flatten in ToGeoJSON / ToCSV.
type modularImpacts struct {
	Electricity    *string  `json:"electricity"`
	HealthServices *string  `json:"healthServices"`
	PressingNeeds  []string `json:"pressingNeeds"`
}

func parseModular(raw json.RawMessage) modularImpacts {
	var m modularImpacts
	if len(raw) > 0 && string(raw) != "null" {
		_ = json.Unmarshal(raw, &m)
	}
	return m
}

// titleTier title-cases the already-computed 3-tier damage_tier (minimal|partial|
// complete) to the C2 gate value {Minimal,Partial,Complete}. An empty/unknown tier
// defaults to "Minimal" (safe — damage_tier is a generated column, always populated).
func titleTier(t string) string {
	switch t {
	case "minimal":
		return "Minimal"
	case "partial":
		return "Partial"
	case "complete":
		return "Complete"
	default:
		return "Minimal"
	}
}

// reportResolved reports whether a report carries a usable point. After C4 made
// model.Report.Lat/Lng *float64, an unresolved (landmark-only) report has nil coords
// (and LocationResolved=false). Geometry/coords are emitted only for resolved reports.
func reportResolved(r model.Report) bool {
	return r.LocationResolved && r.Lat != nil && r.Lng != nil
}

func ToGeoJSON(reports []model.Report) ([]byte, error) {
	fc := exportFC{Type: "FeatureCollection", Features: make([]exportFeature, 0, len(reports))}
	for _, r := range reports {
		// Geometry is null for a location-unresolved report (never [0,0]).
		var geom *exportGeometry
		if reportResolved(r) {
			geom = &exportGeometry{Type: "Point", Coordinates: [2]float64{*r.Lng, *r.Lat}}
		}
		mi := parseModular(r.Modular)
		fc.Features = append(fc.Features, exportFeature{
			Type:     "Feature",
			Geometry: geom,
			Properties: exportProps{
				ID:                   r.ID,
				DamageClassification: titleTier(r.DamageTier),
				Damage:               r.Damage,
				PossiblyDamaged:      r.PossiblyDamaged,
				InfrastructureType:   strings.Join(r.InfraTypes, ";"),
				HazardType:           strings.Join(r.CrisisNature, ";"),
				Timestamp:            r.CapturedAt.UTC().Format(time.RFC3339),
				Electricity:          deref(mi.Electricity),
				HealthServices:       deref(mi.HealthServices),
				PressingNeeds:        strings.Join(mi.PressingNeeds, ";"),
				Debris:               r.Debris,
				BuildingID:           deref(r.BuildingID),
				Verification:         r.Verification,
				Synced:               r.Synced,
				Place:                r.Place,
				AccuracyMeters:       numPtr(r.GPSAccuracyMeters),
				Admin1ShapeID:        deref(r.Adm1Pcode),
				Admin2ShapeID:        deref(r.Adm2Pcode),
				Admin3ShapeID:        deref(r.Adm3Pcode),
			},
		})
	}
	// Match JS JSON.stringify: do NOT HTML-escape &, <, > so server and browser
	// exports stay byte-interchangeable. Encoder adds a trailing newline — trim it.
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(fc); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

var csvNeedsQuote = regexp.MustCompile(`[",\n]`)

func csvCell(s string) string {
	if csvNeedsQuote.MatchString(s) {
		return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
	}
	return s
}

// csvColumns is the C2 export schema; hxlRow tags each column with its HXL hashtag so
// OCHA tooling can machine-merge the file. The admin columns are admin_shapeid
// columns (geoBoundaries shapeID / illustrative seed codes — NOT official OCHA
// P-codes until a source=cod layer exists); their HXL tags use +id (not +code) so the
// file never falsely asserts a P-code provenance.
var (
	csvColumns = []string{"id", "latitude", "longitude", "timestamp", "damage_classification", "damage", "infrastructure_type", "hazard_type", "electricity", "health_services", "pressing_needs", "possiblyDamaged", "debris", "buildingId", "verification", "place", "accuracy_m", "admin1_shapeid", "admin2_shapeid", "admin3_shapeid"}
	hxlRow     = []string{"#meta+id", "#geo+lat", "#geo+lon", "#date", "#severity+grade", "#severity+raw", "#sector", "#cause", "#indicator+electricity", "#indicator+health", "#indicator+needs", "#indicator+possibly", "#indicator+debris", "#loc+building+id", "#status+verification", "#loc+name", "#indicator+accuracy", "#loc+adm1+id", "#loc+adm2+id", "#loc+adm3+id"}
)

func ToCSV(reports []model.Report) []byte {
	var b bytes.Buffer
	b.WriteString(strings.Join(csvColumns, ","))
	b.WriteString("\n")
	b.WriteString(strings.Join(hxlRow, ",")) // HXL hashtag row
	for _, r := range reports {
		// Blank lat/lng for a location-unresolved report (never 0,0).
		latStr, lngStr := "", ""
		if reportResolved(r) {
			latStr, lngStr = numPtr(r.Lat), numPtr(r.Lng)
		}
		mi := parseModular(r.Modular)
		row := []string{
			r.ID, latStr, lngStr, r.CapturedAt.UTC().Format(time.RFC3339),
			titleTier(r.DamageTier), r.Damage,
			strings.Join(r.InfraTypes, ";"), strings.Join(r.CrisisNature, ";"),
			deref(mi.Electricity), deref(mi.HealthServices), strings.Join(mi.PressingNeeds, ";"),
			strconv.FormatBool(r.PossiblyDamaged), r.Debris, deref(r.BuildingID),
			r.Verification, r.Place, numPtr(r.GPSAccuracyMeters),
			deref(r.Adm1Pcode), deref(r.Adm2Pcode), deref(r.Adm3Pcode),
		}
		for i := range row {
			row[i] = csvCell(row[i])
		}
		b.WriteString("\n")
		b.WriteString(strings.Join(row, ","))
	}
	return b.Bytes()
}

// ToPdnaCSV renders PDNA-ready damage-count aggregates: a sector × admin pivot of
// report COUNTS per damage grade, HXL-tagged. This is a damage-count input for a
// PDNA — NOT a loss/cost estimation (no monetary or replacement-value figures). A
// leading comment line states this so the file is not mistaken for a costed table.
func ToPdnaCSV(rows []model.PdnaRow) []byte {
	// The CANONICAL breakdown is the 3-tier rollup (minimal/partial/complete) which
	// sums to total per row regardless of capture scale. The 5-level EMS-98 columns
	// are kept as trailing detail (populated only for ems98-scale reports).
	// admin2_shapeid (NOT adm2Pcode): the ADM2 codes here are illustrative seed /
	// geoBoundaries shapeIDs, not official OCHA P-codes — so the column + HXL must not
	// assert a P-code provenance (#loc+adm2+id, not #adm2+code). See package doc.
	cols := []string{"admin2_shapeid", "adm2Name", "sector", "minimal", "partial", "complete", "none", "slight", "moderate", "severe", "destroyed", "total"}
	hxl := []string{"#loc+adm2+id", "#adm2+name", "#sector", "#affected+minimal", "#affected+partial", "#affected+complete", "#affected+none", "#affected+slight", "#affected+moderate", "#affected+severe", "#affected+destroyed", "#affected+total"}
	var b bytes.Buffer
	// Honest label: damage-count aggregates, not a loss/cost estimate. '#'-prefixed
	// so HXL/CSV tooling treats it as a comment line, not data.
	b.WriteString("# PDNA-ready damage-count aggregates (report counts by damage grade) — NOT a loss/cost estimation\n")
	b.WriteString("# minimal/partial/complete = canonical 3-tier rollup (sums to total); none..destroyed = EMS-98 detail (ems98-scale only)\n")
	b.WriteString(strings.Join(cols, ","))
	b.WriteString("\n")
	b.WriteString(strings.Join(hxl, ","))
	for _, r := range rows {
		cells := []string{
			csvCell(r.AdmPcode), csvCell(r.AdmName), csvCell(r.Sector),
			strconv.Itoa(r.Minimal), strconv.Itoa(r.Partial), strconv.Itoa(r.Complete),
			strconv.Itoa(r.None), strconv.Itoa(r.Slight), strconv.Itoa(r.Moderate),
			strconv.Itoa(r.Severe), strconv.Itoa(r.Destroyed), strconv.Itoa(r.Total),
		}
		b.WriteString("\n")
		b.WriteString(strings.Join(cells, ","))
	}
	return b.Bytes()
}

// gpbPoint encodes a lon/lat point as a GeoPackageBinary blob (GPB header + WKB,
// little-endian, no envelope, SRS 4326).
func gpbPoint(lng, lat float64) []byte {
	buf := new(bytes.Buffer)
	buf.Write([]byte{'G', 'P', 0x00, 0x01})                 // magic, version 0, flags: LE + no envelope
	_ = binary.Write(buf, binary.LittleEndian, int32(4326)) // srs_id
	buf.WriteByte(0x01)                                     // WKB byte order: little-endian
	_ = binary.Write(buf, binary.LittleEndian, uint32(1))   // WKB type: Point
	_ = binary.Write(buf, binary.LittleEndian, lng)
	_ = binary.Write(buf, binary.LittleEndian, lat)
	return buf.Bytes()
}

// ToGPKG builds an OGC GeoPackage (single SQLite file) of the reports — a real,
// interoperable, offline-friendly format, written with the pure-Go driver so the
// static binary stays CGO-free.
func ToGPKG(reports []model.Report) ([]byte, error) {
	f, err := os.CreateTemp("", "beacon-*.gpkg")
	if err != nil {
		return nil, err
	}
	path := f.Name()
	f.Close()
	defer os.Remove(path)

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	minX, minY, maxX, maxY := 180.0, 90.0, -180.0, -90.0
	resolvedCount := 0
	for _, r := range reports {
		if !reportResolved(r) {
			continue // skip unresolved (nil-coord) reports from the bbox
		}
		resolvedCount++
		minX, maxX = min(minX, *r.Lng), max(maxX, *r.Lng)
		minY, maxY = min(minY, *r.Lat), max(maxY, *r.Lat)
	}
	if resolvedCount == 0 {
		minX, minY, maxX, maxY = 0, 0, 0, 0
	}

	stmts := []string{
		`PRAGMA application_id = 1196444487`, // 'GPKG'
		`PRAGMA user_version = 10300`,        // GeoPackage 1.3
		`CREATE TABLE gpkg_spatial_ref_sys (srs_name TEXT NOT NULL, srs_id INTEGER PRIMARY KEY, organization TEXT NOT NULL, organization_coordsys_id INTEGER NOT NULL, definition TEXT NOT NULL, description TEXT)`,
		`CREATE TABLE gpkg_contents (table_name TEXT NOT NULL PRIMARY KEY, data_type TEXT NOT NULL, identifier TEXT UNIQUE, description TEXT DEFAULT '', last_change DATETIME NOT NULL, min_x DOUBLE, min_y DOUBLE, max_x DOUBLE, max_y DOUBLE, srs_id INTEGER)`,
		`CREATE TABLE gpkg_geometry_columns (table_name TEXT NOT NULL, column_name TEXT NOT NULL, geometry_type_name TEXT NOT NULL, srs_id INTEGER NOT NULL, z TINYINT NOT NULL, m TINYINT NOT NULL, PRIMARY KEY (table_name, column_name))`,
		// admin*_shapeid (NOT adm*_pcode): same provenance caveat as GeoJSON/CSV — see package doc.
		`CREATE TABLE reports (fid INTEGER PRIMARY KEY AUTOINCREMENT, geom BLOB, id TEXT, damage TEXT, possibly_damaged INTEGER, verification TEXT, infrastructure TEXT, crisis TEXT, debris TEXT, building_id TEXT, place TEXT, admin2_shapeid TEXT, admin3_shapeid TEXT, captured_at TEXT)`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			return nil, fmt.Errorf("gpkg ddl: %w", err)
		}
	}
	srs := [][]any{
		{"WGS 84 geographic", 4326, "EPSG", 4326, `GEOGCS["WGS 84",DATUM["WGS_1984",SPHEROID["WGS 84",6378137,298.257223563]],PRIMEM["Greenwich",0],UNIT["degree",0.0174532925199433]]`, "longitude/latitude WGS84"},
		{"Undefined cartesian SRS", -1, "NONE", -1, "undefined", "undefined cartesian coordinate reference system"},
		{"Undefined geographic SRS", 0, "NONE", 0, "undefined", "undefined geographic coordinate reference system"},
	}
	for _, r := range srs {
		if _, err := db.Exec(`INSERT INTO gpkg_spatial_ref_sys VALUES (?,?,?,?,?,?)`, r...); err != nil {
			return nil, err
		}
	}
	if _, err := db.Exec(`INSERT INTO gpkg_contents (table_name, data_type, identifier, description, last_change, min_x, min_y, max_x, max_y, srs_id) VALUES ('reports','features','reports','Beacon community damage reports',?,?,?,?,?,4326)`,
		now, minX, minY, maxX, maxY); err != nil {
		return nil, err
	}
	if _, err := db.Exec(`INSERT INTO gpkg_geometry_columns VALUES ('reports','geom','POINT',4326,0,0)`); err != nil {
		return nil, err
	}

	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	ins, err := tx.Prepare(`INSERT INTO reports (geom, id, damage, possibly_damaged, verification, infrastructure, crisis, debris, building_id, place, admin2_shapeid, admin3_shapeid, captured_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		tx.Rollback()
		return nil, err
	}
	for _, r := range reports {
		pd := 0
		if r.PossiblyDamaged {
			pd = 1
		}
		// Store NULL geom for a location-unresolved report (never gpbPoint(0,0)).
		var geomBlob any
		if reportResolved(r) {
			geomBlob = gpbPoint(*r.Lng, *r.Lat)
		}
		if _, err := ins.Exec(geomBlob, r.ID, r.Damage, pd, r.Verification,
			strings.Join(r.InfraTypes, ";"), strings.Join(r.CrisisNature, ";"), r.Debris,
			deref(r.BuildingID), r.Place, deref(r.Adm2Pcode), deref(r.Adm3Pcode),
			r.CapturedAt.UTC().Format(time.RFC3339)); err != nil {
			ins.Close()
			tx.Rollback()
			return nil, err
		}
	}
	ins.Close()
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	if err := db.Close(); err != nil {
		return nil, err
	}
	return os.ReadFile(path)
}

// xmlEscape escapes text for inclusion in KML element bodies.
func xmlEscape(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// ToKML renders the reports as a minimal KML document — one <Placemark> per RESOLVED
// report (location-unresolved reports are skipped, never emitted at 0,0). Each
// placemark carries a short description with the C2 gate fields (damage
// classification, infrastructure type, hazard type) and the secondary impacts. This
// is the "KML is a nice add if cheap" deliverable; it opens directly in Google Earth.
func ToKML(reports []model.Report) []byte {
	var b bytes.Buffer
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<kml xmlns="http://www.opengis.net/kml/2.2"><Document>` + "\n")
	b.WriteString(`<name>Beacon community damage reports</name>` + "\n")
	for _, r := range reports {
		if !reportResolved(r) {
			continue
		}
		mi := parseModular(r.Modular)
		desc := fmt.Sprintf(
			"damage_classification: %s\ninfrastructure_type: %s\nhazard_type: %s\nelectricity: %s\nhealth_services: %s\npressing_needs: %s\nverification: %s\ntimestamp: %s",
			titleTier(r.DamageTier), strings.Join(r.InfraTypes, ";"), strings.Join(r.CrisisNature, ";"),
			deref(mi.Electricity), deref(mi.HealthServices), strings.Join(mi.PressingNeeds, ";"),
			r.Verification, r.CapturedAt.UTC().Format(time.RFC3339))
		b.WriteString("<Placemark>")
		b.WriteString("<name>" + xmlEscape(r.ID) + "</name>")
		b.WriteString("<description>" + xmlEscape(desc) + "</description>")
		b.WriteString(fmt.Sprintf("<Point><coordinates>%s,%s</coordinates></Point>",
			strconv.FormatFloat(*r.Lng, 'g', -1, 64), strconv.FormatFloat(*r.Lat, 'g', -1, 64)))
		b.WriteString("</Placemark>\n")
	}
	b.WriteString(`</Document></kml>`)
	return b.Bytes()
}
