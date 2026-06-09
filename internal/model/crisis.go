package model

import (
	"encoding/json"
	"time"
)

// Crisis emits BOTH lat/lng (dashboard) and centerLat/centerLng (mobile), plus
// startedAt (ISO, authoritative) and startedAgoHrs (computed int the dashboard reads).
type Crisis struct {
	ID            string    `json:"id"`
	Title         string    `json:"title"`
	Area          string    `json:"area"`
	Nature        string    `json:"nature"`
	Lat           float64   `json:"lat"`
	Lng           float64   `json:"lng"`
	CenterLat     float64   `json:"centerLat"`
	CenterLng     float64   `json:"centerLng"`
	Source        string    `json:"source"`
	StartedAt     time.Time `json:"startedAt"`
	StartedAgoHrs int       `json:"startedAgoHrs"`
	Glide         *string   `json:"glide,omitempty"`         // cross-org disaster event key
	ResponseLevel *int      `json:"responseLevel,omitempty"` // UNDP corporate crisis Level 1/2/3

	// Spatial + temporal extent and lifecycle (a crisis is a discrete EVENT).
	RadiusKm    float64    `json:"radiusKm"`              // coverage radius around the center point (km)
	EndedAt     *time.Time `json:"endedAt,omitempty"`     // nil = still ongoing
	Status      string     `json:"status"`               // active | proposed (emergent, awaiting analyst) | closed | dismissed
	ResponseID  *string    `json:"responseId,omitempty"` // optional umbrella response
	ReportCount int        `json:"reportCount"`          // denormalized cluster size

	// Set only on /crises/near responses (relative to the queried point).
	DistanceKm *float64 `json:"distanceKm,omitempty"`
	Covers     *bool    `json:"covers,omitempty"` // true if the queried point is within radiusKm
}

// PdnaRow is one cell of the PDNA-ready damage-count aggregate: report COUNTS by
// damage grade for a (sector × admin area) pair — a damage-count input for a PDNA,
// NOT a loss/cost estimation (no monetary / replacement-value figures).
//
// Both vocabularies are reported. The 3-tier rollup (Minimal/Partial/Complete) is
// the CANONICAL breakdown — it is vocabulary-agnostic and Minimal+Partial+Complete
// always == Total (every report rolls into exactly one tier). The 5-level fields
// (None…Destroyed) are kept as detail; they only carry counts for reports captured
// on the EMS-98 scale and DO NOT sum to Total when tier3-scale reports are present.
type PdnaRow struct {
	AdmPcode string `json:"admPcode"`
	AdmName  string `json:"admName"`
	Sector   string `json:"sector"`
	// Canonical 3-tier rollup (sums to Total).
	Minimal  int `json:"minimal"`
	Partial  int `json:"partial"`
	Complete int `json:"complete"`
	// 5-level EMS-98 detail (only populated for ems98-scale reports).
	None      int `json:"none"`
	Slight    int `json:"slight"`
	Moderate  int `json:"moderate"`
	Severe    int `json:"severe"`
	Destroyed int `json:"destroyed"`
	Total     int `json:"total"`
}

type DangerZone struct {
	ID       string `json:"id"`
	CrisisID string `json:"crisisId"`
	Name     string `json:"name"`
	Note     string `json:"note"`
	Severity string `json:"severity"`
}

type Building struct {
	ID            string   `json:"id"`
	CrisisID      string   `json:"crisisId"`
	Lat           *float64 `json:"lat,omitempty"`
	Lng           *float64 `json:"lng,omitempty"`
	CurrentDamage *string  `json:"current,omitempty"`
}

// BuildingVersion is one entry in a building's real damage history.
type BuildingVersion struct {
	ReportID  string    `json:"reportId"`
	V         int       `json:"v"`
	Damage    string    `json:"damage"`
	At        time.Time `json:"at"`
	AgeMin    int       `json:"ageMin"`
	IsCurrent bool      `json:"isCurrent"`
	By        string    `json:"by"`
	Note      string    `json:"note"`
}

type BuildingTimeline struct {
	BuildingID string            `json:"buildingId"`
	Current    *string           `json:"current"`
	Versions   []BuildingVersion `json:"versions"`
}

