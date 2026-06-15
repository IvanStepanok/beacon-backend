package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stepanok/beacon-server/internal/db"
	"github.com/stepanok/beacon-server/internal/model"
	"github.com/stepanok/beacon-server/internal/store"
	"github.com/stepanok/beacon-server/internal/translate"
)

// ── integration harness ─────────────────────────────────────────────────
//
// These tests exercise the DB-backed submit invariants (idempotent replay,
// per-building version chain, near-dup guard, verification photo gate) against
// the local dev database (`make db-up`). When no DB is reachable they SKIP, so
// `go test ./...` stays green either way. Every fixture is isolated (unique
// ids, a throwaway crisis in the South Atlantic) and removed in t.Cleanup.

// testDB connects to the dev database and ensures migrations are applied.
// Skips the calling test when the DB is unreachable.
func testDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		url = "postgres://beacon:beacon@localhost:5544/beacon?sslmode=disable"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pool, err := db.NewPool(ctx, url, 4)
	if err != nil {
		t.Skipf("no test database (make db-up to enable): %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Skipf("no test database (make db-up to enable): %v", err)
	}
	if err := db.Migrate(url); err != nil {
		pool.Close()
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// testService wires a ReportService exactly like main.go, minus translation
// (disabled) and boundary loading (nil) — neither affects these invariants.
func testService(pool *pgxpool.Pool) (*ReportService, *store.Reports, *store.Crises) {
	reports := store.NewReports(pool)
	crises := store.NewCrises(pool)
	svc := NewReportService(pool, reports, store.NewAdmin(pool), crises, translate.New("", "en"), nil, store.DefaultEmergentConfig())
	return svc, reports, crises
}

// newTestCrisis inserts an isolated active crisis far from any real/seeded data
// (mid-South-Atlantic) so spatial queries never touch the demo dataset, and
// removes every report/building/crisis row it spawned on cleanup.
func newTestCrisis(t *testing.T, pool *pgxpool.Pool, crises *store.Crises) (id string, lat, lng float64) {
	t.Helper()
	ctx := context.Background()
	id = fmt.Sprintf("crisis-test-%d", time.Now().UnixNano())
	lat, lng = -34.83, -16.21
	if err := crises.UpsertCrisis(ctx, model.Crisis{
		ID: id, Title: "Integration-test crisis", Area: "South Atlantic",
		Nature: "earthquake", CenterLat: lat, CenterLng: lng,
		Source: "test", StartedAt: time.Now().UTC().Add(-2 * time.Hour),
		RadiusKm: 10, Status: "active",
	}); err != nil {
		t.Fatalf("upsert test crisis: %v", err)
	}
	t.Cleanup(func() {
		// reports first (verification audit cascades), then their buildings, then the crisis.
		_, _ = pool.Exec(ctx, "DELETE FROM reports WHERE crisis_id = $1", id)
		_, _ = pool.Exec(ctx, "DELETE FROM buildings WHERE crisis_id = $1", id)
		_, _ = pool.Exec(ctx, "DELETE FROM crises WHERE id = $1", id)
	})
	return id, lat, lng
}

// newTestSubmitter resolves a fresh anonymous device to its submitter UUID.
// Cleanup deletes the submitter's reports first, so it is order-independent
// with the crisis cleanup.
func newTestSubmitter(t *testing.T, pool *pgxpool.Pool, crises *store.Crises) string {
	t.Helper()
	ctx := context.Background()
	sid, err := crises.ResolveSubmitterID(ctx, fmt.Sprintf("test-device-%d", time.Now().UnixNano()))
	if err != nil {
		t.Fatalf("resolve submitter: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM reports WHERE submitter_id = $1::uuid", sid)
		_, _ = pool.Exec(ctx, "DELETE FROM submitters WHERE id = $1::uuid", sid)
	})
	return sid
}

// submitReq builds a minimal valid pinned submission at the given point.
func submitReq(id, crisisID string, lat, lng float64, capturedAt time.Time) model.SubmitReportRequest {
	return model.SubmitReportRequest{
		ID: id, CrisisID: crisisID, Damage: "partial",
		InfraTypes: []string{"residential"}, CrisisNature: []string{"earthquake"},
		Lat: f64(lat), Lng: f64(lng), CapturedAt: &capturedAt,
	}
}

// TestSubmit_IdempotentReplay locks the at-least-once contract: re-POSTing the
// same report (same id — an offline client flushing its queue) inserts nothing,
// returns created=false, and never trips the anti-abuse guards against its own
// earlier row; a NEW id carrying an already-used idempotency key resolves to
// the existing report the same way.
func TestSubmit_IdempotentReplay(t *testing.T) {
	pool := testDB(t)
	svc, _, crises := testService(pool)
	cid, lat, lng := newTestCrisis(t, pool, crises)
	sid := newTestSubmitter(t, pool, crises)
	ctx := context.Background()

	id := fmt.Sprintf("t-idem-%d", time.Now().UnixNano())
	req := submitReq(id, cid, lat, lng, time.Now().UTC())

	first, created, err := svc.Submit(ctx, req, &sid)
	if err != nil || !created {
		t.Fatalf("first submit: created=%v err=%v", created, err)
	}
	if first.Version != 1 {
		t.Errorf("first submit version = %d, want 1", first.Version)
	}

	// Replay with the SAME id (and same submitter): no new row, no guard trip.
	replay, created, err := svc.Submit(ctx, req, &sid)
	if err != nil {
		t.Fatalf("replay submit: %v", err)
	}
	if created {
		t.Errorf("replay must return created=false")
	}
	if replay.ID != first.ID {
		t.Errorf("replay returned %s, want %s", replay.ID, first.ID)
	}
	var n int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM reports WHERE id = $1", id).Scan(&n); err != nil || n != 1 {
		t.Errorf("rows for %s = %d (err=%v), want exactly 1", id, n, err)
	}

	// A NEW id reusing the stored idempotency key also resolves to the original.
	req2 := submitReq(id+"-retry", cid, lat, lng, time.Now().UTC())
	req2.IdempotencyKey = "idem-" + id
	stored, created, err := svc.Submit(ctx, req2, nil)
	if err != nil {
		t.Fatalf("idempotency-key replay: %v", err)
	}
	if created || stored.ID != id {
		t.Errorf("idempotency-key replay: created=%v id=%s, want false/%s", created, stored.ID, id)
	}
}

