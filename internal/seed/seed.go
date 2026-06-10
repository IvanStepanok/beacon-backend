// Package seed deterministically reproduces the Antakya dataset using the same
// sin-hash rnd(seed)/pick() as the dashboard mock (ported to Go; math.Floor(rnd*N)
// absorbs the sub-ULP gap between Go math.Sin and JS Math.sin). Stable across the
// 5-level migration: verification 19/28/9, synced 37, ids 1156..1211 are unchanged.
// The damage scale is now 5-level EMS-98 (none:12/slight:17/moderate:11/severe:6/
// destroyed:10) — the dashboard mock will be re-pointed at this contract when the
// clients are wired to the live API. Runs only on an empty reports table (idempotent).
//
// All timestamps are RELATIVE to seed time (crisis started seedCrisisWindow ago,
// captures spread deterministically across it) so the demo reads as a live
// operation whenever it is seeded. Real embedded evidence photos (photos.go) are
// installed into PHOTO_DIR and attached to every verified report.
package seed

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"log/slog"
	"math"
	"sort"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stepanok/beacon-server/internal/auth"
	"github.com/stepanok/beacon-server/internal/model"
	"github.com/stepanok/beacon-server/internal/store"
)

// demoUser is a seeded analyst account (password "beacon123" for all — demo only).
type demoUser struct {
	email, name, role string
	region            *string
	scope             []string
}

func seedUsers(ctx context.Context, users *store.Users, logger *slog.Logger) error {
	if n, err := users.Count(ctx); err != nil || n > 0 {
		return err
	}
	rbec := "RBEC" // Türkiye sits in UNDP's Europe & CIS regional bureau
	hash, err := auth.HashPassword("beacon123")
	if err != nil {
		return err
	}
	demo := []demoUser{
		{"admin@undp.org", "Aylin Demir", model.RoleCrisisAdmin, nil, []string{"*"}},
		{"regional@undp.org", "Marco Rossi", model.RoleRegionalAnalyst, &rbec, []string{"*"}},
		{"co@undp.org", "Zeynep Kaya", model.RoleCOAnalyst, &rbec, []string{crisisID}},
		{"validator@undp.org", "Emre Yıldız", model.RoleFieldValidator, &rbec, []string{crisisID}},
		{"viewer@undp.org", "Sven Olsson", model.RoleExternalViewer, nil, []string{"*"}},
	}
	for _, d := range demo {
		if err := users.Create(ctx, d.email, hash, d.name, d.role, d.region, d.scope); err != nil {
			return fmt.Errorf("seed user %s: %w", d.email, err)
		}
	}
	logger.Info("seeded demo users", "count", len(demo), "password", "beacon123")
	return nil
}

const (
	antakyaLat = 36.2021
	antakyaLng = 36.1601
	crisisID   = "crisis-antakya"

	// seedCrisisWindow is how long before SEED TIME the demo crisis started.
	// Captures spread deterministically across it (offsets come from the same
	// rnd(fi+200) draw as before), so the dataset always reads as a live
	// multi-day operation — never a frozen calendar date that looks dead by
	// the time it is evaluated.
	seedCrisisWindow = 72 * time.Hour

	// seedReportSpreadMin caps a seeded capture's age so every report falls
	// strictly INSIDE the crisis window (latest possible age + margin < window).
	seedReportSpreadMin = int(seedCrisisWindow/time.Minute) - 90
)

// rnd mirrors the dashboard: frac(sin(seed*127.1+311.7)*43758.5453). Go float64
// math.Sin == JS Math.sin (IEEE-754), so the sequence is identical.
func rnd(seed float64) float64 {
	x := math.Sin(seed*127.1+311.7) * 43758.5453
	return x - math.Floor(x)
}

func pick[T any](arr []T, seed float64) T {
	return arr[int(math.Floor(rnd(seed)*float64(len(arr))))%len(arr)]
}

