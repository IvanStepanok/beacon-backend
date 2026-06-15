package service

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"

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

// ToGeoJSON is the in-memory wrapper (tests + small callers) over StreamGeoJSON;
// the export endpoint streams from a DB cursor instead (see handler.ExportReports).
func ToGeoJSON(reports []model.Report) ([]byte, error) {
	var buf bytes.Buffer
	if err := StreamGeoJSON(&buf, sliceSource(reports)); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
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
	csvColumns = []string{"id", "latitude", "longitude", "timestamp", "damage_classification", "damage", "infrastructure_type", "infrastructure_name", "infrastructure_other_detail", "hazard_type", "electricity", "health_services", "pressing_needs", "possiblyDamaged", "debris", "buildingId", "verification", "place", "description", "plus_code", "accuracy_m", "admin1_pcode", "admin2_pcode", "admin3_pcode", "h3id"}
	hxlRow     = []string{"#meta+id", "#geo+lat", "#geo+lon", "#date", "#severity+grade", "#severity+raw", "#sector", "#loc+name+infrastructure", "#loc+name+infrastructure+detail", "#cause", "#indicator+electricity", "#indicator+health", "#indicator+needs", "#indicator+possibly", "#indicator+debris", "#loc+building+id", "#status+verification", "#loc+name", "#description", "#geo+code+plus", "#indicator+accuracy", "#loc+adm1+code", "#loc+adm2+code", "#loc+adm3+code", "#geo+h3"}
)

// ToCSV is the in-memory wrapper over StreamCSV (tests + small callers); the
// export endpoint streams from a DB cursor with extras from ModularKeysRaw.
func ToCSV(reports []model.Report) []byte {
	extras := extraModularColumns(reports, csvColumns)
	var b bytes.Buffer
	// StreamCSV only errors on writer failure; bytes.Buffer never fails.
	_ = StreamCSV(&b, sliceSource(reports), extras)
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

// ToGPKG is the in-memory wrapper over the streaming GeoPackage builder (tests +
// small callers); the export endpoint streams the temp file straight to the client.
func ToGPKG(reports []model.Report) ([]byte, error) {
	var buf bytes.Buffer
	extras := extraModularColumns(reports, gpkgAttrCols)
	if err := StreamGPKG(&buf, sliceSource(reports), extras); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
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
// ToKML is the in-memory wrapper over StreamKML (tests + small callers); the
// export endpoint streams from a DB cursor.
func ToKML(reports []model.Report) []byte {
	var b bytes.Buffer
	_ = StreamKML(&b, sliceSource(reports))
	return b.Bytes()
}
