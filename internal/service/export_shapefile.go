package service

import (
	"archive/zip"
	"bufio"
	"bytes"
	"encoding/binary"
	"io"
	"math"
	"os"
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

// ToShapefile is the in-memory wrapper over StreamShapefile (tests + small
// callers); the export endpoint streams the ZIP straight to the client.
func ToShapefile(reports []model.Report) ([]byte, error) {
	var buf bytes.Buffer
	if err := StreamShapefile(&buf, sliceSource(reports)); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// StreamShapefile writes the .shp/.shx/.dbf/.prj ZIP for RESOLVED reports while
// bounding RAM at crisis scale: the three variable-length member BODIES are staged
// to temp files in a SINGLE cursor pass (the .shp/.shx/.dbf headers need the final
// record count + bbox up front, so bodies are written first, then prefixed with
// their headers as the ZIP is assembled). The ZIP itself streams to w. Landmark-only
// (unresolved) reports can't be a POINT record and are skipped (never 0,0).
func StreamShapefile(w io.Writer, src RowSource) error {
	const headerLen = 100
	const recordLen = 8 + 20 // 8-byte record header + point content (4 type + 8 X + 8 Y)

	shpTmp, err := os.CreateTemp("", "beacon-*.shpbody")
	if err != nil {
		return err
	}
	shxTmp, err := os.CreateTemp("", "beacon-*.shxbody")
	if err != nil {
		shpTmp.Close()
		os.Remove(shpTmp.Name())
		return err
	}
	dbfTmp, err := os.CreateTemp("", "beacon-*.dbfbody")
	if err != nil {
		shpTmp.Close()
		os.Remove(shpTmp.Name())
		shxTmp.Close()
		os.Remove(shxTmp.Name())
		return err
	}
	defer func() {
		shpTmp.Close()
		os.Remove(shpTmp.Name())
		shxTmp.Close()
		os.Remove(shxTmp.Name())
		dbfTmp.Close()
		os.Remove(dbfTmp.Name())
	}()

	shpW := bufio.NewWriterSize(shpTmp, 64*1024)
	shxW := bufio.NewWriterSize(shxTmp, 64*1024)
	dbfW := bufio.NewWriterSize(dbfTmp, 64*1024)

	minX, minY, maxX, maxY := 0.0, 0.0, 0.0, 0.0
	count := 0
	offsetWords := headerLen / 2
	err = src(func(r *model.Report) error {
		if !reportResolved(*r) {
			return nil
		}
		x, y := *r.Lng, *r.Lat
		if count == 0 {
			minX, maxX, minY, maxY = x, x, y, y
		} else {
			minX, maxX = math.Min(minX, x), math.Max(maxX, x)
			minY, maxY = math.Min(minY, y), math.Max(maxY, y)
		}
		count++
		// .shp record: header BIG-endian, geometry content LITTLE-endian.
		_ = binary.Write(shpW, binary.BigEndian, int32(count)) // record number (1-based)
		_ = binary.Write(shpW, binary.BigEndian, int32(10))    // content length in words: (4+8+8)/2
		_ = binary.Write(shpW, binary.LittleEndian, int32(1))  // shape type: Point
		_ = binary.Write(shpW, binary.LittleEndian, x)
		_ = binary.Write(shpW, binary.LittleEndian, y)
		// .shx index record (BIG-endian): offset + content length, both in words.
		_ = binary.Write(shxW, binary.BigEndian, int32(offsetWords))
		_ = binary.Write(shxW, binary.BigEndian, int32(10))
		offsetWords += recordLen / 2
		// .dbf record: deletion flag + fixed-width ASCII fields.
		dbfW.WriteByte(0x20)
		for _, f := range shapefileFields {
			dbfW.Write(asciiFixed(f.value(*r), f.width))
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, fw := range []*bufio.Writer{shpW, shxW, dbfW} {
		if err := fw.Flush(); err != nil {
			return err
		}
	}
	for _, tf := range []*os.File{shpTmp, shxTmp, dbfTmp} {
		if _, err := tf.Seek(0, io.SeekStart); err != nil {
			return err
		}
	}
	if count == 0 {
		minX, minY, maxX, maxY = 0, 0, 0, 0
	}

	zw := zip.NewWriter(w)
	type member struct {
		name   string
		header []byte
		body   *os.File
		footer []byte
	}
	members := []member{
		{"beacon-reports.shp", shpHeader(headerLen+count*recordLen, minX, minY, maxX, maxY), shpTmp, nil},
		{"beacon-reports.shx", shpHeader(headerLen+count*8, minX, minY, maxX, maxY), shxTmp, nil},
		{"beacon-reports.dbf", dbfHeader(count), dbfTmp, []byte{0x1A}}, // 0x1A = dBASE EOF
		{"beacon-reports.prj", []byte(wgs84PRJ), nil, nil},
	}
	for _, m := range members {
		f, err := zw.Create(m.name)
		if err != nil {
			return err
		}
		if _, err := f.Write(m.header); err != nil {
			return err
		}
		if m.body != nil {
			if _, err := io.Copy(f, m.body); err != nil {
				return err
			}
		}
		if m.footer != nil {
			if _, err := f.Write(m.footer); err != nil {
				return err
			}
		}
	}
	return zw.Close()
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

// dbfHeader writes the dBASE III table header: the 32-byte file header (with the
// final record count) + the field descriptors + the 0x0D terminator. The records
// themselves are streamed separately by StreamShapefile, and the 0x1A EOF byte is
// appended after them — so the full .dbf is dbfHeader + records + 0x1A.
func dbfHeader(count int) []byte {
	recordSize := 1 // leading deletion flag
	for _, f := range shapefileFields {
		recordSize += f.width
	}
	headerSize := 32 + 32*len(shapefileFields) + 1

	b := &bytes.Buffer{}
	b.WriteByte(0x03) // dBASE III, no memo
	now := time.Now().UTC()
	b.Write([]byte{byte(now.Year() - 1900), byte(int(now.Month())), byte(now.Day())})
	_ = binary.Write(b, binary.LittleEndian, uint32(count))
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
