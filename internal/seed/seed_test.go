package seed

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestSeedParity locks the distributions to the dashboard/mobile dataset. If the
// ported rnd()/pick() ever drifts from JS Math.sin, this fails CI instead of
// silently diverging the two clients' seed data. Counts are the golden
// assertion; timestamps are asserted only RELATIVE to the supplied base —
// never as absolute dates — so the seed stays demo-fresh whenever it runs.
func TestSeedParity(t *testing.T) {
	base := time.Unix(1_700_000_000, 0).UTC()
	reps := BuildReports(base)
	if len(reps) != 56 {
		t.Fatalf("report count = %d, want 56", len(reps))
	}

	crisisStart := base.Add(-seedCrisisWindow)
	dmg := map[string]int{}
	ver := map[string]int{}
	synced, possibly := 0, 0
	for _, r := range reps {
		dmg[r.Damage]++
		ver[r.Verification]++
		if r.Synced {
			synced++
		}
		if r.PossiblyDamaged {
			possibly++
		}
		if r.ID == "" || r.IdempotencyKey != "idem-"+r.ID {
			t.Errorf("bad id/idempotency: %q / %q", r.ID, r.IdempotencyKey)
		}
		if r.Damage != "minimal" && r.Damage != "partial" && r.Damage != "complete" {
			t.Errorf("unknown damage tier %q (must be a 3-tier value)", r.Damage)
		}
		// Relative window: every capture sits after the crisis start and before
		// base, and reaches the server (created_at) after it was captured.
		if !r.CapturedAt.After(crisisStart) || r.CapturedAt.After(base) {
			t.Errorf("report %s: capturedAt %v outside (crisisStart %v, base %v]", r.ID, r.CapturedAt, crisisStart, base)
		}
		if r.CreatedAt.Before(r.CapturedAt) {
			t.Errorf("report %s: createdAt %v before capturedAt %v", r.ID, r.CreatedAt, r.CapturedAt)
		}
	}
	t.Logf("damage(3)=%v synced=%d possibly=%d", dmg, synced, possibly)

	// 3-tier distribution must sum to 56 and span the mandated tiers.
	sum := dmg["minimal"] + dmg["partial"] + dmg["complete"]
	if sum != 56 {
		t.Errorf("damage buckets sum = %d, want 56 (%v)", sum, dmg)
	}
	if dmg["minimal"] == 0 || dmg["partial"] == 0 || dmg["complete"] == 0 {
		t.Errorf("damage distribution must span all 3 tiers, got %v", dmg)
	}
	// verification + synced are independent of the damage scale change → unchanged.
	if ver["verified"] != 19 || ver["pending"] != 28 || ver["flagged"] != 9 {
		t.Errorf("verification counts = %v, want verified:19 pending:28 flagged:9", ver)
	}
	if synced != 37 {
		t.Errorf("synced = %d, want 37", synced)
	}

	// Determinism: the same base must reproduce the exact same capture offsets
	// (the spread is rnd-derived, not wall-clock-derived).
	again := BuildReports(base)
	for i := range reps {
		if !reps[i].CapturedAt.Equal(again[i].CapturedAt) {
			t.Errorf("report %s: capturedAt not deterministic: %v vs %v", reps[i].ID, reps[i].CapturedAt, again[i].CapturedAt)
		}
	}
}

