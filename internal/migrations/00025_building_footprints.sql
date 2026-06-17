-- +goose Up
-- +goose StatementBegin
-- Authoritative building FOOTPRINTS for the "tap the building" flow. Until now the
-- buildings table held only a centroid Point, and building ids were minted client-side
-- from the basemap's OpenStreetMap "building" vector layer (coverage = OSM coverage, no
-- stable provenance, ids = a hash of the polygon ring). These columns let an AOI be
-- pre-loaded with authoritative footprint polygons — OpenStreetMap, the Google-Microsoft
-- Open Buildings dataset, or an official government shapefile — each carrying its real
-- source id, so a report anchors to a known building with provenance and the map can
-- overlay the actual footprint geometry (must-have M2/M9: "overlaid building footprint
-- shapefiles where available"). Client-tapped basemap buildings keep working unchanged
-- (footprint/source stay NULL); ingested rows carry the real polygon + provenance.
ALTER TABLE buildings ADD COLUMN footprint      geometry(MultiPolygon,4326);
ALTER TABLE buildings ADD COLUMN source         text;   -- 'osm' | 'google_microsoft_open_buildings' | 'gov:<name>' | NULL (client tap)
ALTER TABLE buildings ADD COLUMN source_id      text;   -- the dataset's own id (OSM way/relation id, Open Buildings id, gov reference)
ALTER TABLE buildings ADD COLUMN source_version text;   -- dataset release / snapshot date (provenance)
-- Footprints are served as MVT tiles (BuildingTileMVT) filtered by crisis + tile
-- envelope, so a GiST index on the polygon plus a crisis index power the tile query.
-- Partial (footprint IS NOT NULL): client-tapped rows have no polygon and never match.
CREATE INDEX IF NOT EXISTS idx_buildings_footprint        ON buildings USING gist (footprint) WHERE footprint IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_buildings_crisis_footprint ON buildings (crisis_id)           WHERE footprint IS NOT NULL;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_buildings_crisis_footprint;
DROP INDEX IF EXISTS idx_buildings_footprint;
ALTER TABLE buildings DROP COLUMN IF EXISTS source_version;
ALTER TABLE buildings DROP COLUMN IF EXISTS source_id;
ALTER TABLE buildings DROP COLUMN IF EXISTS source;
ALTER TABLE buildings DROP COLUMN IF EXISTS footprint;
-- +goose StatementEnd
