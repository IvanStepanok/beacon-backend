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
- **Open standards & interoperability.** PostGIS geometry; export to **GeoJSON**
  and **CSV** byte-compatible with the in-app export (GPKG on the roadmap).
- **Built to scale & not fall over.** pgxpool, SQL-side aggregations (the overview
  is one query, never "pull all rows"), GIST + composite btree indexes, keyset
  pagination, per-request timeouts, rate limiting, graceful shutdown, structured
  `slog`. Ships as a single static binary on a distroless image.
- **Privacy & governance.** Anonymous submitters, anonymization flags stored per
  report, and a `report_verification_audit` trail for every analyst decision.

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
curl 'localhost:8080/api/v1/reports/export?format=geojson' -o reports.geojson
```

## Layout

```
cmd/server         entrypoint: config → migrate → pool → seed → router → graceful shutdown
internal/config    env-driven config
internal/db        pgxpool + goose (embedded migrations)
internal/migrations 00001_init.sql (PostGIS, versioning, indexes)
internal/model     canonical JSON structs (superset contract)
internal/store     pgx SQL access (reports, stats, crisis, write/upsert, tx)
internal/service   validation, idempotent submit + versioning, stats, export
internal/handler   chi handlers (decode → service → encode)
internal/api       router + middleware (CORS, rate-limit, slog, recoverer, timeout)
internal/seed      deterministic Antakya seeder (parity with both clients) + golden test
```

## API (v1)

| Method | Path | Consumer |
|---|---|---|
| GET | `/healthz`, `/readyz` | infra |
| POST | `/api/v1/reports` | mobile (idempotent submit) |
| GET | `/api/v1/reports` | both (filter/search/paginate) |
| GET | `/api/v1/reports/{id}` | both |
| PATCH | `/api/v1/reports/{id}/verification` | dashboard (analyst) |
| GET | `/api/v1/reports/latest-per-building` | mobile (map pins) |
| GET | `/api/v1/reports/area-groups` | both |
| GET | `/api/v1/reports/export?format=geojson\|csv` | both |
| GET | `/api/v1/buildings/{id}/timeline` | both |
| GET | `/api/v1/map/features?bbox=` | both |
| GET | `/api/v1/stats/overview` | dashboard |
| GET | `/api/v1/crises`, `/crises/{id}`, `/crises/active` | both |
| GET | `/api/v1/crises/{id}/danger-zones` | both |
| GET | `/api/v1/profile` · POST `/api/v1/profile/points` | mobile |

Config: see `.env.example`. In `ENV=prod` analyst mutations require a bearer token.