// TestSeedBuildingIdentity locks the post-fabrication building mix: a seeded
// report either came from a tapped footprint — a deterministic "fp-" id WITH
// buildingSource="footprint", so per-building version chains stay believable —
// or is GPS pin-only (no buildingId at all), the new normal for mobile reports.
// The synthetic "b-<grid>" ids the mobile client removed must never reappear.
// The footprint share is the golden ~60% draw: exactly 33 of 56.
func TestSeedBuildingIdentity(t *testing.T) {
	base := time.Unix(1_700_000_000, 0).UTC()
	footprints := 0
	for _, r := range BuildReports(base) {
		if r.BuildingID == nil {
			if r.BuildingSource != nil {
				t.Errorf("report %s: pin-only report must carry no buildingSource, got %q", r.ID, *r.BuildingSource)
			}
			continue
		}
		if !strings.HasPrefix(*r.BuildingID, "fp-") {
			t.Errorf("report %s: buildingId %q is not footprint-style (fabricated grid ids must not reappear)", r.ID, *r.BuildingID)
		}
		if r.BuildingSource == nil || *r.BuildingSource != "footprint" {
			t.Errorf("report %s: fp- id without buildingSource=footprint", r.ID)
		}
		footprints++
	}
	if footprints != 33 {
		t.Errorf("footprint reports = %d, want 33 (~60%% of 56)", footprints)
	}

	// The demo version chain is a footprint re-report by definition: all three
	// entries share one deterministic fp- id + the footprint provenance.
	chain := buildVersionChain(base)
	for _, r := range chain {
		if r.BuildingID == nil || !strings.HasPrefix(*r.BuildingID, "fp-") ||
			r.BuildingSource == nil || *r.BuildingSource != "footprint" {
			t.Errorf("version-chain report %s must be a footprint re-report (fp- id + buildingSource)", r.ID)
		}
	}
	for _, r := range chain[1:] {
		if *r.BuildingID != *chain[0].BuildingID {
			t.Errorf("version chain must share one building id: %q vs %q", *r.BuildingID, *chain[0].BuildingID)
		}
	}
}

// TestSeedModular locks the deterministic [B5] modular demo data: even indices
// (28 of 56) carry the C1 camelCase blob, odd indices stay nil, every value sits
// inside the C1 enums, and worse damage never claims fully-functional services.
// Index-keyed (no rnd() calls), so it must be byte-identical across runs.
func TestSeedModular(t *testing.T) {
	base := time.Unix(1_700_000_000, 0).UTC()
	reps := BuildReports(base)

	elecOK := map[string]bool{"none_observed": true, "minor": true, "moderate": true, "severe": true, "destroyed": true, "unknown": true}
	healthOK := map[string]bool{"fully_functional": true, "partially_functional": true, "largely_disrupted": true, "not_functioning": true, "unknown": true}
	needsOK := map[string]bool{"food_water": true, "cash": true, "healthcare": true, "shelter": true, "livelihoods": true, "wash": true, "protection": true, "local_support": true, "other": true}

	withModular := 0
	for i, r := range reps {
		if i%2 != 0 {
			if r.Modular != nil {
				t.Errorf("report %s (index %d): odd index must not carry modular", r.ID, i)
			}
			continue
		}
		if r.Modular == nil {
			t.Errorf("report %s (index %d): even index must carry modular", r.ID, i)
			continue
		}
		withModular++

		// Exact C1 wire keys (camelCase), not Go-field or snake_case names.
		s := string(r.Modular)
		for _, key := range []string{`"electricity":`, `"healthServices":`, `"pressingNeeds":`} {
			if !strings.Contains(s, key) {
				t.Errorf("report %s: modular missing wire key %s: %s", r.ID, key, s)
			}
		}

		var m struct {
			Electricity    string   `json:"electricity"`
			HealthServices string   `json:"healthServices"`
			PressingNeeds  []string `json:"pressingNeeds"`
		}
		if err := json.Unmarshal(r.Modular, &m); err != nil {
			t.Fatalf("report %s: bad modular JSON: %v", r.ID, err)
		}
		if !elecOK[m.Electricity] {
			t.Errorf("report %s: electricity %q not in C1 enum", r.ID, m.Electricity)
		}
		if !healthOK[m.HealthServices] {
			t.Errorf("report %s: healthServices %q not in C1 enum", r.ID, m.HealthServices)
		}
		if len(m.PressingNeeds) == 0 {
			t.Errorf("report %s: pressingNeeds must not be empty", r.ID)
		}
		for _, n := range m.PressingNeeds {
			if !needsOK[n] {
				t.Errorf("report %s: pressingNeed %q not in C1 enum", r.ID, n)
			}
		}
		// Plausibility: partial/complete damage never reports fully-functional health.
		if (r.Damage == "partial" || r.Damage == "complete") && m.HealthServices == "fully_functional" {
			t.Errorf("report %s: damage %q with fully_functional health is implausible", r.ID, r.Damage)
		}
	}
	if withModular != 28 {
		t.Errorf("reports with modular = %d, want 28 (half of 56)", withModular)
	}

	// Determinism: a second build must produce byte-identical modular blobs.
	again := BuildReports(base)
	for i := range reps {
		if string(reps[i].Modular) != string(again[i].Modular) {
			t.Errorf("report %s: modular not deterministic:\n%s\nvs\n%s",
				reps[i].ID, reps[i].Modular, again[i].Modular)
		}
	}
}

