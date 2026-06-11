-- Scale benchmark seed: generate N synthetic reports for crisis-antakya.
-- Realistic admin cardinality (~20 adm2 districts, ~200 adm3) so area-groups,
-- pcode filters and trigram place search are exercised, not trivially small.
-- Run:  psql ... -v n=500000 -f bench_seed.sql
\set n :n

INSERT INTO reports (
  id, idempotency_key, crisis_id, submitter_id,
  damage, verification, debris, infra_types, crisis_nature,
  geom, lat, lng, gps_accuracy_m,
  version, plus_code, place,
  desc_original, desc_original_lang,
  ai_level, ai_confidence,
  photos, size_bytes, modular, anonymization,
  is_mine, synced, captured_at, created_at, updated_at,
  admin, location_resolved
)
SELECT
  'bench-' || g                                   AS id,
  'bench-' || g                                   AS idempotency_key,
  'crisis-antakya'                                AS crisis_id,
  (ARRAY[
     '7373ed1c-aa0c-408c-82b1-4b3c44a46d63'::uuid,
     'a4fd5b66-5dab-4a04-8c93-3a2f19207168'::uuid,
     '22b9f002-0914-4084-bbaa-bd2e84ab6e35'::uuid
   ])[1 + (g % 3)]                                AS submitter_id,
  (ARRAY['minimal','partial','complete'])[1 + (g % 3)]                 AS damage,
  (ARRAY['pending','verified','verified','flagged'])[1 + (g % 4)]      AS verification,
  (ARRAY['yes','no','unsure'])[1 + (g % 3)]                            AS debris,
  ARRAY[(ARRAY['residential','commercial','government','utility',
               'transport','community','public','other'])[1 + (g % 8)]] AS infra_types,
  ARRAY[(ARRAY['earthquake','flood','tsunami','hurricane','wildfire',
               'explosion','chemical','conflict','civil_unrest'])[1 + (g % 9)]] AS crisis_nature,
  ST_SetSRID(ST_MakePoint(lng, lat), 4326)        AS geom,
  lat, lng,
  5 + (g % 20)                                    AS gps_accuracy_m,
  1                                               AS version,
  NULL                                            AS plus_code,
  'Mahalle ' || (g % 200)                         AS place,
  'Synthetic benchmark report ' || g              AS desc_original,
  'en'                                            AS desc_original_lang,
  CASE WHEN g % 4 = 0 THEN (ARRAY['minimal','partial','complete'])[1 + (g % 3)] END AS ai_level,
  CASE WHEN g % 4 = 0 THEN (55 + (g % 45))::smallint END               AS ai_confidence,
  '[]'::jsonb                                     AS photos,
  150000 + (g % 1850000)                          AS size_bytes,
  NULL                                            AS modular,
  '{"anonymous": true, "exifStripped": true, "facesBlurred": true, "platesBlurred": true}'::jsonb AS anonymization,
  false                                           AS is_mine,
  true                                            AS synced,
  now() - ((g % 2160) || ' hours')::interval      AS captured_at,
  now() - ((g % 2160) || ' hours')::interval      AS created_at,
  now() - ((g % 2160) || ' hours')::interval      AS updated_at,
  jsonb_build_object(
    'adm0', jsonb_build_object('name','Turkey','pcode','TUR'),
    'adm1', jsonb_build_object('name','Hatay','pcode','TUR031'),
    'adm2', jsonb_build_object('name','District ' || (g % 20),
                               'pcode','TUR0310' || lpad((g % 20)::text, 2, '0')),
    'adm3', jsonb_build_object('name','Mahalle ' || (g % 200),
                               'pcode','TUR0310' || lpad((g % 20)::text, 2, '0') || lpad((g % 10)::text, 3, '0'))
  )                                               AS admin,
  true                                            AS location_resolved
FROM generate_series(1, :n) AS g,
LATERAL (SELECT 36.0 + random()*0.5 AS lat, 36.0 + random()*0.5 AS lng) coords
ON CONFLICT (id) DO NOTHING;
