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
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	_ "modernc.org/sqlite" // pure-Go SQLite driver (no CGO) for GeoPackage export

	"github.com/stepanok/beacon-server/internal/model"
)

// Export produces interoperable formats so Beacon data drops straight into the
// humanitarian stack: GeoJSON + HXL-tagged CSV (with admin P-code columns) +
// GeoPackage (OGC, single offline file) + KML.
//
// The exported admin columns are admin{1,2,3}_pcode — the OCHA COD-AB P-code the point
// reverse-geocoded to (source='cod'; ResolveAdmin ranks COD highest), so the data is
// natively joinable against the official COD-AB / HDX humanitarian datasets. HXL tags
// use +code accordingly. A `GB:`-prefixed value is the honest exception: a geoBoundaries
// shapeID placeholder for a country not yet published as a COD (no official P-code
// available) — filter those out for a strict P-code join.
//
// Per the C2 export contract, GeoJSON Feature geometry is a Point [lng, lat] in
// decimal degrees (or null when the report's location is unresolved), and properties
// carry the required gate fields: damage_classification ∈ {Minimal,Partial,Complete},
// infrastructure_type, timestamp (ISO-8601), hazard_type, and the secondary-impact
// sections flattened from the modular blob — the three known sections under their
// stable names (electricity / health_services / pressing_needs, always present) plus
// any later-added section DYNAMICALLY (camelCase key → snake_case), so new modular
// questions appear in exports without a code change. Rows also carry
// infrastructure_name, the free-text description (analyst language) and plus_code.
// These exports are analyst-only: the low-trust external_viewer tier is denied the
// whole endpoint (403, see handler.ExportReports), so description/precision never
// reach it.

type exportGeometry struct {
	Type        string     `json:"type"`
	Coordinates [2]float64 `json:"coordinates"`
}

type exportFeature struct {
	Type string `json:"type"`
	// Geometry is a POINTER so an unresolved report serializes "geometry": null
	// (a Point [lng, lat] otherwise). Never emit [0,0] (Null Island).
	// Properties is a map (keys marshal sorted) so dynamically-flattened modular
	// sections ride along with the fixed gate fields.
	Geometry   *exportGeometry `json:"geometry"`
	Properties map[string]any  `json:"properties"`
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

// stableModularColumns are the three known [C1] sections, ALWAYS present in every
// export row under today's stable snake_case names — even when unanswered (empty).
var stableModularColumns = []string{"electricity", "health_services", "pressing_needs"}

// safeColumnRe gates DYNAMIC modular keys: only snake_case identifiers become
// export columns. This keeps CSV headers clean and — critically — makes the
// client-controlled modular keys safe to splice into the GPKG DDL.
var safeColumnRe = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// reservedExportColumns are container-owned column names a CLIENT-CONTROLLED
// modular key must never claim: "fid" and "geom" are the GPKG feature table's own
// primary-key and geometry columns, so a modular section sanitizing to either would
// duplicate them in the CREATE TABLE and break the WHOLE GPKG export (a one-report
// DoS on the endpoint). Such keys keep their data under an "x_" prefix instead —
// applied inside flattenModular, so every format (CSV/GeoJSON/KML/GPKG) renames
// them consistently. (Keys colliding with the FIXED export columns are already
// skipped per-format by extraModularColumns; the fixed value always wins.)
var reservedExportColumns = map[string]bool{"fid": true, "geom": true}

// flattenModular projects the modular blob into snake_case export fields. The three
// known sections keep their stable names and are ALWAYS present (empty when
// unanswered); any other section a crisis adds later is flattened automatically
// (camelCase key → snake_case, arrays ";"-joined), so new modular questions appear
// in exports without a code change. Keys that don't sanitize to a safe column name
// are dropped; reserved physical column names (fid/geom) are kept under "x_".
func flattenModular(raw json.RawMessage) map[string]string {
	out := map[string]string{}
	for _, c := range stableModularColumns {
		out[c] = ""
	}
	if len(raw) == 0 || string(raw) == "null" {
		return out
	}
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return out
	}
	for k, v := range m {
		col := camelToSnake(k)
		if !safeColumnRe.MatchString(col) {
			continue
		}
		if reservedExportColumns[col] {
			col = "x_" + col
		}
		out[col] = flatValue(v)
	}
	return out
}

// flatValue renders one modular answer as a flat cell: strings as-is, arrays
// ";"-joined, scalars printed, nested objects as compact JSON, null as "".
func flatValue(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case []any:
		parts := make([]string, 0, len(t))
		for _, e := range t {
			parts = append(parts, flatValue(e))
		}
		return strings.Join(parts, ";")
	case float64:
		return strconv.FormatFloat(t, 'g', -1, 64)
	case bool:
		return strconv.FormatBool(t)
	default:
		b, _ := json.Marshal(t)
		return string(b)
	}
}

