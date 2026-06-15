package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// These tests lock the crisis-lifecycle redesign's core invariant — a crisis is an
// EVENT, not a single pin. They run against the dev DB (skip when none) and use open-
// ocean points (no admin boundary loaded in tests → emergent falls back to pure radius,
// exactly the path under test). Fixtures are isolated + removed in t.Cleanup.

// cleanupEmergentNear removes any emergent crisis (and its reports/buildings) whose
// centre is within 10 km of the point — registered FIRST so it runs LAST (after the
// per-submitter cleanups have already removed the reports).
func cleanupEmergentNear(t *testing.T, pool *pgxpool.Pool, lat, lng float64) {
	t.Helper()
	t.Cleanup(func() {
		ctx := context.Background()
		rows, err := pool.Query(ctx,
			"SELECT id FROM crises WHERE source = 'emergent' AND ST_DWithin(geom::geography, ST_SetSRID(ST_MakePoint($2,$1),4326)::geography, 10000)",
			lat, lng)
		if err != nil {
			return
		}
		var ids []string
		for rows.Next() {
			var id string
			if rows.Scan(&id) == nil {
				ids = append(ids, id)
			}
		}
		rows.Close()
		for _, id := range ids {
			_, _ = pool.Exec(ctx, "DELETE FROM reports WHERE crisis_id = $1", id)
			_, _ = pool.Exec(ctx, "DELETE FROM buildings WHERE crisis_id = $1", id)
			_, _ = pool.Exec(ctx, "DELETE FROM crises WHERE id = $1", id)
		}
	})
}

// emergentCrisisNear returns the most-recent emergent crisis within 10 km of the point
// (id "" when none), plus its status, source and live distinct-submitter count.
func emergentCrisisNear(t *testing.T, pool *pgxpool.Pool, lat, lng float64) (id, status, source string, distinct int) {
	t.Helper()
	err := pool.QueryRow(context.Background(),
		`SELECT c.id, c.status, c.source,
		        (SELECT count(DISTINCT r.submitter_id) FROM reports r WHERE r.crisis_id = c.id)
		   FROM crises c
		  WHERE c.source = 'emergent'
		    AND ST_DWithin(c.geom::geography, ST_SetSRID(ST_MakePoint($2,$1),4326)::geography, 10000)
		  ORDER BY c.started_at DESC LIMIT 1`, lat, lng).Scan(&id, &status, &source, &distinct)
	if err != nil {
		if err == pgx.ErrNoRows {
			return "", "", "", 0
		}
		t.Fatalf("emergentCrisisNear query: %v", err)
	}
	return id, status, source, distinct
}

// TestEmergent_SingleSubmitterNeverFormsCrisis is the direct guard for the bug this
// redesign fixed: repeated reports from ONE device must never propose a crisis, because
// the threshold counts DISTINCT submitters, not rows. (The two pins are ~165 m apart so
// they clear the 25 m near-dup guard and both insert.)
func TestEmergent_SingleSubmitterNeverFormsCrisis(t *testing.T) {
	pool := testDB(t)
	svc, _, crises := testService(pool)
	lat, lng := 12.5, -45.0 // open Atlantic, no admin boundary in the test DB
	cleanupEmergentNear(t, pool, lat, lng)
	sid := newTestSubmitter(t, pool, crises)
	ctx := context.Background()
	now := time.Now().UTC()

	for i, off := range []float64{0.0, 0.0015} {
		id := fmt.Sprintf("emg-solo-%d-%d", time.Now().UnixNano(), i)
		if _, created, err := svc.Submit(ctx, submitReq(id, "", lat+off, lng, now), &sid); err != nil || !created {
			t.Fatalf("submit %d: created=%v err=%v", i, created, err)
		}
	}

	if id, status, _, _ := emergentCrisisNear(t, pool, lat, lng); id != "" {
		t.Fatalf("a single submitter formed an emergent crisis %s (status=%s) — one device must never be a crisis", id, status)
	}
}

// TestEmergent_DistinctSubmittersFormProposedNotActive locks the rest of the model:
// THREE distinct submitters clustered within 2 km / 24 h DO propose a crisis — born
// 'proposed' (NEVER auto-activated) with source 'emergent' — and all three pending
// reports are pulled into it.
func TestEmergent_DistinctSubmittersFormProposedNotActive(t *testing.T) {
	pool := testDB(t)
	svc, _, crises := testService(pool)
	lat, lng := 13.7, -47.3
	cleanupEmergentNear(t, pool, lat, lng)
	ctx := context.Background()
	now := time.Now().UTC()

	for i, off := range []float64{0.0, 0.002, 0.004} {
		sid := newTestSubmitter(t, pool, crises) // a distinct device per report
		id := fmt.Sprintf("emg-multi-%d-%d", time.Now().UnixNano(), i)
		if _, created, err := svc.Submit(ctx, submitReq(id, "", lat+off, lng+off, now), &sid); err != nil || !created {
			t.Fatalf("submit %d: created=%v err=%v", i, created, err)
		}
	}

	id, status, source, distinct := emergentCrisisNear(t, pool, lat, lng)
	if id == "" {
		t.Fatalf("3 distinct submitters in a 2 km / 24 h cluster must propose an emergent crisis, got none")
	}
	if status != "proposed" {
		t.Errorf("emergent crisis status = %q, want \"proposed\" (must NOT auto-activate)", status)
	}
	if source != "emergent" {
		t.Errorf("emergent crisis source = %q, want \"emergent\"", source)
	}
	if distinct < 3 {
		t.Errorf("distinct submitters on the crisis = %d, want >= 3", distinct)
	}

	var attached int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM reports WHERE crisis_id = $1", id).Scan(&attached); err != nil {
		t.Fatalf("count attached reports: %v", err)
	}
	if attached < 3 {
		t.Errorf("reports attached to the emergent crisis = %d, want >= 3 (gate circle == pull-in circle)", attached)
	}
}
