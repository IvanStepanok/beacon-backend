# Crisis lifecycle & emergent detection

How a "crisis" comes into existence, how citizen reports attach to it, and what the
map shows. This is the design that replaced the original behaviour where **a single
citizen pin instantly surfaced an "active crisis"** — which was wrong both against the
challenge requirements (community reports are an *early-signal / verification layer*
feeding an event, not a declaration of one) and against every competitor (RAPIDA,
CrisisMapper, Verified Crisis Mapper, CrisisMap, UN-ASIGN all declare a crisis
admin-side or from an authoritative feed; none let one report conjure one).

## The model: a crisis is an EVENT, reports are observations inside it

A **crisis** is a discrete event with a name, an admin-area scope, a time window and a
lifecycle. **Reports** are observations *inside* an event; they attach to it — they do
not, on their own, create one. Within a crisis, data rolls up:

```
Report  →  Building (versioned, latest-wins)  →  H3 hotspot cell (heatmap)  →  Crisis (admin-area)
```

### Two ways a crisis is born

| Source | `source` | Born as | Becomes public when |
| --- | --- | --- | --- |
| Analyst-declared crisis (extent, time window, optional GLIDE id) | `analyst` | `active` immediately | — already active |
| **Emergent** — auto-detected from a cluster of citizen reports | `emergent` | **`proposed`** | an analyst **Confirms** it on the dashboard |

A `proposed` crisis is **never** shown to the public and is **not** in the default
stats/map/export scope. It lives only in the analyst **"Needs review"** queue until a
human confirms (→ `active`) or dismisses it (→ reports released back to pending).

### State machine

```
                 analyst Confirm                 analyst
   proposed ───────────────────────▶ active ───────────────▶ closed
      │                                  ▲
      │ analyst Dismiss                  │ (feed/analyst declared crises
      ▼  (reports → pending)             │  start here directly)
  dismissed                          [seed/feed]
```

There is **no auto-activation**. (The previous `ActivateIfProposed`, which flipped a
proposed crisis to active the instant one report was assigned to it, was removed — it
was the root cause of "one pin = active crisis".) A new report may still *attach* to a
`proposed` crisis (so the cluster keeps growing and corroboration accumulates for the
analyst), but attaching never changes the crisis's status.

## Emergent formation rules

When a report matches no existing crisis (`crisis_id` stays NULL) and has a resolved
point, the server checks whether a cluster has formed
(`store.DetectEmergentCrisis`). A `proposed` crisis is created **only when all** of:

- **≥ N DISTINCT submitters** (`BEACON_EMERGENT_MIN_REPORTS`, default **3**) —
  counted by `count(DISTINCT submitter_id)`, **not** raw rows. Three reports from one
  device can never propose a crisis. (Anonymous reports with no device id —
  `submitter_id IS NULL` — do not count toward the distinct gate; they are
  un-attributable. The app always sends an `X-Device-Id`, so this is an edge case.)
- within **`BEACON_EMERGENT_RADIUS_KM`** (default **2.0 km**) of the report,
- over the last **`BEACON_EMERGENT_WINDOW_HRS`** (default **24 h**),
- **within ONE admin area** — the cluster is constrained to the report's `adm2_pcode`
  (preferred) or `adm1_pcode` so a 2 km circle can't merge two districts into one
  event. If the point falls outside all known boundaries (`ResolveAdmin` → nil), it
  falls back to the pure-radius behaviour.

The created crisis is stamped with its admin scope (`crises.admin_pcode`) and the
**effective thresholds that formed it** (`emergent_radius_km`, `emergent_window_hrs`,
`emergent_min_reports`) for provenance and future per-crisis tuning. Its title/area
come from the centroid's admin-area name (`ResolveAdmin`) — **never** a report's
free-text `place` (so client placeholders like "Your location" can't leak into a
crisis title); the fallback is the centroid coordinates.

### Configuring the thresholds

Deployment-global, env-overridable (validated at boot; `MIN_REPORTS` must be ≥ 2):

```
BEACON_EMERGENT_RADIUS_KM=2.0
BEACON_EMERGENT_WINDOW_HRS=24
BEACON_EMERGENT_MIN_REPORTS=3
```

**Why global, not per-crisis, for formation:** a cluster that is *forming* has no
crisis row yet, so there is nothing to read a per-crisis value from. The thresholds are
therefore a deployment/region default. The effective values are then persisted on the
crisis row (`crises.emergent_*`), which is the hook for future per-crisis tuning of an
*existing* crisis (analogous to the existing per-crisis `form_overrides`).

## What the map shows — three states (mobile)

The mobile launch resolves the user's location, calls `/crises/near`, and picks a
state from the **covering** crisis's `status`:

| State | Condition | Banner |
| --- | --- | --- |
| `IN_CRISIS` | covering crisis with `status == "active"` | **red** "Active crisis · {title}" (alarm) |
| `EMERGING` | covering crisis with `status == "proposed"` | **amber** "Reports coming in · Awaiting verification" (soft) |
| `NO_CRISIS` | no covering crisis | neutral card; reports shown as pins |