var (
	places = []string{
		"Akdeniz Ave", "Bahçe Sk.", "Saray Cd.", "İstiklal Sk.", "Mevlana Mh.", "Pazar Sk.",
		"Kale Mh.", "Liman Sk.", "Çiçek Cd.", "Demir Mh.", "Kuş Sk.", "Gül Cd.",
		"Cumhuriyet Mh.", "Ulus Mh.", "Yeni Mahalle", "Kanatlı Cd.", "Köprübaşı", "Harbiye Yolu",
	}
	infra  = []string{"residential", "commercial", "government", "utility", "transport", "community", "public"}
	damage = []string{"none", "slight", "moderate", "severe", "destroyed"} // EMS-98 5-level
	verif  = []string{"verified", "pending", "pending", "verified", "flagged", "pending"}
	debris = []string{"no", "no", "yes", "unsure"}

	descriptions = map[string][]model.ReportDescription{
		"none": {
			{Original: "Bina sağlam, sadece toz ve cam kırıkları.", OriginalLang: "tr", Translated: "Building intact, only dust and broken glass."},
		},
		"slight": {
			{Original: "Cephe çatlakları görünüyor, çatı sağlam.", OriginalLang: "tr", Translated: "Facade cracks visible, roof intact."},
			{Original: "Hafif hasar, bina hâlâ kullanılabilir.", OriginalLang: "tr", Translated: "Minor damage, the building is still usable."},
		},
		"moderate": {
			{Original: "Taşıyıcı olmayan duvarlarda çatlaklar, dikkatli girilmeli.", OriginalLang: "tr", Translated: "Cracks in non-load-bearing walls, enter with caution."},
			{Original: "Orta düzey hasar, kısmi kullanım mümkün.", OriginalLang: "tr", Translated: "Moderate damage, partial use possible."},
		},
		"severe": {
			{Original: "Üst katlarda ciddi hasar var, giriş riskli.", OriginalLang: "tr", Translated: "Serious damage on the upper floors, entry is risky."},
			{Original: "Duvarlar çatladı, bina tahliye edildi.", OriginalLang: "tr", Translated: "Walls have cracked, the building was evacuated."},
		},
		"destroyed": {
			{Original: "Bina tamamen çökmüş, kimse giremiyor.", OriginalLang: "tr", Translated: "Building has fully collapsed, no access."},
			{Original: "Enkaz altında insan olabilir, ekip lazım.", OriginalLang: "tr", Translated: "People may be trapped under the rubble, a team is needed."},
		},
	}
)

func strptr(s string) *string { return &s }

// footprintID derives a deterministic footprint-style building id from a report's
// point — the seed's stand-in for the mobile client's real footprint ids (hashed
// from the tapped polygon). Same point ⇒ same id, so seeded re-reports keep
// forming believable per-building version chains.
func footprintID(lat, lng float64) string {
	h := fnv.New32a()
	fmt.Fprintf(h, "%.6f:%.6f", lat, lng)
	return fmt.Sprintf("fp-%08x", h.Sum32())
}

// clusterFor routes an infrastructure type to its lead cluster (2026 model).
var clusterFor = map[string]string{
	"residential": "slsc", "commercial": "early_recovery", "government": "early_recovery",
	"utility": "wash", "transport": "logistics", "community": "cccm", "public": "education",
}

// modularSeed mirrors the [C1] modular wire/stored shape (camelCase keys) so the
// seeded blob is byte-compatible with what the mobile client POSTs.
type modularSeed struct {
	Electricity    string   `json:"electricity"`
	HealthServices string   `json:"healthServices"`
	PressingNeeds  []string `json:"pressingNeeds"`
}

// modularVariants holds two plausible secondary-impact profiles per damage grade
// (worse damage → worse electricity/health, more acute needs). All values come from
// the C1 enums: electricity none_observed|minor|moderate|severe|destroyed|unknown,
// healthServices fully_functional|partially_functional|largely_disrupted|
// not_functioning|unknown, pressingNeeds ⊆ {food_water,cash,healthcare,shelter,
// livelihoods,wash,protection,local_support,other}.
var modularVariants = map[string][2]modularSeed{
	"none": {
		{"none_observed", "fully_functional", []string{"local_support"}},
		{"unknown", "unknown", []string{"other"}},
	},
	"slight": {
		{"minor", "fully_functional", []string{"cash"}},
		{"none_observed", "partially_functional", []string{"livelihoods"}},
	},
	"moderate": {
		{"moderate", "partially_functional", []string{"food_water", "wash"}},
		{"minor", "partially_functional", []string{"cash", "livelihoods"}},
	},
	"severe": {
		{"severe", "largely_disrupted", []string{"shelter", "food_water"}},
		{"severe", "not_functioning", []string{"shelter", "healthcare", "wash"}},
	},
	"destroyed": {
		{"destroyed", "not_functioning", []string{"shelter", "healthcare", "food_water"}},
		{"destroyed", "not_functioning", []string{"shelter", "protection", "healthcare"}},
	},
}

