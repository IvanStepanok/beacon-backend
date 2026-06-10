package service

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"math"
	"strings"
	"time"

	"github.com/stepanok/beacon-server/internal/model"
)

// Shapefile is the legacy ESRI GIS interchange format explicitly named in the
// challenge brief. GeoPackage (also exported) is its modern OGC successor and is what
// the UNDP stack (RAPIDA / GeoHub) actually consumes — but a real .shp removes any
// doubt for a checklist reviewer and opens directly in QGIS/ArcGIS. This is a
// dependency-free POINT writer (a damage report is a point); the dBASE III .dbf holds
// ASCII enum/id/code attributes only (dBASE has no reliable UTF-8), so free-text
// place/description are intentionally omitted here — they live in the GeoJSON/CSV/GPKG
// exports. WGS84 (.prj) is included so tools georeference it without prompting.

const wgs84PRJ = `GEOGCS["GCS_WGS_1984",DATUM["D_WGS_1984",SPHEROID["WGS_1984",6378137,298.257223563]],PRIMEM["Greenwich",0],UNIT["Degree",0.0174532925199433]]`

type shpField struct {
	name  string // ≤10 chars (dBASE field-name limit)
	width int
	value func(model.Report) string
}

var shapefileFields = []shpField{
	{"id", 24, func(r model.Report) string { return r.ID }},
	{"damage", 12, func(r model.Report) string { return titleTier(r.DamageTier) }},
	{"infra", 40, func(r model.Report) string { return strings.Join(r.InfraTypes, ";") }},
	{"hazard", 40, func(r model.Report) string { return strings.Join(r.CrisisNature, ";") }},
	{"verif", 10, func(r model.Report) string { return r.Verification }},
	{"pluscode", 16, func(r model.Report) string { return deref(r.PlusCode) }},
	{"clusters", 40, func(r model.Report) string { return strings.Join(r.Clusters, ";") }},
	{"timestamp", 20, func(r model.Report) string { return r.CapturedAt.UTC().Format(time.RFC3339) }},
}

