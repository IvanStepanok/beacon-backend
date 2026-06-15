package service

import (
	"bufio"
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver (no CGO) for GeoPackage export

	"github.com/stepanok/beacon-server/internal/model"
	"github.com/stepanok/beacon-server/internal/store"
)

// reportH3 is the resolution-8 H3 cell id for an export row — the native h3id
// interoperability column (RAPIDA/GeoHub). Empty for a location-unresolved report
// (no point), matching the blank lat/lng those rows already emit. Computed with the
// SAME helper as the stored h3_r8 column, so export and aggregation always agree.
func reportH3(r model.Report) string {
	if !reportResolved(r) || r.Lat == nil || r.Lng == nil {
		return ""
	}
	return store.H3CellR8(*r.Lat, *r.Lng)
}

// RowSource invokes yield once per report, in export order, returning the first
// error from the underlying query or from yield. Backed by a DB cursor
// (store.ExportEach) on the hot path so the streaming writers below never hold
// the whole result set — bounding server memory at crisis scale (100k–500k).
type RowSource func(yield func(*model.Report) error) error

// sliceSource adapts an in-memory slice to a RowSource — used by the legacy
// ToGeoJSON/ToCSV/… byte-returning wrappers (tests + small callers) so there is a
// SINGLE implementation of each format shared with the streaming path.
func sliceSource(reports []model.Report) RowSource {
	return func(yield func(*model.Report) error) error {
		for i := range reports {
			if err := yield(&reports[i]); err != nil {
				return err
			}
		}
		return nil
	}
}

