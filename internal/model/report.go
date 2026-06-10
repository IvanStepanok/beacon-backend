// Package model holds the canonical JSON contract. Field tags are camelCase and
// the Report object is a SUPERSET of what the mobile (KMP/@Serializable) and the
// dashboard (TS) each read: nested objects + flat aliases coexist so one JSON
// document deserializes cleanly into both clients (each ignores keys it doesn't know).
package model

import (
	"encoding/json"
	"time"
)

// Enum wire values are LOWERCASE (matches the export contract both clients already use).
// Damage is a 5-level ordinal grade aligned to EMS-98 / Copernicus EMS / UNOSAT.
var (
	DamageLevels  = []string{"none", "slight", "moderate", "severe", "destroyed"}
	DamageTiers   = []string{"minimal", "partial", "complete"} // challenge's required 3-level core indicator
	Verifications = []string{"pending", "verified", "flagged"}
	DebrisStates  = []string{"yes", "no", "unsure"}
	InfraTypesAll = []string{"residential", "commercial", "government", "utility", "transport", "community", "public", "other"}
	CrisisNatures = []string{"earthquake", "flood", "tsunami", "hurricane", "wildfire", "explosion", "chemical", "conflict", "civil_unrest"}
)

// DamageValuesAll = both vocabularies (5-level EMS-98 + 3-tier). A report's damage
// may be either, depending on the global capture scale; the server accepts both.
var DamageValuesAll = append(append([]string{}, DamageLevels...), DamageTiers...)

// DamageOrder ranks the 5-level grade for "worst"/escalation computation. It does
// NOT cover the 3-tier vocabulary (minimal/partial/complete) — a 'partial' report
// is NOT in this map and would rank 0 (== 'none'). For any "worst"/ranking that may
// see EITHER vocabulary (the global default scale is tier3), rank by TierOrder on
// the rollup tier instead, never by DamageOrder on the raw grade. See TierRank.
var DamageOrder = map[string]int{"none": 0, "slight": 1, "moderate": 2, "severe": 3, "destroyed": 4}

// TierOrder ranks the required 3-tier classification (minimal < partial < complete)
// for vocabulary-agnostic "worst" / escalation computation.
var TierOrder = map[string]int{"minimal": 0, "partial": 1, "complete": 2}

// RollupTier maps either vocabulary to the required 3-tier classification.
func RollupTier(damage string) string {
	switch damage {
	case "none", "slight", "minimal":
		return "minimal"
	case "moderate", "severe", "partial":
		return "partial"
	case "destroyed", "complete":
		return "complete"
	default:
		return "minimal"
	}
}

// TierRank ranks a raw damage grade (either vocabulary) by its 3-tier rollup, so
// "worst-of" comparisons are correct no matter which capture scale produced the
// grade. minimal=0 < partial=1 < complete=2.
func TierRank(damage string) int { return TierOrder[RollupTier(damage)] }

type PhotoRef struct {
	LocalPath string  `json:"localPath"`
	RemoteURL *string `json:"remoteUrl,omitempty"`
	SizeBytes int64   `json:"sizeBytes"`
}

type ReportLocation struct {
	Lat               *float64 `json:"lat"`
	Lng               *float64 `json:"lng"`
	BuildingID        *string  `json:"buildingId,omitempty"`
	BuildingSource    *string  `json:"buildingSource,omitempty"` // "footprint" only for a real tapped footprint polygon
	What3Words        *string  `json:"what3words,omitempty"`     // legacy alias of PlusCode (same value)
	PlusCode          *string  `json:"plusCode,omitempty"`
	Landmark          *string  `json:"landmark,omitempty"`
	GPSAccuracyMeters *float64 `json:"gpsAccuracyMeters,omitempty"`
}

type ReportDescription struct {
	Original     string `json:"original"`
	OriginalLang string `json:"originalLang"`
	// Translated is always emitted (dashboard reads it as the primary note); the
	// server coalesces to the original text when there is no translation yet.
	Translated     string  `json:"translated"`
	TranslatedLang *string `json:"translatedLang,omitempty"`
}

// AdminRef is one administrative level (P-code + name) — the COD-AB join key.
type AdminRef struct {
	Pcode string `json:"pcode"`
	Name  string `json:"name"`
}