// modularFor deterministically assigns the C1 secondary-impacts blob to a seeded
// report: even indices (28 of 56) carry it, odd indices stay nil — a realistic
// "optional module sometimes skipped" mix. The variant alternates by (i/2)%2.
// Purely index-keyed (NO rnd() calls), so the TestSeedParity sequence is untouched.
func modularFor(i int, dmg string) json.RawMessage {
	if i%2 != 0 {
		return nil
	}
	v := modularVariants[dmg][(i/2)%2]
	raw, err := json.Marshal(v)
	if err != nil { // unreachable: static struct, but never seed a half-broken blob
		return nil
	}
	return raw
}

// BuildReports reproduces the dashboard's 56-report Antakya set. captured_at is
// base − ageMin, with ageMin drawn from the same rnd(fi+200) sequence as the
// dashboard mock but scaled across the full crisis window, so the dataset reads
// as a live multi-day operation at seed time and ages naturally afterwards.
// Photos/SizeBytes start empty/0 — assignSeedPhotos attaches the real embedded
// evidence (and its honest byte size) afterwards.
func BuildReports(base time.Time) []model.Report {
	out := make([]model.Report, 0, 56)
	for i := 0; i < 56; i++ {
		fi := float64(i)
		dmg := damage[int(math.Floor(rnd(fi+100)*5))%5]
		possibly := rnd(fi+900) > 0.82 // ~18% flagged "possibly damaged" (reporter unsure)
		lat := antakyaLat + (rnd(fi)-0.5)*0.05
		lng := antakyaLng + (rnd(fi+50)-0.5)*0.05
		ageMin := int(math.Floor(rnd(fi+200)*float64(seedReportSpreadMin))) + 1
		synced := rnd(fi+300) > 0.28
		hasDesc := rnd(fi+400) > 0.5
		hasAI := rnd(fi+500) > 0.4
		id := fmt.Sprintf("%d", 1156+i)
		// Building identity mirrors the REAL mobile capture mix: ~60% of reports
		// come from a tapped footprint polygon (deterministic "fp-" id WITH the
		// buildingSource provenance, so version chains stay believable), the rest
		// are GPS pin-only — the new normal for a reporter who never taps a
		// footprint. The old synthetic "b-<grid>" ids fabricated a building
		// identity from the pin itself (exactly what mobile removed) and must not
		// be resurrected here. rnd() is seed-keyed (stateless), so the golden
		// parity draws (fi … fi+990) are untouched by the extra fi+1000 draw.
		var buildingID, buildingSource *string
		if rnd(fi+1000) < 0.6 {
			buildingID = strptr(footprintID(lat, lng))
			buildingSource = strptr("footprint")
		}

		r := model.Report{
			ID:               id,
			IdempotencyKey:   "idem-" + id,
			CrisisID:         crisisID,
			Damage:           dmg,
			PossiblyDamaged:  possibly,
			Verification:     verif[i%len(verif)],
			Debris:           pick(debris, fi+22),
			InfraTypes:       []string{pick(infra, fi+11)},
			CrisisNature:     []string{"earthquake"},
			Lat:              &lat,
			Lng:              &lng,
			LocationResolved: true,
			BuildingID:       buildingID,
			BuildingSource:   buildingSource,
			Version:          1,
			PlusCode:         strptr("8G7F6526+VC"),
			Place:            places[i%len(places)],
			Photos:           []model.PhotoRef{},
			SizeBytes:        0, // honest: set from the real photo size in assignSeedPhotos
			Anonymization:    model.DefaultAnonymization(),
			Synced:           synced,
			Modular:          modularFor(i, dmg),
			CapturedAt:       base.Add(-time.Duration(ageMin) * time.Minute),
		}
		// created_at: the report reached the server a short, deterministic
		// offline-sync lag after capture — seeded history must not all "arrive"
		// at seed time (live rows get created_at = now at insert).
		r.CreatedAt = r.CapturedAt.Add(time.Duration(int(math.Floor(rnd(fi+650)*45))+2) * time.Minute)
		r.UpdatedAt = r.CreatedAt
		if synced {
			r.Sync = json.RawMessage(`{"type":"Synced"}`)
		} else {
			r.Sync = json.RawMessage(`{"type":"Queued"}`)
		}
		if hasDesc {
			d := pick(descriptions[dmg], fi+800)
			r.Description = &d
		}
		if hasAI {
			lvl := dmg
			r.AILevel = &lvl
			conf := int(math.Round((0.62 + rnd(fi+700)*0.36) * 100))
			r.AIConfidence = &conf
		}

		// affected-sector tags (OCHA humanitarian clusters) — an optional data dimension.
		if c, ok := clusterFor[r.InfraTypes[0]]; ok {
			r.Clusters = []string{c}
		}
		out = append(out, r)
	}
	return out
}

