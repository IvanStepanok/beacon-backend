# Beacon Backend

Stateless **Go** HTTP service over **PostgreSQL 16 + PostGIS**, serving one
camelCase JSON contract to both Beacon clients — the **mobile app** (Kotlin
Multiplatform) and the **analyst dashboard** (Next.js).

## Why this design wins (UNDP scoring)

- **One contract, two clients.** The `Report` JSON is a *superset*: nested objects
  (mobile) + flat aliases (dashboard) coexist, so a single document deserializes
  cleanly into both. No drift, no per-client API.
- **Offline-first, at-least-once safe.** `POST /api/v1/reports` is **idempotent**:
  the client-supplied id is the PK and re-submits `UPSERT` (no duplicates after a
  flaky network), with a second `idempotency_key` guard.
- **Server-authoritative per-building versioning.** On submit the server computes
  `version` and `supersedesReportId` inside a transaction (row-locked), so the
  damage history of a building is real, not fabricated.
- **Open standards & interoperability.** PostGIS geometry; export to **GeoJSON,
  HXL-tagged CSV, GeoPackage (pure-Go), KML and a PDNA Volume B pivot** — the
  in-app reporter export is a schema-aligned subset of the same GeoJSON/CSV
  vocabulary (same column names; the device file has no verification/admin
  columns and no HXL row).
- **Built to scale & not fall over.** pgxpool, SQL-side aggregations (the overview
  is one query, never "pull all rows"), GIST + composite btree indexes, keyset
  pagination, per-request timeouts, rate limiting, graceful shutdown, structured
  `slog`. Ships as a single static binary on a distroless image.
- **Privacy & governance.** Anonymous submitters, anonymization flags stored per
  report, a `report_verification_audit` trail for every analyst decision, and a
  locked-down **public projection** for low-trust reads (verified-only, coords
  coarsened to ~110 m, PII/operational fields stripped).

## Run locally

```bash
make db-up      # start Postgres+PostGIS (docker) on :5544
make run        # go run the server on :8080, auto-migrate + seed Antakya (56 reports)
# or everything in containers:
make compose-up # server + db
```

Then:

```bash
curl localhost:8080/healthz
curl 'localhost:8080/api/v1/stats/overview' | jq
curl 'localhost:8080/api/v1/reports?damage=complete&pageSize=5' | jq
curl 'localhost:8080/api/v1/reports/export?format=gpkg' -o reports.gpkg
curl 'localhost:8080/api/v1/reports/1156/photo' -o evidence.jpg   # seeded demo photo
```

The seeder embeds real, free-licensed ground photos of the 2023 Hatay
earthquakes (`internal/seed/photos/ATTRIBUTION.md`) and installs them into
`PHOTO_DIR`, so — on a fresh or reseeded database — every verified demo report
serves an actual image (the seeder only runs when the reports table is empty,
so it never retrofits photos onto an existing dataset). Seed timestamps are
relative to seed time (crisis started 72 h ago, reports spread across the
window), so the demo dataset never goes stale.

## Layout

```
cmd/server         entrypoint: config → migrate → pool → seed → router → graceful shutdown
internal/config    env-driven config
internal/db        pgxpool + goose (embedded migrations)
internal/migrations goose SQL migrations (PostGIS, versioning, RBAC, photo gate, form overrides)
internal/model     canonical JSON structs (superset contract)
internal/store     pgx SQL access (reports, stats, crisis, write/upsert, tx)
internal/service   validation, idempotent submit + versioning, stats, export
internal/handler   chi handlers (decode → service → encode)
internal/api       router + middleware (CORS, rate-limit, slog, recoverer, timeout)
internal/seed      deterministic Antakya seeder (parity with both clients) + embedded demo photos + golden tests
```

## Exports

`GET /api/v1/reports/export?format=` (analyst auth, crisis-scoped) — all live:

| Format | Output |
|---|---|
| `geojson` | FeatureCollection; the in-app export emits a schema-aligned subset (same property names, minus the verification/admin columns) |
| `csv` | HXL-tagged CSV (HDX-ready), includes `plus_code` + `admin*_shapeid` columns (geoBoundaries shape ids — not official OCHA P-codes; COD-AB P-code layer is roadmap) |
| `gpkg` | OGC GeoPackage (single SQLite file, pure-Go writer, no CGO) |
| `kml` | KML placemarks (gate fields + secondary impacts per placemark; opens in Google Earth) |
| `pdna` | PDNA-ready damage-count pivot: ADM2 × sector rows, count columns minimal/partial/complete (+ EMS-98 detail) — damage counts, not a loss/cost estimate |

Row formats carry both damage vocabularies: the raw grade and the required
3-tier rollup (`damage_tier` ∈ {minimal, partial, complete}).

## API (v1)

| Method | Path | Consumer |
|---|---|---|
| GET | `/healthz`, `/readyz` | infra |
| POST | `/api/v1/auth/login`, GET `/auth/me` | dashboard (JWT) |
| POST | `/api/v1/reports` | mobile (idempotent submit, `X-Device-Id` required; rate-limit + near-dup guards) |
| GET | `/api/v1/reports` | analyst (filter/search/paginate, crisis-scoped) |
| GET | `/api/v1/reports/{id}` | both (public sees verified-only, coarsened projection) |
| POST | `/api/v1/reports/{id}/photo` | mobile (anonymous upload, sniffed + ownership-bound) |
| GET | `/api/v1/reports/{id}/photo` | both (public: verified photos only) |
| PATCH | `/api/v1/reports/{id}/verification` | analyst; body `{status, note?, force?}` — verifying a photo-less report is 409 `photo_required` unless `force: true`; note/force land in the audit trail |
| PATCH | `/api/v1/reports/{id}/task` | analyst dispatch (status/assignee/severity/disposition/clusters, audited) |
| GET | `/api/v1/reports/latest-per-building` | mobile (map pins; `crisisId` or `bbox`) |
| GET | `/api/v1/reports/area-groups` | both |
| GET | `/api/v1/reports/export?format=geojson\|csv\|gpkg\|kml\|pdna` | analyst (see Exports) |
| GET | `/api/v1/buildings/{id}/timeline` | both (public: verified entries, notes stripped) |
| GET | `/api/v1/map/features?bbox=` | both |
| GET | `/api/v1/tiles/reports/{z}/{x}/{y}` | both (MVT: clusters at low zoom, points at high zoom) |
| GET | `/api/v1/config`, PATCH `/api/v1/config` | global capture-scale config (PATCH: analyst) |
| GET | `/api/v1/form-schema?crisisId=` | mobile (modular capture-form sections, resolved with crisis overrides) |
| GET | `/api/v1/stats/overview` | dashboard (analyst, scoped) |
| GET | `/api/v1/crises`, `/crises/{id}`, `/crises/active`, `/crises/near` | both |
| GET | `/api/v1/crises/{id}/danger-zones` | both |
| PATCH | `/api/v1/crises/{id}/status` | analyst (confirm/dismiss emergent crises) |
| PATCH | `/api/v1/crises/{id}/form` | senior analyst (per-crisis form-schema overrides) |
| POST | `/api/v1/feeds/refresh` | analyst (on-demand USGS/GDACS ingest) |
| GET | `/api/v1/profile` | mobile (points/badges are **server-derived** from verified reports) |
| POST | `/api/v1/profile/points` | **410 Gone** — retired self-award endpoint (anti-gaming) |

Reports carry `plusCode` as the canonical short location code; the legacy
`what3words` key is accepted on submit and emitted with the same value for
older clients. The authoritative surface is `internal/api/router.go`, documented
in [`openapi.yaml`](./openapi.yaml).

Config: see `.env.example`. In `ENV=prod` analyst mutations require a bearer token.
