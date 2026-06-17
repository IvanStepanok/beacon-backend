# Authoritative building footprints

The "tap the building" flow (must-have M2/M9: *"overlaid building footprint shapefiles where
available"*) can now use **authoritative footprint polygons** ingested per crisis AOI — from
OpenStreetMap, the Google–Microsoft Open Buildings dataset, or an official government shapefile —
instead of only the basemap's generic OpenMapTiles `building` layer. Each footprint carries its real
source id + provenance, so a report anchors to a known building rather than to a hash of a basemap
polygon ring.

## What ships

| Piece | Where |
|-------|-------|
| `buildings.footprint` (MultiPolygon) + `source` / `source_id` / `source_version` | migration `00025_building_footprints.sql` |
| GeoJSON ingest | `cmd/ingest-footprints` (CLI) → `store.UpsertBuildingFootprint` |
| Footprint vector tiles | `GET /api/v1/tiles/buildings/{z}/{x}/{y}?crisisId=` (`store.BuildingTileMVT`, layer `buildings`) |
| Dashboard overlay | `dashboard/components/SubmissionsMap.tsx` (authoritative layer above the basemap grid; selection prefers it) |
| Mobile overlay | `Mobile app/.../map/BeaconMap.kt` (`footprintTilesUrl` param; drawn over the offline basemap footprints; a tap returns the real `bid`) |

Building id scheme: `buildings.id = "<source>:<source_id>"` (e.g. `osm:way/12345`,
`google_microsoft_open_buildings:abc123`). `reports.building_id` references it, so a tap on an
authoritative footprint round-trips the real id into the report. The ingest is **idempotent**:
re-running refreshes geometry + provenance and preserves any crowd-derived `current_damage`.

## Ingesting an AOI

The backend runtime image is **distroless (no GDAL)**, so the server/CLI ingests **GeoJSON**.
Convert the upstream dataset **once** with GDAL/`ogr2ogr`, clipped to the crisis AOI, then ingest.

```sh
# 1a) Google–Microsoft Open Buildings (GeoParquet / FlatGeobuf, e.g. from HDX), clipped to an AOI
#     bbox order for -clipsrc is: xmin ymin xmax ymax  (lon/lat)
ogr2ogr -f GeoJSON hatay_ob.geojson open_buildings.fgb -clipsrc 36.05 36.10 36.30 36.30

# 1b) OpenStreetMap buildings (HOT Export Tool shapefile, or an osmium-extracted .osm/.pbf)
ogr2ogr -f GeoJSON hatay_osm.geojson hatay_osm_buildings.shp \
  -where "building IS NOT NULL" -nlt PROMOTE_TO_MULTI

# 1c) An official government building shapefile
ogr2ogr -f GeoJSON gov.geojson buildings.shp -t_srs EPSG:4326 -nlt PROMOTE_TO_MULTI

# 2) Ingest (DATABASE_URL points at the target DB; same default as the server)
go run ./cmd/ingest-footprints \
  -crisis crisis-antakya \
  -source osm \
  -source-version 2026-06 \
  -id-prop osm_id \
  -file hatay_osm.geojson
```

Flags: `-source` is the provenance label (`osm` | `google_microsoft_open_buildings` | `gov:<name>`);
`-id-prop` names the feature property holding the dataset id (omit to use the GeoJSON feature `id`);
`-crisis` scopes the rows (optional — the tile endpoint is bbox-filtered, so unscoped works too);
`-id-prefix` overrides the id prefix (defaults to `-source`). One invalid polygon is skipped via a
per-feature SAVEPOINT; the rest load.

## Online vs offline

- **Online (done, verified):** clients fetch `/tiles/buildings/...` MVT. The dashboard overlays the
  authoritative footprints above the basemap grid and snaps the selected-report highlight to them.
  The mobile capture map draws them over the offline basemap footprints; where they exist (and there
  is signal) a tap returns the real `bid`, otherwise it falls through to the basemap polygon.

- **Offline (NOT yet wired — needs on-device verification):** the mobile app is offline-first, and
  the authoritative MVT layer needs connectivity, so offline it renders nothing and taps fall back to
  the basemap footprints in the downloaded pack (this fallback is intentional and works today). To
  make **authoritative** footprints available offline, two options:
  1. **PMTiles** served to a `VectorSource(uri = "pmtiles://<bundled file>")`. MapLibre Native added
     PMTiles support, but whether the version bundled by maplibre-compose 0.13.0 exposes the
     `pmtiles://` protocol for a bundled file must be confirmed on a real device before relying on it.
  2. **Bundled GeoJSON** for the AOI loaded as a `GeoJsonSource` when offline (uses the same API the
     app already uses for report pins). Definitely works; the tradeoff is asset size for a large AOI.

  Recommendation: ship the verified online path now; pick (1) or (2) for offline after a quick device
  test, scoped to the demo AOI so the bundle stays small.