// AreaGroup is per-place report count + worst damage. Worst is the worst RAW grade
// (either vocabulary), chosen by ranking on the 3-tier rollup so a tier-3 'partial'
// or 'complete' report is never mis-ranked below an EMS-98 'severe'. WorstTier is
// that grade's canonical rollup (minimal|partial|complete).
type AreaGroup struct {
	Area      string `json:"area"`
	Count     int    `json:"count"`
	Worst     string `json:"worst"`
	WorstTier string `json:"worstTier"`
}

type Profile struct {
	AnonymousID   string          `json:"anonymousId"`
	Alias         *string         `json:"alias,omitempty"`
	ReportCount   int             `json:"reportCount"`
	BuildingCount int             `json:"buildingCount"`
	Points        int             `json:"points"`
	Badges        json.RawMessage `json:"badges"`
}

type PointsRequest struct {
	Points int    `json:"points"`
	Reason string `json:"reason"`
}

// ── stats/overview (computed entirely in SQL) ──────────────────────────

// DamageCounts is the 5-level EMS-98 detail breakdown. These counts ONLY cover
// reports captured on the ems98 scale; when the global scale is tier3 (the default)
// most reports land in DamageTierCounts instead, so these 5 fields do NOT sum to
// TotalReports. Use DamageTierCounts for the canonical, always-complete breakdown.
type DamageCounts struct {
	None      int `json:"none"`
	Slight    int `json:"slight"`
	Moderate  int `json:"moderate"`
	Severe    int `json:"severe"`
	Destroyed int `json:"destroyed"`
}

// DamageTierCounts is the CANONICAL, vocabulary-agnostic damage breakdown: every
// report (both scales) rolls into exactly one tier, so Minimal+Partial+Complete
// always equals TotalReports. This is the breakdown clients should chart by default.
type DamageTierCounts struct {
	Minimal  int `json:"minimal"`
	Partial  int `json:"partial"`
	Complete int `json:"complete"`
}

type VerificationCounts struct {
	Verified int `json:"verified"`
	Pending  int `json:"pending"`
	Flagged  int `json:"flagged"`
}

type TimeBucket struct {
	Hour  int `json:"hour"`
	Count int `json:"count"`
}

// TaskCounts powers the dispatch board (open work by stage).
// Keyed by the task_status wire values so clients can index by TaskStatus.
type TaskCounts struct {
	New        int `json:"new"`
	Triaged    int `json:"triaged"`
	Assigned   int `json:"assigned"`
	InProgress int `json:"in_progress"`
	Resolved   int `json:"resolved"`
	Closed     int `json:"closed"`
}

type StatsOverview struct {
	TotalReports int `json:"totalReports"`
	// DamageTierCounts is the canonical breakdown (sums to TotalReports). DamageCounts
	// is the 5-level EMS-98 detail (only ems98-scale reports; does NOT sum to total).
	DamageTierCounts   DamageTierCounts   `json:"damageTierCounts"`
	DamageCounts       DamageCounts       `json:"damageCounts"`
	VerificationCounts VerificationCounts `json:"verificationCounts"`
	SyncedCount        int                `json:"syncedCount"`
	SyncedPct          int                `json:"syncedPct"`
	// Headline damage percentages are computed off the CANONICAL tier rollup so they
	// stay correct under either capture scale. CompletePct = complete tier (the
	// tier-3 'complete' / EMS-98 'destroyed'). SeverePlusPct = partial+complete tiers
	// (the "heavy damage" headline: EMS-98 moderate/severe/destroyed + tier-3 partial/complete).
	CompletePct   int `json:"completePct"`
	DestroyedPct  int `json:"destroyedPct"` // alias of completePct (kept for dashboard compatibility)
	SeverePlusPct int `json:"severePlusPct"`

	TaskCounts     TaskCounts   `json:"taskCounts"`
	LifeSafetyOpen int          `json:"lifeSafetyOpen"` // open life-safety tasks (fast lane)
	Areas          []AreaGroup  `json:"areas"`
	TimeSeries     []TimeBucket `json:"timeSeries"`
	Recent         []Report     `json:"recent"`
}

// OfflineBundle is a downloadable offline pack manifest entry (mobile).
type OfflineBundle struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	Type       string `json:"type"`
	BytesTotal int64  `json:"bytesTotal"`
	State      string `json:"state"`
}