// extraModularColumnsFromKeys computes the sorted DYNAMIC modular columns from the
// raw distinct JSON keys (store.ModularKeysRaw), applying the SAME sanitize rules
// as extraModularColumns (camelToSnake, safe-column gate, reserved→x_, skip
// fixed/stable) so the streaming CSV/GPKG column set matches the slice path exactly.
func extraModularColumnsFromKeys(rawKeys, fixed []string) []string {
	taken := map[string]bool{}
	for _, c := range fixed {
		taken[c] = true
	}
	for _, c := range stableModularColumns {
		taken[c] = true
	}
	seen := map[string]bool{}
	for _, raw := range rawKeys {
		col := camelToSnake(raw)
		if !safeColumnRe.MatchString(col) {
			continue
		}
		if reservedExportColumns[col] {
			col = "x_" + col
		}
		if taken[col] || seen[col] {
			continue
		}
		seen[col] = true
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// CSVExtraColumns / GPKGExtraColumns turn the raw distinct modular keys
// (store.ModularKeysRaw) into the sorted dynamic export columns for the streaming
// CSV / GPKG writers — the cursor path's equivalent of extraModularColumns, which
// the slice path derives from the rows themselves.
func CSVExtraColumns(rawKeys []string) []string  { return extraModularColumnsFromKeys(rawKeys, csvColumns) }
func GPKGExtraColumns(rawKeys []string) []string { return extraModularColumnsFromKeys(rawKeys, gpkgAttrCols) }

// ---- GeoJSON (streaming) ------------------------------------------------------

// geoJSONFeature builds one Feature (geometry null for an unresolved report; never
// [0,0]). Shared by the streaming writer and the ToGeoJSON wrapper.
func geoJSONFeature(r *model.Report) exportFeature {
	var geom *exportGeometry
	if reportResolved(*r) {
		geom = &exportGeometry{Type: "Point", Coordinates: [2]float64{*r.Lng, *r.Lat}}
	}
	props := map[string]any{}
	for k, v := range flattenModular(r.Modular) {
		props[k] = v
	}
	props["id"] = r.ID
	props["damage_classification"] = titleTier(r.DamageTier)
	props["damage"] = r.Damage
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
	props["description"] = exportDescription(*r)
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
	if v := reportH3(*r); v != "" {
		props["h3id"] = v
	}
	return exportFeature{Type: "Feature", Geometry: geom, Properties: props}
}

// StreamGeoJSON writes a FeatureCollection incrementally: the envelope plus one
// indented, non-HTML-escaped Feature per row (": " separators preserved so the
// output reads like the prior json.Encoder form). RAM stays at one feature.
func StreamGeoJSON(w io.Writer, src RowSource) error {
	bw := bufio.NewWriterSize(w, 64*1024)
	if _, err := bw.WriteString("{\n  \"type\": \"FeatureCollection\",\n  \"features\": ["); err != nil {
		return err
	}
	first := true
	err := src(func(r *model.Report) error {
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		enc.SetIndent("", "  ")
		if err := enc.Encode(geoJSONFeature(r)); err != nil {
			return err
		}
		sep := ",\n"
		if first {
			sep = "\n"
			first = false
		}
		if _, err := bw.WriteString(sep); err != nil {
			return err
		}
		// Encoder appends a trailing newline — trim it so commas sit cleanly.
		_, err := bw.Write(bytes.TrimRight(buf.Bytes(), "\n"))
		return err
	})
	if err != nil {
		return err
	}
	if _, err := bw.WriteString("\n  ]\n}"); err != nil {
		return err
	}
	return bw.Flush()
}

// ---- CSV (streaming) ----------------------------------------------------------

// csvRowCells builds the raw (un-quoted) cells for one report given the dynamic
// extras. Shared by the streaming writer and the ToCSV wrapper.
func csvRowCells(r *model.Report, extras []string) []string {
	latStr, lngStr := "", ""
	if reportResolved(*r) {
		latStr, lngStr = numPtr(r.Lat), numPtr(r.Lng)
	}
	flat := flattenModular(r.Modular)
	row := []string{
		r.ID, latStr, lngStr, r.CapturedAt.UTC().Format(time.RFC3339),
		titleTier(r.DamageTier), r.Damage,
		strings.Join(r.InfraTypes, ";"), deref(r.InfraName), deref(r.InfraOtherDetail), strings.Join(r.CrisisNature, ";"),
		flat["electricity"], flat["health_services"], flat["pressing_needs"],
		strconv.FormatBool(r.PossiblyDamaged), r.Debris, deref(r.BuildingID),
		r.Verification, r.Place, exportDescription(*r), deref(r.PlusCode), numPtr(r.GPSAccuracyMeters),
		deref(r.Adm1Pcode), deref(r.Adm2Pcode), deref(r.Adm3Pcode), reportH3(*r),
	}
	for _, c := range extras {
		row = append(row, flat[c])
	}
	return row
}

// StreamCSV writes the C2 header + HXL tag row + one CSV row per report, matching
// the byte structure of the slice ToCSV (header\n, hxl, then "\n"+row per row).
func StreamCSV(w io.Writer, src RowSource, extras []string) error {
	bw := bufio.NewWriterSize(w, 64*1024)
	header := append(append([]string{}, csvColumns...), extras...)
	hxl := append([]string{}, hxlRow...)
	for _, c := range extras {
		hxl = append(hxl, "#indicator+"+c)
	}
	if _, err := bw.WriteString(strings.Join(header, ",") + "\n" + strings.Join(hxl, ",")); err != nil {
		return err
	}
	err := src(func(r *model.Report) error {
		row := csvRowCells(r, extras)
		for i := range row {
			row[i] = csvCell(row[i])
		}
		_, err := bw.WriteString("\n" + strings.Join(row, ","))
		return err
	})
	if err != nil {
		return err
	}
	return bw.Flush()
}

// ---- KML (streaming) ----------------------------------------------------------

// writeKMLPlacemark writes one <Placemark> for a RESOLVED report (callers skip
// unresolved). Shared by the streaming writer and the ToKML wrapper.
func writeKMLPlacemark(bw *bufio.Writer, r *model.Report) {
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
		exportDescription(*r), deref(r.PlusCode),
		r.Verification, r.CapturedAt.UTC().Format(time.RFC3339))
	bw.WriteString("<Placemark>")
	bw.WriteString("<name>" + xmlEscape(r.ID) + "</name>")
	bw.WriteString("<description>" + xmlEscape(desc) + "</description>")
	bw.WriteString(fmt.Sprintf("<Point><coordinates>%s,%s</coordinates></Point>",
		strconv.FormatFloat(*r.Lng, 'g', -1, 64), strconv.FormatFloat(*r.Lat, 'g', -1, 64)))
	bw.WriteString("</Placemark>\n")
}

func StreamKML(w io.Writer, src RowSource) error {
	bw := bufio.NewWriterSize(w, 64*1024)
	bw.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	bw.WriteString(`<kml xmlns="http://www.opengis.net/kml/2.2"><Document>` + "\n")
	bw.WriteString(`<name>Beacon community damage reports</name>` + "\n")
	err := src(func(r *model.Report) error {
		if reportResolved(*r) {
			writeKMLPlacemark(bw, r)
		}
		return nil
	})
	if err != nil {
		return err
	}
	bw.WriteString(`</Document></kml>`)
	return bw.Flush()
}

// ---- GPKG (streaming to a temp file, then copied out) -------------------------

// gpkgAttrCols is the fixed attribute column list (beyond geom) for the GPKG
// reports table, shared so the DDL and the INSERT stay in lockstep.
var gpkgAttrCols = []string{"id", "damage", "possibly_damaged", "verification", "infrastructure", "infrastructure_name", "infrastructure_other_detail", "crisis", "debris", "building_id", "place", "description", "plus_code", "admin2_pcode", "admin3_pcode", "captured_at", "h3id"}

// gpkgRowArgs builds the INSERT bind values for one report (geom first, then the
// fixed columns, then stable + dynamic modular extras), matching gpkgAttrCols.
func gpkgRowArgs(r *model.Report, extras []string) []any {
	pd := 0
	if r.PossiblyDamaged {
		pd = 1
	}
	var geomBlob any
	if reportResolved(*r) {
		geomBlob = gpbPoint(*r.Lng, *r.Lat)
	}
	flat := flattenModular(r.Modular)
	args := []any{geomBlob, r.ID, r.Damage, pd, r.Verification,
		strings.Join(r.InfraTypes, ";"), deref(r.InfraName), deref(r.InfraOtherDetail),
		strings.Join(r.CrisisNature, ";"), r.Debris,
		deref(r.BuildingID), r.Place, exportDescription(*r), deref(r.PlusCode),
		deref(r.Adm2Pcode), deref(r.Adm3Pcode),
		r.CapturedAt.UTC().Format(time.RFC3339), reportH3(*r)}
	for _, c := range stableModularColumns {
		args = append(args, flat[c])
	}
	for _, c := range extras {
		args = append(args, flat[c])
	}
	return args
}

// buildGPKGFile writes the GeoPackage to a temp file by streaming rows from src
// (single forward pass: bbox is tracked during insert and patched into
// gpkg_contents afterwards). Returns the temp path; the caller streams it out and
// removes it. RAM stays at one row + SQLite's own page cache, not the dataset.
func buildGPKGFile(src RowSource, extras []string) (string, error) {
	f, err := os.CreateTemp("", "beacon-*.gpkg")
	if err != nil {
		return "", err
	}
	path := f.Name()
	f.Close()

	db, err := sql.Open("sqlite", path)
	if err != nil {
		os.Remove(path)
		return "", err
	}
	closed := false
	cleanup := func() {
		if !closed {
			db.Close()
		}
		os.Remove(path)
	}

	now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")

	attrCols := append(append([]string{}, gpkgAttrCols...), stableModularColumns...)
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
		`PRAGMA application_id = 1196444487`,
		`PRAGMA user_version = 10300`,
		`CREATE TABLE gpkg_spatial_ref_sys (srs_name TEXT NOT NULL, srs_id INTEGER PRIMARY KEY, organization TEXT NOT NULL, organization_coordsys_id INTEGER NOT NULL, definition TEXT NOT NULL, description TEXT)`,
		`CREATE TABLE gpkg_contents (table_name TEXT NOT NULL PRIMARY KEY, data_type TEXT NOT NULL, identifier TEXT UNIQUE, description TEXT DEFAULT '', last_change DATETIME NOT NULL, min_x DOUBLE, min_y DOUBLE, max_x DOUBLE, max_y DOUBLE, srs_id INTEGER)`,
		`CREATE TABLE gpkg_geometry_columns (table_name TEXT NOT NULL, column_name TEXT NOT NULL, geometry_type_name TEXT NOT NULL, srs_id INTEGER NOT NULL, z TINYINT NOT NULL, m TINYINT NOT NULL, PRIMARY KEY (table_name, column_name))`,
		`CREATE TABLE reports (fid INTEGER PRIMARY KEY AUTOINCREMENT, geom BLOB, ` + strings.Join(ddlCols, ", ") + `)`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			cleanup()
			return "", fmt.Errorf("gpkg ddl: %w", err)
		}
	}
	srs := [][]any{
		{"WGS 84 geographic", 4326, "EPSG", 4326, `GEOGCS["WGS 84",DATUM["WGS_1984",SPHEROID["WGS 84",6378137,298.257223563]],PRIMEM["Greenwich",0],UNIT["degree",0.0174532925199433]]`, "longitude/latitude WGS84"},
		{"Undefined cartesian SRS", -1, "NONE", -1, "undefined", "undefined cartesian coordinate reference system"},
		{"Undefined geographic SRS", 0, "NONE", 0, "undefined", "undefined geographic coordinate reference system"},
	}
	for _, r := range srs {
		if _, err := db.Exec(`INSERT INTO gpkg_spatial_ref_sys VALUES (?,?,?,?,?,?)`, r...); err != nil {
			cleanup()
			return "", err
		}
	}
	// bbox is unknown until rows are streamed; insert a placeholder, patch later.
	if _, err := db.Exec(`INSERT INTO gpkg_contents (table_name, data_type, identifier, description, last_change, min_x, min_y, max_x, max_y, srs_id) VALUES ('reports','features','reports','Beacon community damage reports',?,?,?,?,?,4326)`,
		now, 0.0, 0.0, 0.0, 0.0); err != nil {
		cleanup()
		return "", err
	}
	if _, err := db.Exec(`INSERT INTO gpkg_geometry_columns VALUES ('reports','geom','POINT',4326,0,0)`); err != nil {
		cleanup()
		return "", err
	}

	tx, err := db.Begin()
	if err != nil {
		cleanup()
		return "", err
	}
	ins, err := tx.Prepare(`INSERT INTO reports (geom, ` + strings.Join(attrCols, ", ") + `) VALUES (` + strings.Join(marks, ",") + `)`)
	if err != nil {
		tx.Rollback()
		cleanup()
		return "", err
	}

	minX, minY, maxX, maxY := 180.0, 90.0, -180.0, -90.0
	resolved := 0
	err = src(func(r *model.Report) error {
		if reportResolved(*r) {
			resolved++
			minX, maxX = min(minX, *r.Lng), max(maxX, *r.Lng)
			minY, maxY = min(minY, *r.Lat), max(maxY, *r.Lat)
		}
		_, e := ins.Exec(gpkgRowArgs(r, extras)...)
		return e
	})
	if err != nil {
		ins.Close()
		tx.Rollback()
		cleanup()
		return "", err
	}
	ins.Close()
	if err := tx.Commit(); err != nil {
		cleanup()
		return "", err
	}
	if resolved == 0 {
		minX, minY, maxX, maxY = 0, 0, 0, 0
	}
	if _, err := db.Exec(`UPDATE gpkg_contents SET min_x=?, min_y=?, max_x=?, max_y=? WHERE table_name='reports'`,
		minX, minY, maxX, maxY); err != nil {
		cleanup()
		return "", err
	}
	if err := db.Close(); err != nil {
		os.Remove(path)
		return "", err
	}
	closed = true
	return path, nil
}

// StreamGPKG builds the GeoPackage to a temp file and copies it to w, removing the
// temp file afterward. (A SQLite container can't be produced incrementally to a
// stream, so disk is the bound — RAM is one row, not the dataset.)
func StreamGPKG(w io.Writer, src RowSource, extras []string) error {
	path, err := buildGPKGFile(src, extras)
	if err != nil {
		return err
	}
	defer os.Remove(path)
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(w, f)
	return err
}