// TestSubmit_BuildingVersionChain locks the server-authoritative per-building
// history: three submissions against the same tapped footprint get versions
// 1→2→3, each superseding the previous newest report, inside a row-locked tx.
func TestSubmit_BuildingVersionChain(t *testing.T) {
	pool := testDB(t)
	svc, _, crises := testService(pool)
	cid, lat, lng := newTestCrisis(t, pool, crises)
	ctx := context.Background()

	bid := fmt.Sprintf("fp-test-%d", time.Now().UnixNano())
	fp := "footprint"
	base := time.Now().UTC()
	ids := make([]string, 3)
	for i := range ids {
		ids[i] = fmt.Sprintf("%s-v%d", bid, i+1)
		req := submitReq(ids[i], cid, lat, lng, base.Add(time.Duration(i-3)*10*time.Minute))
		req.BuildingID = &bid
		req.BuildingSource = &fp

		stored, created, err := svc.Submit(ctx, req, nil)
		if err != nil || !created {
			t.Fatalf("submit v%d: created=%v err=%v", i+1, created, err)
		}
		if stored.Version != i+1 {
			t.Errorf("v%d: version = %d, want %d", i+1, stored.Version, i+1)
		}
		switch {
		case i == 0 && stored.SupersedesReportID != nil:
			t.Errorf("v1 must supersede nothing, got %v", *stored.SupersedesReportID)
		case i > 0 && (stored.SupersedesReportID == nil || *stored.SupersedesReportID != ids[i-1]):
			t.Errorf("v%d supersedes %v, want %s", i+1, stored.SupersedesReportID, ids[i-1])
		}
	}

	// The building's cached damage reflects the newest version's grade.
	var current string
	if err := pool.QueryRow(ctx, "SELECT current_damage FROM buildings WHERE id = $1", bid).Scan(&current); err != nil {
		t.Fatalf("building row: %v", err)
	}
	if current != "partial" {
		t.Errorf("building current_damage = %q, want partial", current)
	}
}

