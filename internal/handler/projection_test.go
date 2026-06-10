package handler

import (
	"encoding/json"
	"testing"

	"github.com/stepanok/beacon-server/internal/model"
)

func fptr(v float64) *float64 { return &v }
func sptr(s string) *string   { return &s }

// fullReport is a maximally-populated VERIFIED report — every field the public
// projection must strip or coarsen is set to a sentinel the assertions detect.
func fullReport() model.Report {
	conf := 91
	return model.Report{
		ID: "1156", IdempotencyKey: "idem-1156", CrisisID: "crisis-antakya",
		SubmitterID: sptr("d1b2c3"), Damage: "severe", DamageTier: "partial",
		Verification: "verified", Debris: "yes",
		InfraTypes: []string{"residential"}, InfraName: sptr("Cumhuriyet Primary School"),
		CrisisNature: []string{"earthquake"},
		Lat:          fptr(36.2021492), Lng: fptr(36.1601337), LocationResolved: true,
		GPSAccuracyMeters: fptr(4.2),
		BuildingID:        sptr("b-362021-361601"), BuildingSource: sptr("footprint"),
		What3Words: sptr("8G7F6526+VC"), PlusCode: sptr("8G7F6526+VC"),
		Landmark: sptr("opposite the bakery"), Place: "Saray Cd.",
		PhotoURL: sptr("/api/v1/reports/1156/photo"),
		Location: model.ReportLocation{
			Lat: fptr(36.2021492), Lng: fptr(36.1601337),
			BuildingID: sptr("b-362021-361601"), BuildingSource: sptr("footprint"),
			What3Words: sptr("8G7F6526+VC"), PlusCode: sptr("8G7F6526+VC"),
			Landmark: sptr("opposite the bakery"), GPSAccuracyMeters: fptr(4.2),
		},
		Description: &model.ReportDescription{Original: "Duvarlar çatladı", OriginalLang: "tr", Translated: "Walls cracked"},
		AILevel:     sptr("severe"), AIConfidence: &conf,
		Modular:       json.RawMessage(`{"electricity":"severe"}`),
		Anonymization: model.DefaultAnonymization(),
		TaskStatus:    "in_progress", Disposition: sptr("referred"), Assignee: sptr("Alpha Team"),
		TaskRef: sptr("ANT-1156"), Severity: "life_safety", LifeSafety: true,
		Clusters: []string{"slsc"},
	}
}

// TestPublicProjection_Coarsening locks the public-tier lockdown: coordinates
// are coarsened to ~110 m (3 decimals) and EVERY field that could de-coarsen
// the grid or de-anonymize the reporter — identity, precise-location keys
// (flat AND nested), free text, and the operational/analyst-only axis — is
// stripped. Weakening any of these is a privacy regression.
func TestPublicProjection_Coarsening(t *testing.T) {
	pub := publicProjection(fullReport())

	// Coordinates: rounded to exactly 3 decimals, never the raw fix.
	if pub.Lat == nil || *pub.Lat != 36.202 || pub.Lng == nil || *pub.Lng != 36.160 {
		t.Errorf("coords = %v,%v, want coarsened 36.202,36.160", pub.Lat, pub.Lng)
	}
	if pub.Location.Lat == nil || *pub.Location.Lat != 36.202 || pub.Location.Lng == nil || *pub.Location.Lng != 36.160 {
		t.Errorf("nested coords = %v,%v, want coarsened 36.202,36.160", pub.Location.Lat, pub.Location.Lng)
	}

	// Identity + precise-location keys must be gone (flat and nested).
	if pub.SubmitterID != nil {
		t.Errorf("submitterId leaked: %v", *pub.SubmitterID)
	}
	for name, got := range map[string]*string{
		"what3words": pub.What3Words, "plusCode": pub.PlusCode, "landmark": pub.Landmark,
		"buildingId": pub.BuildingID, "buildingSource": pub.BuildingSource, "infraName": pub.InfraName,
		"location.what3words": pub.Location.What3Words, "location.plusCode": pub.Location.PlusCode,
		"location.landmark": pub.Location.Landmark, "location.buildingId": pub.Location.BuildingID,
		"location.buildingSource": pub.Location.BuildingSource,
	} {
		if got != nil {
			t.Errorf("%s leaked: %v", name, *got)
		}
	}
	if pub.GPSAccuracyMeters != nil || pub.Location.GPSAccuracyMeters != nil {
		t.Errorf("gpsAccuracyMeters leaked")
	}

	// Operational / PII / analyst-only fields must be cleared.
	if pub.Description != nil {
		t.Errorf("description leaked: %+v", pub.Description)
	}
	if pub.Assignee != nil || pub.TaskRef != nil || pub.Disposition != nil {
		t.Errorf("dispatch fields leaked: %v %v %v", pub.Assignee, pub.TaskRef, pub.Disposition)
	}
	if pub.TaskStatus != "" || pub.Severity != "" || pub.LifeSafety || len(pub.Clusters) != 0 {
		t.Errorf("task axis leaked: %q %q %v %v", pub.TaskStatus, pub.Severity, pub.LifeSafety, pub.Clusters)
	}
	if pub.AILevel != nil || pub.AIConfidence != nil || pub.Modular != nil {
		t.Errorf("AI/modular leaked: %v %v %s", pub.AILevel, pub.AIConfidence, pub.Modular)
	}
	if pub.Anonymization != (model.Anonymization{}) {
		t.Errorf("anonymization object leaked: %+v", pub.Anonymization)
	}

	// What a public damage map legitimately needs is KEPT.
	if pub.Damage != "severe" || pub.DamageTier != "partial" || pub.Verification != "verified" || pub.Place != "Saray Cd." {
		t.Errorf("public fields lost: %q %q %q %q", pub.Damage, pub.DamageTier, pub.Verification, pub.Place)
	}
	// photoUrl survives ONLY on a verified report (asserted unverified below).
	if pub.PhotoURL == nil {
		t.Errorf("verified report must keep its photoUrl")
	}
}

// TestPublicProjection_UnverifiedPhotoHidden locks the photo gate's read side:
// an unverified report's photoUrl is never advertised to the public tier.
func TestPublicProjection_UnverifiedPhotoHidden(t *testing.T) {
	for _, status := range []string{"pending", "flagged"} {
		rep := fullReport()
		rep.Verification = status
		if pub := publicProjection(rep); pub.PhotoURL != nil {
			t.Errorf("%s report leaked photoUrl: %v", status, *pub.PhotoURL)
		}
	}
}

// TestPublicProjection_UnresolvedStaysNull locks the Null-Island rule: a
// location-unresolved report keeps lat/lng nil through the projection — never
// a coarsened 0,0.
func TestPublicProjection_UnresolvedStaysNull(t *testing.T) {
	rep := fullReport()
	rep.Lat, rep.Lng = nil, nil
	rep.LocationResolved = false
	pub := publicProjection(rep)
	if pub.Lat != nil || pub.Lng != nil || pub.Location.Lat != nil || pub.Location.Lng != nil {
		t.Errorf("unresolved report must keep nil coords, got %v,%v / %v,%v",
			pub.Lat, pub.Lng, pub.Location.Lat, pub.Location.Lng)
	}
	if pub.LocationResolved {
		t.Errorf("locationResolved must stay false")
	}
}