// mahalle holds the seeded ADM3 neighbourhood names (illustrative, pending real
// Türkiye COD-AB ingest); pcodes follow the TR63 (Hatay) → TR6303 (Antakya) chain.
var mahalle = []string{
	"Akevler Mh.", "Saraykent Mh.", "Cumhuriyet Mh.",
	"Ulus Mh.", "Şükrükanatlı Mh.", "Gazi Mh.",
	"Kurtuluş Mh.", "Emek Mh.", "Zafer Mh.",
}

// seedAdminAreas builds the COD-AB reference: Türkiye → Hatay → Antakya, then a
// 3×3 grid of ADM3 neighbourhood polygons covering the Antakya report bbox so
// every seeded/submitted point reverse-geocodes to a real P-code chain.
func seedAdminAreas(ctx context.Context, pool *pgxpool.Pool, admin *store.Admin) error {
	if n, err := admin.AreaCount(ctx); err != nil || n > 0 {
		return err
	}
	tr := func(s string) *string { return &s }
	if err := store.UpsertAdminArea(ctx, pool, "TR", 0, "Türkiye", nil, "TUR", "seed", nil); err != nil {
		return err
	}
	if err := store.UpsertAdminArea(ctx, pool, "TR63", 1, "Hatay", tr("TR"), "TUR", "seed", nil); err != nil {
		return err
	}
	if err := store.UpsertAdminArea(ctx, pool, "TR6303", 2, "Antakya", tr("TR63"), "TUR", "seed", nil); err != nil {
		return err
	}
	// 3×3 grid covering lng[36.130,36.190] × lat[36.172,36.232] (cells of 0.02°).
	const minLng, minLat, cell = 36.130, 36.172, 0.02
	for gi := 0; gi < 3; gi++ {
		for gj := 0; gj < 3; gj++ {
			idx := gi*3 + gj
			pcode := fmt.Sprintf("TR6303%02d", idx+1)
			bbox := [4]float64{
				minLng + float64(gj)*cell, minLat + float64(gi)*cell,
				minLng + float64(gj+1)*cell, minLat + float64(gi+1)*cell,
			}
			if err := store.UpsertAdminArea(ctx, pool, pcode, 3, mahalle[idx], tr("TR6303"), "TUR", "seed", &bbox); err != nil {
				return err
			}
		}
	}
	return nil
}

