// Command ingest-footprints loads authoritative building-footprint polygons into the
// buildings table for one crisis AOI, stamping each with the dataset's real source id +
// provenance. Reports then anchor to a known building (must-have M2/M9: "overlaid
// building footprint shapefiles where available") instead of a hash of a basemap polygon.
//
// The backend runtime image is distroless (no GDAL), so this CLI ingests GeoJSON. Convert
// the upstream dataset ONCE with GDAL/ogr2ogr, then point this at the result:
//
//	# Google-Microsoft Open Buildings (GeoParquet / FlatGeobuf on HDX), clipped to an AOI:
//	ogr2ogr -f GeoJSON hatay_ob.geojson open_buildings.fgb -clipsrc 36.05 36.13 36.30 36.30
//	ingest-footprints -crisis crisis-antakya -source google_microsoft_open_buildings \
//	    -source-version 2025-05 -file hatay_ob.geojson
//
//	# OpenStreetMap buildings (HOT Export Tool shapefile, or osmium-extracted .osm):
//	ogr2ogr -f GeoJSON hatay_osm.geojson hatay_osm_buildings.shp
//	ingest-footprints -crisis crisis-antakya -source osm -source-version 2026-06 \
//	    -id-prop osm_id -file hatay_osm.geojson
//
//	# An official government building shapefile:
//	ogr2ogr -f GeoJSON gov.geojson buildings.shp
//	ingest-footprints -crisis crisis-antakya -source "gov:hatay-cadastre" -id-prop bldg_ref -file gov.geojson
//
// Run with DATABASE_URL pointing at the target DB (same default as the server). The load
// is idempotent: re-running refreshes geometry + provenance and preserves crowd-derived
// damage status (UpsertBuildingFootprint's ON CONFLICT).
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/stepanok/beacon-server/internal/db"
	"github.com/stepanok/beacon-server/internal/store"
)

func main() {
	dsn := flag.String("dsn", env("DATABASE_URL", "postgres://beacon:beacon@localhost:5544/beacon?sslmode=disable"), "Postgres DSN")
	crisis := flag.String("crisis", "", "crisis id to scope footprints to (e.g. crisis-antakya); empty = unscoped")
	source := flag.String("source", "osm", "provenance label: osm | google_microsoft_open_buildings | gov:<name>")
	version := flag.String("source-version", "", "dataset release / snapshot (e.g. 2026-06)")
	file := flag.String("file", "", "GeoJSON FeatureCollection of building Polygons/MultiPolygons (RFC 7946, lon,lat)")
	idProp := flag.String("id-prop", "", "feature property holding the dataset id; empty = use the GeoJSON feature id")
	idPrefix := flag.String("id-prefix", "", "building-id prefix; empty = the -source value")
	batchN := flag.Int("batch", 500, "rows per commit")
	flag.Parse()

	if *file == "" {
		fatal("-file is required (a GeoJSON FeatureCollection of building footprints)")
	}
	prefix := *idPrefix
	if prefix == "" {
		prefix = *source
	}

	f, err := os.Open(*file)
	if err != nil {
		fatal("open %s: %v", *file, err)
	}
	defer f.Close()

	dec := json.NewDecoder(f)
	if err := seekToFeatures(dec); err != nil {
		fatal("not a GeoJSON FeatureCollection: %v", err)
	}

	ctx := context.Background()
	pool, err := db.NewPool(ctx, *dsn, 4)
	if err != nil {
		fatal("db connect: %v", err)
	}
	defer pool.Close()

	type feat struct {
		ID         json.RawMessage            `json:"id"`
		Geometry   json.RawMessage            `json:"geometry"`
		Properties map[string]json.RawMessage `json:"properties"`
	}

	start := time.Now()
	var ok, skip, fail, total int
	tx, err := pool.Begin(ctx)
	if err != nil {
		fatal("begin: %v", err)
	}
	inBatch := 0
	for dec.More() {
		var ft feat
		if err := dec.Decode(&ft); err != nil {
			fail++
			continue
		}
		total++
		if len(ft.Geometry) == 0 || string(ft.Geometry) == "null" {
			skip++
			continue
		}
		sid := extractID(ft.ID, ft.Properties, *idProp)
		if sid == "" {
			skip++
			continue
		}
		bid := prefix + ":" + sid
		// SAVEPOINT so one invalid polygon rolls back only itself, not the whole batch.
		if _, e := tx.Exec(ctx, "SAVEPOINT f"); e != nil {
			fatal("savepoint: %v", e)
		}
		if err := store.UpsertBuildingFootprint(ctx, tx, bid, *crisis, *source, sid, *version, ft.Geometry); err != nil {
			_, _ = tx.Exec(ctx, "ROLLBACK TO SAVEPOINT f")
			fail++
			continue
		}
		_, _ = tx.Exec(ctx, "RELEASE SAVEPOINT f")
		ok++
		inBatch++
		if inBatch >= *batchN {
			if err := tx.Commit(ctx); err != nil {
				fatal("commit: %v", err)
			}
			if tx, err = pool.Begin(ctx); err != nil {
				fatal("begin: %v", err)
			}
			inBatch = 0
			fmt.Printf("  ... %d ingested (%d skipped, %d failed)\n", ok, skip, fail)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		fatal("final commit: %v", err)
	}
	fmt.Printf("done: %d footprints ingested, %d skipped, %d failed (of %d features) in %s\n",
		ok, skip, fail, total, time.Since(start).Round(time.Millisecond))
}

// seekToFeatures advances the decoder to just inside the top-level "features" array, so
// the caller can stream features with dec.More()/dec.Decode without loading the whole
// (potentially huge) FeatureCollection into memory.
func seekToFeatures(dec *json.Decoder) error {
	t, err := dec.Token()
	if err != nil {
		return err
	}
	if d, ok := t.(json.Delim); !ok || d != '{' {
		return fmt.Errorf("expected a JSON object at the top level, got %v", t)
	}
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return err
		}
		ks, _ := key.(string)
		if ks == "features" {
			at, err := dec.Token()
			if err != nil {
				return err
			}
			if d, ok := at.(json.Delim); !ok || d != '[' {
				return fmt.Errorf(`"features" is not an array`)
			}
			return nil
		}
		if err := skipValue(dec); err != nil {
			return err
		}
	}
	return fmt.Errorf(`no "features" array found`)
}

// skipValue consumes one complete JSON value (object, array, or scalar) and discards it.
func skipValue(dec *json.Decoder) error {
	var raw json.RawMessage
	return dec.Decode(&raw)
}

// extractID pulls the dataset id from a feature: the named property if -id-prop is set,
// otherwise the GeoJSON feature id. Strings are unquoted; numbers pass through verbatim.
func extractID(id json.RawMessage, props map[string]json.RawMessage, idProp string) string {
	if idProp != "" {
		if raw, ok := props[idProp]; ok {
			return rawToString(raw)
		}
		return ""
	}
	return rawToString(id)
}

func rawToString(raw json.RawMessage) string {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return ""
	}
	if s[0] == '"' {
		var str string
		if json.Unmarshal(raw, &str) == nil {
			return str
		}
	}
	return s
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func fatal(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "ingest-footprints: "+format+"\n", a...)
	os.Exit(1)
}