// TestSubmit_NearDupGuard locks the anti-abuse dedup: a second pin from the
// SAME submitter within 25 m / 10 min is a 409 DuplicateError referencing the
// original — including a synthetic GPS-grid "b-" building id, which is derived
// from the pin and must NOT bypass the guard. Only a REAL tapped footprint
// (buildingSource=footprint) is exempt (the version chain owns those).
func TestSubmit_NearDupGuard(t *testing.T) {
	pool := testDB(t)
	svc, _, crises := testService(pool)
	cid, lat, lng := newTestCrisis(t, pool, crises)
	sid := newTestSubmitter(t, pool, crises)
	ctx := context.Background()

	now := time.Now().UTC()
	origID := fmt.Sprintf("t-dup-%d", time.Now().UnixNano())
	if _, created, err := svc.Submit(ctx, submitReq(origID, cid, lat, lng, now), &sid); err != nil || !created {
		t.Fatalf("original submit: created=%v err=%v", created, err)
	}

	// ~5 m away, moments later, no building: duplicate.
	var dup DuplicateError
	_, _, err := svc.Submit(ctx, submitReq(origID+"-again", cid, lat+0.00005, lng, now), &sid)
	if !errors.As(err, &dup) {
		t.Fatalf("near-dup pin: err = %v, want DuplicateError", err)
	}
	if dup.ExistingID != origID {
		t.Errorf("duplicate references %s, want %s", dup.ExistingID, origID)
	}

	// A synthetic GPS-grid "b-" id is no exemption: still a duplicate.
	gridReq := submitReq(origID+"-grid", cid, lat, lng, now)
	grid := fmt.Sprintf("b-test-%d", time.Now().UnixNano())
	gridReq.BuildingID = &grid
	if _, _, err := svc.Submit(ctx, gridReq, &sid); !errors.As(err, &dup) {
		t.Errorf("b- grid pin: err = %v, want DuplicateError (must not bypass the guard)", err)
	}

	// A REAL tapped footprint at the same spot is exempt — the version chain
	// supersedes it instead of rejecting it.
	fpReq := submitReq(origID+"-fp", cid, lat, lng, now)
	fpID := fmt.Sprintf("fp-test-%d", time.Now().UnixNano())
	fp := "footprint"
	fpReq.BuildingID = &fpID
	fpReq.BuildingSource = &fp
	if _, created, err := svc.Submit(ctx, fpReq, &sid); err != nil || !created {
		t.Errorf("footprint re-report: created=%v err=%v, want a normal insert", created, err)
	}
}