// Run seeds the database if (and only if) the reports table is empty. photoDir
// is the live PHOTO_DIR — the embedded evidence photos are installed there so
// seeded photoUrls serve real images through GET /reports/{id}/photo.
func Run(ctx context.Context, pool *pgxpool.Pool, reports *store.Reports, crises *store.Crises, admin *store.Admin, users *store.Users, dataset, photoDir string, logger *slog.Logger) error {
	// Users seed independently of reports (gated by their own count).
	if err := seedUsers(ctx, users, logger); err != nil {
		return fmt.Errorf("seed users: %w", err)
	}
	count, err := reports.ReportCount(ctx)
	if err != nil {
		return err
	}
	if count > 0 {
		logger.Info("seed skipped (reports present)", "count", count)
		return nil
	}

	if err := seedAdminAreas(ctx, pool, admin); err != nil {
		return fmt.Errorf("seed admin areas: %w", err)
	}

	base := time.Now().UTC()

	// crisis — started a full seedCrisisWindow before seed time, so every seeded
	// capture (spread over seedReportSpreadMin) falls after the crisis began.
	glide := "EQ-2026-000042-TUR"
	level := 2
	if err := crises.UpsertCrisis(ctx, model.Crisis{
		ID: crisisID, Title: "Earthquake M 6.4", Area: "Antakya district, Hatay",
		Nature: "earthquake", CenterLat: antakyaLat, CenterLng: antakyaLng,
		Source: "UNDP RAPIDA", StartedAt: base.Add(-seedCrisisWindow),
		Glide: &glide, ResponseLevel: &level,
		RadiusKm: 25, Status: "active",
	}); err != nil {
		return fmt.Errorf("seed crisis: %w", err)
	}
	badges := json.RawMessage(`[{"id":"first","name":"First responder","earned":true},{"id":"streets","name":"Street mapper","earned":true},{"id":"verified","name":"Verified eyes","earned":false,"progressLabel":"3/5 verified"}]`)
	if err := crises.UpsertSubmitter(ctx, "A4-92K", nil, 12, 8, 120, badges); err != nil {
		return fmt.Errorf("seed submitter: %w", err)
	}

	photos, err := loadSeedPhotos()
	if err != nil {
		return fmt.Errorf("load seed photos: %w", err)
	}

	reps := BuildReports(base)
	if dataset == "mobile" || dataset == "both" {
		reps = append(reps, buildVersionChain(base)...)
	}
	// Attach the real embedded evidence photos BEFORE sorting (the round-robin
	// assignment is index-keyed, so it must see the deterministic build order).
	assignSeedPhotos(reps, photos)
	// Insert oldest-first so each building's cached current_damage ends as the newest version.
	sort.SliceStable(reps, func(a, b int) bool { return reps[a].CapturedAt.Before(reps[b].CapturedAt) })

	withPhoto := 0
	for _, r := range reps {
		// reverse-geocode via the same DB path live submits use (seeded reports are
		// always resolved, so r.Lat/r.Lng are non-nil here).
		if r.Lat != nil && r.Lng != nil {
			if chain, err := admin.ResolveAdmin(ctx, *r.Lng, *r.Lat); err == nil && chain != nil {
				r.Admin = chain
			}
		}
		if r.BuildingID != nil {
			if err := store.UpsertBuilding(ctx, pool, model.Building{
				ID: *r.BuildingID, CrisisID: crisisID, Lat: r.Lat, Lng: r.Lng, CurrentDamage: &r.Damage,
			}); err != nil {
				return fmt.Errorf("seed building: %w", err)
			}
		}
		if _, err := store.UpsertReport(ctx, pool, r); err != nil {
			return fmt.Errorf("seed report %s: %w", r.ID, err)
		}
		// Mirror the live photo flow: write the image into PHOTO_DIR, then record
		// photo_url (UpsertReport never carries photo_url — uploads set it).
		if r.PhotoURL != nil {
			if err := installSeedPhoto(photoDir, r, photos); err != nil {
				return fmt.Errorf("seed photo %s: %w", r.ID, err)
			}
			if _, err := reports.SetPhotoURL(ctx, r.ID, *r.PhotoURL); err != nil {
				return fmt.Errorf("seed photo url %s: %w", r.ID, err)
			}
			withPhoto++
		}
	}
	logger.Info("seed complete", "reports", len(reps), "withPhoto", withPhoto, "dataset", dataset)
	return nil
}

// buildVersionChain creates a 3-version history for one tapped footprint to
// demonstrate the per-building timeline (mobile scenario): slight ~2 days ago,
// severe ~1 day ago, destroyed ~2 hours ago — a deterioration spanning the
// crisis window. A version chain only exists for a REAL footprint (re-reporting
// one is the chain's job), so each entry carries the fp- id + buildingSource.
// Sizes start 0 — assignSeedPhotos attaches the real evidence.
func buildVersionChain(base time.Time) []model.Report {
	vLat, vLng := antakyaLat+0.004, antakyaLng+0.004
	bid := footprintID(vLat, vLng)
	mk := func(idn int, dmg string, ageMin int, sup *string) model.Report {
		id := fmt.Sprintf("%d", 1300+idn)
		lat, lng := vLat, vLng // fresh addresses per report
		captured := base.Add(-time.Duration(ageMin) * time.Minute)
		return model.Report{
			ID: id, IdempotencyKey: "idem-" + id, CrisisID: crisisID,
			Damage: dmg, Verification: "pending", Debris: "unsure",
			InfraTypes: []string{"residential"}, CrisisNature: []string{"earthquake"},
			Lat: &lat, Lng: &lng, LocationResolved: true,
			BuildingID: &bid, BuildingSource: strptr("footprint"),
			Version: idn, SupersedesReportID: sup, Place: "Saray Cd.",
			Photos: []model.PhotoRef{}, SizeBytes: 0,
			Anonymization: model.DefaultAnonymization(), Synced: true,
			Sync: json.RawMessage(`{"type":"Synced"}`), IsMine: true,
			CapturedAt: captured,
			CreatedAt:  captured.Add(5 * time.Minute), UpdatedAt: captured.Add(5 * time.Minute),
		}
	}
	v1 := mk(1, "slight", 2880, nil)
	id1 := v1.ID
	v2 := mk(2, "severe", 1440, &id1)
	id2 := v2.ID
	v3 := mk(3, "destroyed", 120, &id2)
	return []model.Report{v1, v2, v3}
}