`active` wins over `proposed` when both cover the user. A single own report with no
cluster lands in `NO_CRISIS` (its own pin is still visible) — it never triggers a
banner. New strings `map_crisis_emerging` / `map_crisis_emerging_sub` are translated in
all six UN locales (incl. Arabic RTL).

**EMERGING map data is deliberately viewport-scoped, not crisis-scoped.** A fresh
`proposed` cluster holds only *pending* reports, and the public/anonymous tier serves
**verified-only** data — so scoping the map to the proposed crisis id would render an
empty map under the banner. Instead the EMERGING state scopes pins to the viewport
(like `NO_CRISIS`): the contributor still sees their own reports (always merged from
local state, pre-sync) plus any verified reports nearby, while the amber "awaiting
verification" banner honestly conveys that the cluster itself is unconfirmed. We do
**not** relax verified-only for the public tier — broadcasting unverified citizen
reports would be a privacy/trust regression.

## Dashboard

- **Needs review** queue = all `proposed` crises. Each card surfaces the
  **distinct-submitter count** (`distinctSubmitters`, the corroboration/anti-spam
  signal — 5 reports from 5 devices ≫ 5 from 1) alongside report count, age and a
  damage-tier mini-breakdown.
- **Confirm & publish** (→ `active`) is the **only** path from proposed to active;
  Dismiss releases the cluster's reports back to pending.
- The analyst map's "busiest crisis" auto-default is restricted to `status==='active'`,
  so an unconfirmed cluster can't silently become the default scope — but proposed
  crises remain manually selectable for inspection.
- The **public / community** view resolves `GET /crises/active` and shows verified-only,
  coarsened aggregates — a `proposed` crisis can never reach the anonymous tier.

## H3 hotspots & interoperability

Each resolved report carries an **H3 resolution-8 cell** (`reports.h3_r8`, ≈ 0.74 km²
hexagon), computed in Go (`uber/h3-go` v4) on every insert path and backfilled at
startup for pre-existing rows. H3 was chosen over the Postgres `h3`/`h3_postgis`
extension because the deployment Postgres is not assumed to allow `CREATE EXTENSION`
and the project also targets a non-Postgres backend; a single stored text id is
portable and feeds both aggregation and export consistently.

- **`GET /reports/area-groups?grid=h3`** returns the hexagonal hotspot view (cell id +
  report centroid + representative place label + count + worst tier). Without `grid=h3`
  the endpoint keeps the legacy free-text `place` grouping for the textual ranking — both
  views stay available, so existing clients are unaffected.
- **`h3id`** (HXL `#geo+h3`) is now a column in every export format (CSV / GeoJSON /
  GPKG), the native RAPIDA / GeoHub interoperability key.
- Resolution 8 is a single constant (`store.h3Resolution`) shared by insert,
  aggregation and export so they always agree; per-crisis grain is a documented future
  knob.

## Migrations

- **00021_crisis_scope_thresholds** — `crises.admin_pcode` (FK → `admin_areas`),
  `crises.emergent_radius_km/window_hrs/min_reports`, index on `admin_pcode`.
- **00022_reports_h3** — `reports.h3_r8` + **partial** index
  `(crisis_id, h3_r8) WHERE h3_r8 IS NOT NULL` (matches the only reader, skips the
  location-unresolved NULL rows). The startup `BackfillH3R8` fills the column for rows
  that predate it.
- **00023_reports_crisis_submitter_idx** — `(crisis_id, submitter_id)` so the
  `count(DISTINCT submitter_id)` per-crisis subquery (the `distinctSubmitters` signal on
  every crisis response) is index-served, not a full per-crisis heap scan at 500k scale.

## Known limitations & decisions

- **Sybil resistance:** `X-Device-Id` is a client-generated UUID with no attestation, so
  a determined actor can reinstall / reset prefs to mint new submitter ids and inflate
  the distinct count. The 25 m / 10 min near-duplicate guard and analyst confirmation are
  the mitigations; stronger attestation (e.g. optional phone/community verification, the
  endorsed "trusted contributor" tier) is future work.
- **Cross-boundary clusters:** a disaster straddling two ADM2 districts forms **two**
  proposed crises (one per district). This is intentional — it keeps each event cleanly
  attributable to one admin area for routing/export — and an analyst can keep, merge or
  dismiss them at review.
- **`distinctSubmitters` subquery** runs on every crisis list/near/active query. Crisis
  rows are few (not report-scale), and `reports.crisis_id` is indexed, so this mirrors
  the existing `report_count` subquery cost; revisit only if crisis cardinality grows.
- **Backfill at scale:** `BackfillH3R8` runs in a **background goroutine** at startup
  (never blocks readiness) and works in bounded chunks (2 000-row `LIMIT` SELECT → one
  set-based `UPDATE … FROM unnest(...)` per chunk), so memory stays at one chunk. It is
  idempotent and resumable — a transient failure just retries on the next boot. On a
  fresh deploy the reseed already stamps H3 via `UpsertReport`, so the backfill only
  touches non-reseeded legacy rows.
- **Emergent gate vs pull-in:** the formation gate (distinct-submitter count) and the
  report pull-in use the **same circle** — centred on the triggering pin, same radius,
  same admin scope — so exactly the gated reports are attached (the stored crisis
  centre/geom is the cluster centroid, for display only).