// TestAreaGroups_PublicTierVerifiedOnly locks the public-data coherence of the
// area aggregate: verifiedOnly=true (the anonymous/public tier, mirrored from
// /map/features by the handler) counts VERIFIED reports only, while the analyst
// call keeps every status — so the public page can never read pending/flagged
// counts out of the anon area-groups endpoint next to verified-only numbers.
func TestAreaGroups_PublicTierVerifiedOnly(t *testing.T) {
	pool := testDB(t)
	svc, reports, crises := testService(pool)
	cid, lat, lng := newTestCrisis(t, pool, crises)
	ctx := context.Background()

	place := fmt.Sprintf("Test Quarter %d", time.Now().UnixNano())
	mk := func(suffix string, lngOff float64) string {
		id := fmt.Sprintf("t-area-%s-%d", suffix, time.Now().UnixNano())
		req := submitReq(id, cid, lat, lng+lngOff, time.Now().UTC())
		req.Place = place
		if _, created, err := svc.Submit(ctx, req, nil); err != nil || !created {
			t.Fatalf("submit %s: created=%v err=%v", id, created, err)
		}
		return id
	}
	verifiedID := mk("v", 0)
	mk("p", 0.001) // stays pending

	// Verify one (force=true: the photo gate is not under test here).
	if _, err := reports.UpdateVerification(ctx, verifiedID, "verified", "qa@undp.org", nil, true); err != nil {
		t.Fatalf("verify: %v", err)
	}

	countFor := func(groups []model.AreaGroup) int {
		for _, g := range groups {
			if g.Area == place {
				return g.Count
			}
		}
		return 0
	}
	analyst, err := reports.AreaGroups(ctx, cid, false)
	if err != nil {
		t.Fatalf("analyst AreaGroups: %v", err)
	}
	if got := countFor(analyst); got != 2 {
		t.Errorf("analyst-tier count = %d, want 2 (all statuses)", got)
	}
	public, err := reports.AreaGroups(ctx, cid, true)
	if err != nil {
		t.Fatalf("public AreaGroups: %v", err)
	}
	if got := countFor(public); got != 1 {
		t.Errorf("public-tier count = %d, want 1 (verified only)", got)
	}
}

// TestUpdateVerification_PhotoGate locks the evidence gate and its audit trail:
// verifying a photo-less report fails closed (ErrPhotoRequired → 409) and
// writes NO audit row; the explicit force=true override succeeds and is
// recorded as forced (with the analyst's note); a report WITH a photo verifies
// without any override.
func TestUpdateVerification_PhotoGate(t *testing.T) {
	pool := testDB(t)
	svc, reports, crises := testService(pool)
	cid, lat, lng := newTestCrisis(t, pool, crises)
	ctx := context.Background()

	auditRows := func(id string) (n int, forced bool, note string) {
		t.Helper()
		err := pool.QueryRow(ctx, `
			SELECT count(*),
			       COALESCE(bool_or(forced), false),
			       COALESCE(max(note), '')
			FROM report_verification_audit WHERE report_id = $1`, id).Scan(&n, &forced, &note)
		if err != nil {
			t.Fatalf("audit query: %v", err)
		}
		return n, forced, note
	}

	// 1. Photo-less report: verified is refused, nothing is audited or changed.
	bare := fmt.Sprintf("t-gate-%d", time.Now().UnixNano())
	if _, created, err := svc.Submit(ctx, submitReq(bare, cid, lat, lng, time.Now().UTC()), nil); err != nil || !created {
		t.Fatalf("submit: created=%v err=%v", created, err)
	}
	if _, err := reports.UpdateVerification(ctx, bare, "verified", "qa@undp.org", nil, false); !errors.Is(err, store.ErrPhotoRequired) {
		t.Fatalf("photo-less verify: err = %v, want ErrPhotoRequired", err)
	}
	if rep, err := reports.GetByID(ctx, bare); err != nil || rep == nil || rep.Verification != "pending" {
		t.Errorf("refused verify must leave the report pending, got %+v (err=%v)", rep, err)
	}
	if n, _, _ := auditRows(bare); n != 0 {
		t.Errorf("refused verify must write no audit row, got %d", n)
	}

	// 2. force=true overrides the gate — and the override itself is audited.
	note := "no photo; damage confirmed on-site by field validator"
	updated, err := reports.UpdateVerification(ctx, bare, "verified", "qa@undp.org", &note, true)
	if err != nil || updated == nil || updated.Verification != "verified" {
		t.Fatalf("forced verify: %+v (err=%v)", updated, err)
	}
	if n, forced, gotNote := auditRows(bare); n != 1 || !forced || gotNote != note {
		t.Errorf("forced verify audit = (n=%d forced=%v note=%q), want (1, true, %q)", n, forced, gotNote, note)
	}

	// 3. A report WITH a photo verifies without force, audited as not forced.
	withPhoto := bare + "-photo"
	if _, created, err := svc.Submit(ctx, submitReq(withPhoto, cid, lat, lng, time.Now().UTC()), nil); err != nil || !created {
		t.Fatalf("submit: created=%v err=%v", created, err)
	}
	if ok, err := reports.SetPhotoURL(ctx, withPhoto, "/api/v1/reports/"+withPhoto+"/photo"); err != nil || !ok {
		t.Fatalf("set photo url: ok=%v err=%v", ok, err)
	}
	if updated, err := reports.UpdateVerification(ctx, withPhoto, "verified", "qa@undp.org", nil, false); err != nil || updated == nil || updated.Verification != "verified" {
		t.Fatalf("verify with photo: %+v (err=%v)", updated, err)
	}
	if n, forced, _ := auditRows(withPhoto); n != 1 || forced {
		t.Errorf("with-photo audit = (n=%d forced=%v), want (1, false)", n, forced)
	}
}

