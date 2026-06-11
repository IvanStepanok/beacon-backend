# Beacon — Scale Benchmark (500k reports)

The UNDP brief requires the system to support **50,000 (sub-national) → 250,000
(regional) → 500,000 (national)** reports per crisis, and to *"detail the
structure, scale, and architecture of your database"* (challenge brief) and
*"display how your solution can support hundreds of thousands of records"* (the
≤2-min video). This document records a measured benchmark at the **500k upper
bound** and the two scalability fixes it surfaced.

## Method

- **Data:** 500,000 synthetic reports generated into the real schema for one
  crisis (`crisis-antakya`), via `scripts/bench_seed.sql` — realistic admin
  cardinality (~20 ADM2 districts / ~200 ADM3 mahalle), points spread over a
  0.5°×0.5° box, balanced damage tiers / verification states, modular blobs.
- **Stack:** Go backend (single instance) + PostgreSQL 16 + PostGIS, local
  (Apple Silicon, Docker Postgres). End-to-end timings are over HTTP against the
  running server (query + serialization + transfer), best-of-5 warm.
- Seed load time for 500k rows (all indexes + GiST live): **~31 s**.

## Database footprint @ 500k

| | |
|---|---|
| Total relation size | **523 MB** (≈ 1.05 KB / report, excl. photo blobs) |
| Heap | 387 MB |
| All indexes | 136 MB |
| Largest indexes | GiST geom 27 MB · PK 26 MB · idempotency-key 26 MB · place trigram 16 MB |

Photos are stored on a separate object volume, not in the row — so the relational
footprint of a 500k national crisis is ~0.5 GB. 100 crises/year ≈ 50 GB of
relational data, trivially within a managed Postgres instance.

## Query latency @ 500k (analyst dashboard)

| Endpoint | Best of 5 |
|---|---|
| Reports list (keyset page of 50) | 374 ms |
| Full-text place search (trigram) | 511 ms |
| ADM2 P-code filter | **139 ms** |
| Damage filter | 303 ms |
| Area-groups aggregate | 404 ms |
| Stats overview (one SQL aggregate) | ~1.0 s |
| **Vector tiles (MVT), z8–z12** | **139–911 B, ~0.8 s** |
| Submit throughput (single instance) | **198 req/s**, p50 148 ms / p95 215 ms |

The list is keyset-paginated (`LIMIT n+1` on `idx_reports_crisis_captured`), so
page latency is independent of table size. Submit cost is dominated by the
per-insert spatial dedup lookup against the 500k GiST index; 198 req/s/instance
is ample (the durable client-side outbox absorbs bursts, POST is idempotent) and
scales horizontally — the API is stateless behind the Postgres primary. A global
per-IP rate limit (20 req/s, `RATE_LIMIT_RPS`) protects against abuse.

## Two scalability blockers found & fixed

The benchmark surfaced two places where a response was built fully in memory and
would OOM a memory-tight host at scale. Both are fixed.

### 1. Unbounded map-pin endpoints → bounded

`/map/features` (dashboard) and `/reports/latest-per-building` (mobile community
feed) serialized **every** matching report into a single JSON array. Reports
without a `building_id` never collapse via the latest-per-building window, so at
500k the responses were **89 MB** and **260 MB** respectively.

**Fix:** a `mapPinCap` (5,000, most-recent) on the latest-per-building query
(`store.LatestPerBuilding`). Result: **89 MB → 892 KB**, **260 MB → 5.2 MB**.
Full-density map rendering at scale is served by the clustered **MVT vector-tile**
endpoint (server-side clustering, 139–911 B/tile); the complete dataset by the
analyst export path.

### 2. In-memory export → streaming

The export path loaded the whole result set into a `[]Report` slice and built the
entire output as one `[]byte`. A single GeoJSON export of 500k peaked at **3.3 GB
RSS** — a guaranteed OOM on a ~256 MB-free host, despite "export hundreds of
thousands of records" being a must-have shown in the video.

**Fix:** the export endpoint now streams from a DB cursor (`store.ExportEach`)
straight to the HTTP response. Text formats (GeoJSON / CSV / KML) write row-by-row;
binary containers (GeoPackage / Shapefile) build to a temp file on disk in a single
pass, then stream out. Dynamic modular columns for CSV/GPKG come from a cheap
distinct-keys pre-pass (`store.ModularKeysRaw`), so no row buffering is needed.

| Format | Time @ 500k | Output size | Peak server RSS (all 5, back-to-back) |
|---|---|---|---|
| GeoJSON | ~10 s | 409 MB | |
| CSV (HXL) | ~4 s | 100 MB | |
| KML | ~6 s | 236 MB | **35 MB total** |
| GeoPackage | ~6 s | 93 MB | (was 3.3 GB for one GeoJSON) |
| Shapefile (zip) | ~4 s | 4.5 MB | |

**~95× peak-memory reduction.** All five outputs validated at 500k (502,064
features/rows/records; valid JSON, SQLite GPKG with correct bbox, shapefile .shx
record count). Output is bounded by client download speed, not server RAM.

## Reproduce

```sh
# 1. seed (local PostGIS on :5544)
docker cp scripts/bench_seed.sql <db-container>:/tmp/bench_seed.sql
docker exec -e PGPASSWORD=beacon <db-container> psql -U beacon -d beacon -v n=500000 -f /tmp/bench_seed.sql
# 2. run the server, then hit /api/v1/reports/export?format=… and /api/v1/tiles/reports/{z}/{x}/{y}
# reset: DELETE FROM reports WHERE id LIKE 'bench-%' OR id LIKE 'loadtest%';
```
