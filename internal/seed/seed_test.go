package seed

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stepanok/beacon-server/internal/model"
)

// TestSeedParity locks the distributions to the dashboard/mobile dataset. If the
// ported rnd()/pick() ever drifts from JS Math.sin, this fails CI instead of
// silently diverging the two clients' seed data.
func TestSeedParity(t *testing.T) {
	reps := BuildReports(time.Unix(1_700_000_000, 0).UTC())
	if len(reps) != 56 {
		t.Fatalf("report count = %d, want 56", len(reps))
	}

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
		if model.DamageOrder[r.Damage] == 0 && r.Damage != "none" {
			t.Errorf("unknown damage grade %q", r.Damage)
		}
	}
	t.Logf("damage(5)=%v synced=%d possibly=%d", dmg, synced, possibly)

	// 5-level distribution must sum to 56 and span the EMS-98 grades.
	sum := dmg["none"] + dmg["slight"] + dmg["moderate"] + dmg["severe"] + dmg["destroyed"]
	if sum != 56 {
		t.Errorf("damage buckets sum = %d, want 56 (%v)", sum, dmg)
	}
	// verification + synced are independent of the damage scale change → unchanged.
	if ver["verified"] != 19 || ver["pending"] != 28 || ver["flagged"] != 9 {
		t.Errorf("verification counts = %v, want verified:19 pending:28 flagged:9", ver)
	}
	if synced != 37 {
		t.Errorf("synced = %d, want 37", synced)
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
		// Plausibility: severe/destroyed damage never reports fully-functional health.
		if (r.Damage == "severe" || r.Damage == "destroyed") && m.HealthServices == "fully_functional" {
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