// TestWithdrawReport locks the reporter-initiated takedown (data-subject erasure):
// only the creating device may withdraw (a stranger gets Owned=false and the report
// survives untouched); the owner's withdrawal ERASES the row entirely (count→0) and
// records a non-PII accountability row; an unknown id is Found=false. The handler maps
// these to 403 / 200 / 404 (see handler.WithdrawReport).
func TestWithdrawReport(t *testing.T) {
	pool := testDB(t)
	svc, reports, crises := testService(pool)
	cid, lat, lng := newTestCrisis(t, pool, crises)
	ctx := context.Background()

	id := fmt.Sprintf("wd-it-%d", time.Now().UnixNano())
	// deviceA is the reporter's X-Device-Id (submitters.anonymous_id); the report stores
	// the RESOLVED submitter uuid, so the handler/Submit takes the uuid, while withdraw
	// authorizes against the anonymous_id (mirrors the live HTTP flow).
	deviceA := fmt.Sprintf("wd-dev-A-%d", time.Now().UnixNano())
	sidA, err := crises.ResolveSubmitterID(ctx, deviceA)
	if err != nil {
		t.Fatalf("resolve submitter: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM report_withdrawals WHERE report_id = $1", id)
		_, _ = pool.Exec(ctx, "DELETE FROM reports WHERE id = $1", id)
		_, _ = pool.Exec(ctx, "DELETE FROM submitters WHERE anonymous_id = $1", deviceA)
	})

	if _, created, err := svc.Submit(ctx, submitReq(id, cid, lat, lng, time.Now().UTC()), &sidA); err != nil || !created {
		t.Fatalf("submit: created=%v err=%v", created, err)
	}

	count := func() int {
		var n int
		_ = pool.QueryRow(ctx, "SELECT count(*) FROM reports WHERE id = $1", id).Scan(&n)
		return n
	}

	// A stranger cannot withdraw: Owned=false, and the report survives untouched.
	if out, err := reports.WithdrawReport(ctx, id, "wd-stranger"); err != nil || !out.Found || out.Owned {
		t.Fatalf("stranger withdraw = %+v (err=%v), want Found=true Owned=false", out, err)
	}
	if count() != 1 {
		t.Fatalf("a stranger's withdraw must NOT erase the report")
	}

	// The creating device erases it.
	if out, err := reports.WithdrawReport(ctx, id, deviceA); err != nil || !out.Found || !out.Owned {
		t.Fatalf("owner withdraw = %+v (err=%v), want Found=true Owned=true", out, err)
	}
	if count() != 0 {
		t.Fatalf("owner withdraw must ERASE the report row (got %d)", count())
	}
	// Accountability: a non-PII record of the erasure is kept.
	var audits int
	_ = pool.QueryRow(ctx, "SELECT count(*) FROM report_withdrawals WHERE report_id = $1", id).Scan(&audits)
	if audits != 1 {
		t.Errorf("report_withdrawals rows = %d, want 1", audits)
	}

	// An unknown id is Found=false (HTTP 404), never an error.
	if out, err := reports.WithdrawReport(ctx, "wd-does-not-exist", deviceA); err != nil || out.Found {
		t.Errorf("withdraw unknown id = %+v (err=%v), want Found=false", out, err)
	}
}