// TestSeedPhotos locks the embedded demo evidence: 8–12 real photos under a
// 3 MB embed budget, and an assignment where EVERY verified report carries a
// photo with its honest byte size (the photo gate must hold in the demo), while
// photo-less reports honestly report SizeBytes 0 — never a fabricated payload.
func TestSeedPhotos(t *testing.T) {
	photos, err := loadSeedPhotos()
	if err != nil {
		t.Fatalf("loadSeedPhotos: %v", err)
	}
	if len(photos) < 8 || len(photos) > 12 {
		t.Errorf("embedded photos = %d, want 8..12", len(photos))
	}
	sizeByName := map[string]int64{}
	var total int64
	for _, p := range photos {
		if len(p.data) == 0 {
			t.Errorf("photo %s is empty", p.name)
		}
		sizeByName[p.name] = int64(len(p.data))
		total += int64(len(p.data))
	}
	if total >= 3<<20 {
		t.Errorf("embedded photo total = %d bytes, must stay under 3 MB", total)
	}

	base := time.Unix(1_700_000_000, 0).UTC()
	reps := append(BuildReports(base), buildVersionChain(base)...)
	assignSeedPhotos(reps, photos)

	for _, r := range reps {
		if r.Verification == "verified" && r.PhotoURL == nil {
			t.Errorf("verified report %s has no photo (the photo gate must hold in the demo)", r.ID)
		}
		if r.PhotoURL == nil {
			if r.SizeBytes != 0 || len(r.Photos) != 0 {
				t.Errorf("photo-less report %s: SizeBytes=%d photos=%d, want 0/0 (honest sizes)", r.ID, r.SizeBytes, len(r.Photos))
			}
			continue
		}
		if want := "/api/v1/reports/" + r.ID + "/photo"; *r.PhotoURL != want {
			t.Errorf("report %s: photoUrl %q, want %q", r.ID, *r.PhotoURL, want)
		}
		if len(r.Photos) != 1 {
			t.Fatalf("report %s: photos = %d, want 1", r.ID, len(r.Photos))
		}
		wantSize, ok := sizeByName[r.Photos[0].LocalPath]
		if !ok {
			t.Errorf("report %s references unknown photo %q", r.ID, r.Photos[0].LocalPath)
		} else if r.SizeBytes != wantSize || r.Photos[0].SizeBytes != wantSize {
			t.Errorf("report %s: SizeBytes=%d/%d, want the real embedded size %d", r.ID, r.SizeBytes, r.Photos[0].SizeBytes, wantSize)
		}
	}

	// Determinism: the index-keyed round-robin must be byte-identical across runs.
	again := append(BuildReports(base), buildVersionChain(base)...)
	assignSeedPhotos(again, photos)
	for i := range reps {
		a, b := reps[i].PhotoURL, again[i].PhotoURL
		switch {
		case (a == nil) != (b == nil):
			t.Errorf("report %s: photo assignment not deterministic", reps[i].ID)
		case a != nil && (*a != *b || reps[i].Photos[0].LocalPath != again[i].Photos[0].LocalPath):
			t.Errorf("report %s: photo assignment not deterministic (%s vs %s)", reps[i].ID, reps[i].Photos[0].LocalPath, again[i].Photos[0].LocalPath)
		}
	}
}