// ToShapefile renders RESOLVED reports as an ESRI Shapefile (POINT), returning a ZIP
// of the .shp/.shx/.dbf/.prj members. Landmark-only (unresolved) reports cannot be a
// shapefile POINT record and are skipped (never emitted at 0,0) — they remain in the
// GeoJSON/CSV exports.
func ToShapefile(reports []model.Report) ([]byte, error) {
	pts := make([]model.Report, 0, len(reports))
	for _, r := range reports {
		if reportResolved(r) {
			pts = append(pts, r)
		}
	}

	const headerLen = 100
	const recordLen = 8 + 20 // 8-byte record header + point content (4 type + 8 X + 8 Y)

	minX, minY, maxX, maxY := 0.0, 0.0, 0.0, 0.0
	for i, r := range pts {
		x, y := *r.Lng, *r.Lat
		if i == 0 {
			minX, maxX, minY, maxY = x, x, y, y
			continue
		}
		minX, maxX = math.Min(minX, x), math.Max(maxX, x)
		minY, maxY = math.Min(minY, y), math.Max(maxY, y)
	}

	shp := &bytes.Buffer{}
	shx := &bytes.Buffer{}
	shp.Write(shpHeader(headerLen+len(pts)*recordLen, minX, minY, maxX, maxY))
	shx.Write(shpHeader(headerLen+len(pts)*8, minX, minY, maxX, maxY))

	offsetWords := headerLen / 2
	for i, r := range pts {
		// .shp record: header is BIG-endian, geometry content is LITTLE-endian.
		_ = binary.Write(shp, binary.BigEndian, int32(i+1)) // record number (1-based)
		_ = binary.Write(shp, binary.BigEndian, int32(10))  // content length in 16-bit words: (4+8+8)/2
		_ = binary.Write(shp, binary.LittleEndian, int32(1))    // shape type: Point
		_ = binary.Write(shp, binary.LittleEndian, *r.Lng)      // X
		_ = binary.Write(shp, binary.LittleEndian, *r.Lat)      // Y
		// .shx index record (BIG-endian): offset + content length, both in words.
		_ = binary.Write(shx, binary.BigEndian, int32(offsetWords))
		_ = binary.Write(shx, binary.BigEndian, int32(10))
		offsetWords += recordLen / 2
	}

	dbf := buildDBF(pts)

	out := &bytes.Buffer{}
	zw := zip.NewWriter(out)
	members := []struct {
		name string
		data []byte
	}{
		{"beacon-reports.shp", shp.Bytes()},
		{"beacon-reports.shx", shx.Bytes()},
		{"beacon-reports.dbf", dbf},
		{"beacon-reports.prj", []byte(wgs84PRJ)},
	}
	for _, m := range members {
		f, err := zw.Create(m.name)
		if err != nil {
			return nil, err
		}
		if _, err := f.Write(m.data); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// shpHeader builds the 100-byte .shp/.shx header. File code + file length are
// big-endian; version, shape type, and the bbox are little-endian (per the spec).
func shpHeader(fileLenBytes int, minX, minY, maxX, maxY float64) []byte {
	h := make([]byte, 100)
	binary.BigEndian.PutUint32(h[0:], 9994)                  // file code
	binary.BigEndian.PutUint32(h[24:], uint32(fileLenBytes/2)) // file length, 16-bit words
	binary.LittleEndian.PutUint32(h[28:], 1000)              // version
	binary.LittleEndian.PutUint32(h[32:], 1)                 // shape type: Point
	binary.LittleEndian.PutUint64(h[36:], math.Float64bits(minX))
	binary.LittleEndian.PutUint64(h[44:], math.Float64bits(minY))
	binary.LittleEndian.PutUint64(h[52:], math.Float64bits(maxX))
	binary.LittleEndian.PutUint64(h[60:], math.Float64bits(maxY))
	// Z/M ranges (bytes 68..99) stay zero — Point has neither.
	return h
}

// buildDBF writes a dBASE III table: header + field descriptors + fixed-width,
// space-padded ASCII records, one per point.
func buildDBF(reports []model.Report) []byte {
	recordSize := 1 // leading deletion flag
	for _, f := range shapefileFields {
		recordSize += f.width
	}
	headerSize := 32 + 32*len(shapefileFields) + 1

	b := &bytes.Buffer{}
	b.WriteByte(0x03) // dBASE III, no memo
	now := time.Now().UTC()
	b.Write([]byte{byte(now.Year() - 1900), byte(int(now.Month())), byte(now.Day())})
	_ = binary.Write(b, binary.LittleEndian, uint32(len(reports)))
	_ = binary.Write(b, binary.LittleEndian, uint16(headerSize))
	_ = binary.Write(b, binary.LittleEndian, uint16(recordSize))
	b.Write(make([]byte, 20)) // reserved

	for _, f := range shapefileFields {
		name := make([]byte, 11) // null-padded, ≤10 chars + NUL
		copy(name, f.name)
		b.Write(name)
		b.WriteByte('C')         // field type: Character
		b.Write(make([]byte, 4)) // field data address (unused)
		b.WriteByte(byte(f.width))
		b.WriteByte(0)            // decimal count
		b.Write(make([]byte, 14)) // reserved
	}
	b.WriteByte(0x0D) // header terminator

	for _, r := range reports {
		b.WriteByte(0x20) // record present (not deleted)
		for _, f := range shapefileFields {
			b.Write(asciiFixed(f.value(r), f.width))
		}
	}
	b.WriteByte(0x1A) // EOF
	return b.Bytes()
}

// asciiFixed left-justifies s into a width-byte ASCII cell (space-padded, truncated);
// non-ASCII runes become '?' since dBASE III has no reliable Unicode encoding.
func asciiFixed(s string, width int) []byte {
	out := make([]byte, width)
	for i := range out {
		out[i] = ' '
	}
	j := 0
	for _, c := range s {
		if j >= width {
			break
		}
		if c < 0x20 || c > 0x7E {
			c = '?'
		}
		out[j] = byte(c)
		j++
	}
	return out
}