// AdminChain is the resolved ADM0–ADM3 P-code chain stamped on a report.
type AdminChain struct {
	Adm0 *AdminRef `json:"adm0,omitempty"`
	Adm1 *AdminRef `json:"adm1,omitempty"`
	Adm2 *AdminRef `json:"adm2,omitempty"`
	Adm3 *AdminRef `json:"adm3,omitempty"`
}

type Anonymization struct {
	Anonymous     bool `json:"anonymous"`
	ExifStripped  bool `json:"exifStripped"`
	FacesBlurred  bool `json:"facesBlurred"`
	PlatesBlurred bool `json:"platesBlurred"`
}

// DefaultAnonymization mirrors the privacy guarantees that are ACTUALLY implemented.
// ExifStripped is real (the mobile client strips EXIF on capture). Face/plate
// blurring is NOT implemented anywhere, so FacesBlurred/PlatesBlurred are FALSE —
// the API must never claim a privacy guarantee it does not deliver. (The whole
// anonymization object is also stripped from the public projection; analyst views
// now see the honest false values.)
func DefaultAnonymization() Anonymization {
	return Anonymization{Anonymous: true, ExifStripped: true, FacesBlurred: false, PlatesBlurred: false}
}

// Report is the canonical output document.
type Report struct {
	ID             string  `json:"id"`
	IdempotencyKey string  `json:"idempotencyKey"`
	CrisisID       string  `json:"crisisId"`
	SubmitterID    *string `json:"submitterId,omitempty"`

	Damage          string `json:"damage"`          // raw grade (3-tier OR 5-level EMS-98)
	DamageTier      string `json:"damageTier"`      // required 3-level rollup (minimal|partial|complete), always present
	PossiblyDamaged bool   `json:"possiblyDamaged"` // reporter unsure / resolves satellite "possibly damaged" class
	Verification    string `json:"verification"`
	Debris          string `json:"debris"`

	InfraTypes       []string `json:"infraTypes"`
	Infra            []string `json:"infra"` // dashboard alias (same array)
	InfraOtherDetail *string  `json:"infraOtherDetail,omitempty"`
	InfraName        *string  `json:"infraName,omitempty"` // name/details of the infrastructure (any type, e.g. "Cumhuriyet Primary School")
	CrisisNature     []string `json:"crisisNature"`
	Crisis           []string `json:"crisis"` // dashboard alias (same array)

	// Flat geo (dashboard) + nested location (mobile) — both emitted.
	// Lat/Lng are POINTERS so a location-unresolved (landmark-only) report serializes
	// lat:null,lng:null rather than [0,0] (Null Island). LocationResolved is true when a
	// real GPS fix or tapped footprint produced a point; false for landmark-only reports.
	Lat               *float64       `json:"lat"`
	Lng               *float64       `json:"lng"`
	LocationResolved  bool           `json:"locationResolved"`
	GPSAccuracyMeters *float64       `json:"gpsAccuracyMeters,omitempty"`
	BuildingID        *string        `json:"buildingId,omitempty"`
	BuildingSource    *string        `json:"buildingSource,omitempty"` // "footprint" ONLY when a real footprint polygon was tapped
	What3Words        *string        `json:"what3words,omitempty"`     // legacy alias of PlusCode (same value, kept for older mobile builds)
	PlusCode          *string        `json:"plusCode,omitempty"`
	Landmark          *string        `json:"landmark,omitempty"`
	Place             string         `json:"place"`
	PhotoURL          *string        `json:"photoUrl,omitempty"`
	Location          ReportLocation `json:"location"`

	// Admin-boundary chain (reverse-geocoded) + flat P-code aliases for filtering.
	Admin     *AdminChain `json:"admin,omitempty"`
	Adm1Pcode *string     `json:"adm1Pcode,omitempty"`
	Adm2Pcode *string     `json:"adm2Pcode,omitempty"`
	Adm3Pcode *string     `json:"adm3Pcode,omitempty"`

	Version            int     `json:"version"`
	SupersedesReportID *string `json:"supersedesReportId,omitempty"`

	Description  *ReportDescription `json:"description,omitempty"`
	AILevel      *string            `json:"aiLevel,omitempty"`
	AIConfidence *int               `json:"aiConfidence,omitempty"`

	Photos        []PhotoRef      `json:"photos"`
	SizeBytes     int64           `json:"sizeBytes"`
	SizeMb        float64         `json:"sizeMb"`
	Modular       json.RawMessage `json:"modular,omitempty"`
	Anonymization Anonymization   `json:"anonymization"`

	// Affected-sector tags (OCHA humanitarian clusters) — an optional data dimension.
	Clusters []string `json:"clusters"`

	IsMine bool            `json:"isMine"`
	Synced bool            `json:"synced"`
	Sync   json.RawMessage `json:"sync"` // discriminated union {type:Queued|Syncing|Synced|Failed,...}

	AgeMin     int       `json:"ageMin"`
	CapturedAt time.Time `json:"capturedAt"`
	CreatedAt  time.Time `json:"createdAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

// SubmitReportRequest is the lenient inbound shape. It accepts flat lat/lng OR a
// nested location, and infra/crisis aliases, so both clients can POST as-is.
type SubmitReportRequest struct {
	ID             string `json:"id"`
	IdempotencyKey string `json:"idempotencyKey"`
	CrisisID       string `json:"crisisId"`

	Damage          string `json:"damage"`
	PossiblyDamaged bool   `json:"possiblyDamaged"`
	Debris          string `json:"debris"`

	InfraTypes       []string `json:"infraTypes"`
	Infra            []string `json:"infra"`
	InfraOtherDetail *string  `json:"infraOtherDetail"`
	InfraName        *string  `json:"infraName"`
	CrisisNature     []string `json:"crisisNature"`
	Crisis           []string `json:"crisis"`

	// Lat/Lng are nullable: a landmark-only report sends lat:null,lng:null with
	// locationResolved:false. AccuracyMeters is the C4 wire name for the GPS fix
	// accuracy; it coalesces into the existing gpsAccuracyMeters / gps_accuracy_m.
	Lat              *float64        `json:"lat"`
	Lng              *float64        `json:"lng"`
	LocationResolved *bool           `json:"locationResolved"`
	AccuracyMeters   *float64        `json:"accuracyMeters"`
	Location         *ReportLocation `json:"location"`

	BuildingID        *string  `json:"buildingId"`
	BuildingSource    *string  `json:"buildingSource"` // "footprint" ONLY when a real footprint polygon was tapped
	What3Words        *string  `json:"what3words"`     // legacy submit key — accepted as a plusCode fallback
	PlusCode          *string  `json:"plusCode"`
	Landmark          *string  `json:"landmark"`
	GPSAccuracyMeters *float64 `json:"gpsAccuracyMeters"`
	Place             string   `json:"place"`

	Description   *ReportDescription `json:"description"`
	AILevel       *string            `json:"aiLevel"`
	AIConfidence  *int               `json:"aiConfidence"`
	Photos        []PhotoRef         `json:"photos"`
	SizeBytes     *int64             `json:"sizeBytes"`
	Modular       json.RawMessage    `json:"modular"`
	Anonymization *Anonymization     `json:"anonymization"`
	Sync          json.RawMessage    `json:"sync"`
	CapturedAt    *time.Time         `json:"capturedAt"`
	Clusters      []string           `json:"clusters"` // reporter-suggested affected sector(s)
}

// Clusters is the closed set of OCHA humanitarian sectors a report may be tagged with.
var Clusters = []string{"slsc", "health", "wash", "education", "food_security", "protection", "logistics", "nutrition", "etc", "cccm", "early_recovery"}

// VerificationRequest is the analyst PATCH body. `status` is the canonical key;
// `verification` is the legacy alias older dashboards still send (status wins when
// both are present). Setting status=verified on a report with no photo is rejected
// (409 photo_required) unless force=true; note and force land in the audit trail.
type VerificationRequest struct {
	Status       string  `json:"status"`
	Verification string  `json:"verification"` // legacy alias for status
	Note         *string `json:"note"`
	Force        bool    `json:"force"`
}

// ListResponse is the paginated reports envelope.
type ListResponse struct {
	Items      []Report `json:"items"`
	Total      int      `json:"total"`      // matches current filters
	GrandTotal int      `json:"grandTotal"` // unfiltered total for the crisis
	NextCursor *string  `json:"nextCursor,omitempty"`
}