// camelToSnake converts a camelCase modular key to its snake_case export name
// (healthServices → health_services, pressingNeedsOther → pressing_needs_other).
func camelToSnake(s string) string {
	var b strings.Builder
	for i, r := range s {
		if unicode.IsUpper(r) {
			if i > 0 {
				b.WriteByte('_')
			}
			b.WriteRune(unicode.ToLower(r))
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// extraModularColumns returns the sorted DYNAMIC modular columns present across the
// rows — every flattened key beyond the three stable sections — skipping any key
// that would collide with a fixed export column.
func extraModularColumns(reports []model.Report, fixed []string) []string {
	taken := map[string]bool{}
	for _, c := range fixed {
		taken[c] = true
	}
	for _, c := range stableModularColumns {
		taken[c] = true
	}
	seen := map[string]bool{}
	for _, r := range reports {
		for k := range flattenModular(r.Modular) {
			if !taken[k] && !seen[k] {
				seen[k] = true
			}
		}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// exportDescription is the free-text note in the analysts' common language: the
// stored translation when one exists, else the original. "" when the report has
// none. (Analyst-only — external_viewer never reaches the export path.)
func exportDescription(r model.Report) string {
	if r.Description == nil {
		return ""
	}
	if r.Description.Translated != "" {
		return r.Description.Translated
	}
	return r.Description.Original
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
		// Flattened modular sections first; the fixed gate fields win on any
		// same-named key.
		props := map[string]any{}
		for k, v := range flattenModular(r.Modular) {
			props[k] = v
		}
		props["id"] = r.ID
		props["damage_classification"] = titleTier(r.DamageTier)
		props["damage"] = r.Damage // raw grade kept as a useful extra
		props["possiblyDamaged"] = r.PossiblyDamaged
		props["infrastructure_type"] = strings.Join(r.InfraTypes, ";")
		props["infrastructure_name"] = deref(r.InfraName)
		props["infrastructure_other_detail"] = deref(r.InfraOtherDetail)
		props["hazard_type"] = strings.Join(r.CrisisNature, ";")
		props["timestamp"] = r.CapturedAt.UTC().Format(time.RFC3339)
		props["debris"] = r.Debris
		props["buildingId"] = deref(r.BuildingID)
		props["verification"] = r.Verification
		props["synced"] = r.Synced
		props["place"] = r.Place
		props["description"] = exportDescription(r)
		props["plus_code"] = deref(r.PlusCode)
		if v := numPtr(r.GPSAccuracyMeters); v != "" {
			props["accuracy_m"] = v
		}
		if v := deref(r.Adm1Pcode); v != "" {
			props["admin1_pcode"] = v
		}
		if v := deref(r.Adm2Pcode); v != "" {
			props["admin2_pcode"] = v
		}
		if v := deref(r.Adm3Pcode); v != "" {
			props["admin3_pcode"] = v
		}
		fc.Features = append(fc.Features, exportFeature{
			Type:       "Feature",
			Geometry:   geom,
			Properties: props,
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
// OCHA tooling can machine-merge the file. The admin columns are admin{1,2,3}_pcode — the
// official OCHA COD-AB P-code the point reverse-geocoded to (source='cod'); their HXL tags
// use +code so the file joins natively against COD-AB. A `GB:`-prefixed value is a
// geoBoundaries shapeID placeholder for a country with no published COD (honest fallback).
// Any DYNAMIC modular sections beyond the three stable ones are appended after these fixed
// columns (#indicator+<name>).
var (
	csvColumns = []string{"id", "latitude", "longitude", "timestamp", "damage_classification", "damage", "infrastructure_type", "infrastructure_name", "infrastructure_other_detail", "hazard_type", "electricity", "health_services", "pressing_needs", "possiblyDamaged", "debris", "buildingId", "verification", "place", "description", "plus_code", "accuracy_m", "admin1_pcode", "admin2_pcode", "admin3_pcode"}
	hxlRow     = []string{"#meta+id", "#geo+lat", "#geo+lon", "#date", "#severity+grade", "#severity+raw", "#sector", "#loc+name+infrastructure", "#loc+name+infrastructure+detail", "#cause", "#indicator+electricity", "#indicator+health", "#indicator+needs", "#indicator+possibly", "#indicator+debris", "#loc+building+id", "#status+verification", "#loc+name", "#description", "#geo+code+plus", "#indicator+accuracy", "#loc+adm1+code", "#loc+adm2+code", "#loc+adm3+code"}
)

func ToCSV(reports []model.Report) []byte {
	extras := extraModularColumns(reports, csvColumns)
	header := append(append([]string{}, csvColumns...), extras...)
	hxl := append([]string{}, hxlRow...)
	for _, c := range extras {
		hxl = append(hxl, "#indicator+"+c)
	}

	var b bytes.Buffer
	b.WriteString(strings.Join(header, ","))
	b.WriteString("\n")
	b.WriteString(strings.Join(hxl, ",")) // HXL hashtag row
	for _, r := range reports {
		// Blank lat/lng for a location-unresolved report (never 0,0).
		latStr, lngStr := "", ""
		if reportResolved(r) {
			latStr, lngStr = numPtr(r.Lat), numPtr(r.Lng)
		}
		flat := flattenModular(r.Modular)
		row := []string{
			r.ID, latStr, lngStr, r.CapturedAt.UTC().Format(time.RFC3339),
			titleTier(r.DamageTier), r.Damage,
			strings.Join(r.InfraTypes, ";"), deref(r.InfraName), deref(r.InfraOtherDetail), strings.Join(r.CrisisNature, ";"),
			flat["electricity"], flat["health_services"], flat["pressing_needs"],
			strconv.FormatBool(r.PossiblyDamaged), r.Debris, deref(r.BuildingID),
			r.Verification, r.Place, exportDescription(r), deref(r.PlusCode), numPtr(r.GPSAccuracyMeters),
			deref(r.Adm1Pcode), deref(r.Adm2Pcode), deref(r.Adm3Pcode),
		}
		for _, c := range extras {
			row = append(row, flat[c])
		}
		for i := range row {
			row[i] = csvCell(row[i])
		}
		b.WriteString("\n")
		b.WriteString(strings.Join(row, ","))
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

	// Attribute columns beyond geom: the fixed row schema, then the three stable
	// modular sections, then any DYNAMIC modular extras (sanitized by
	// extraModularColumns, so they are safe to splice into the DDL).
	attrCols := []string{"id", "damage", "possibly_damaged", "verification", "infrastructure", "infrastructure_name", "infrastructure_other_detail", "crisis", "debris", "building_id", "place", "description", "plus_code", "admin2_pcode", "admin3_pcode", "captured_at"}
	attrCols = append(attrCols, stableModularColumns...)
	extras := extraModularColumns(reports, attrCols)
	attrCols = append(attrCols, extras...)
	ddlCols := make([]string, 0, len(attrCols))
	marks := make([]string, 0, len(attrCols)+1)
	marks = append(marks, "?") // geom
	for _, c := range attrCols {
		t := "TEXT"
		if c == "possibly_damaged" {
			t = "INTEGER"
		}
		ddlCols = append(ddlCols, c+" "+t)
		marks = append(marks, "?")
	}

	stmts := []string{
		`PRAGMA application_id = 1196444487`, // 'GPKG'
		`PRAGMA user_version = 10300`,        // GeoPackage 1.3
		`CREATE TABLE gpkg_spatial_ref_sys (srs_name TEXT NOT NULL, srs_id INTEGER PRIMARY KEY, organization TEXT NOT NULL, organization_coordsys_id INTEGER NOT NULL, definition TEXT NOT NULL, description TEXT)`,
		`CREATE TABLE gpkg_contents (table_name TEXT NOT NULL PRIMARY KEY, data_type TEXT NOT NULL, identifier TEXT UNIQUE, description TEXT DEFAULT '', last_change DATETIME NOT NULL, min_x DOUBLE, min_y DOUBLE, max_x DOUBLE, max_y DOUBLE, srs_id INTEGER)`,
		`CREATE TABLE gpkg_geometry_columns (table_name TEXT NOT NULL, column_name TEXT NOT NULL, geometry_type_name TEXT NOT NULL, srs_id INTEGER NOT NULL, z TINYINT NOT NULL, m TINYINT NOT NULL, PRIMARY KEY (table_name, column_name))`,
		// admin*_pcode: official OCHA COD-AB P-codes (source='cod') — see package doc for the GB: fallback.
		`CREATE TABLE reports (fid INTEGER PRIMARY KEY AUTOINCREMENT, geom BLOB, ` + strings.Join(ddlCols, ", ") + `)`,
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
	ins, err := tx.Prepare(`INSERT INTO reports (geom, ` + strings.Join(attrCols, ", ") + `) VALUES (` + strings.Join(marks, ",") + `)`)
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
		flat := flattenModular(r.Modular)
		args := []any{geomBlob, r.ID, r.Damage, pd, r.Verification,
			strings.Join(r.InfraTypes, ";"), deref(r.InfraName), deref(r.InfraOtherDetail),
			strings.Join(r.CrisisNature, ";"), r.Debris,
			deref(r.BuildingID), r.Place, exportDescription(r), deref(r.PlusCode),
			deref(r.Adm2Pcode), deref(r.Adm3Pcode),
			r.CapturedAt.UTC().Format(time.RFC3339)}
		for _, c := range stableModularColumns {
			args = append(args, flat[c])
		}
		for _, c := range extras {
			args = append(args, flat[c])
		}
		if _, err := ins.Exec(args...); err != nil {
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
		// Modular sections (stable three + dynamic extras) in sorted key order.
		flat := flattenModular(r.Modular)
		keys := make([]string, 0, len(flat))
		for k := range flat {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var impacts strings.Builder
		for _, k := range keys {
			impacts.WriteString(fmt.Sprintf("%s: %s\n", k, flat[k]))
		}
		desc := fmt.Sprintf(
			"damage_classification: %s\ninfrastructure_type: %s\ninfrastructure_name: %s\ninfrastructure_other_detail: %s\nhazard_type: %s\n%sdescription: %s\nplus_code: %s\nverification: %s\ntimestamp: %s",
			titleTier(r.DamageTier), strings.Join(r.InfraTypes, ";"), deref(r.InfraName), deref(r.InfraOtherDetail),
			strings.Join(r.CrisisNature, ";"), impacts.String(),
			exportDescription(r), deref(r.PlusCode),
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
