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
	RadiusKm    float64    `json:"radiusKm"`             // coverage radius around the center point (km)
	EndedAt     *time.Time `json:"endedAt,omitempty"`    // nil = still ongoing
	Status      string     `json:"status"`               // active | proposed (emergent, awaiting analyst) | closed | dismissed
	ResponseID  *string    `json:"responseId,omitempty"` // optional umbrella response
	ReportCount int        `json:"reportCount"`          // denormalized cluster size

	// DistinctSubmitters is the count of distinct devices/submitters behind this
	// crisis's reports — the corroboration signal the review queue ranks on (an
	// emergent cluster from many independent reporters is far stronger than one
	// device posting repeatedly). It is also the quantity the emergent threshold
	// (BEACON_EMERGENT_MIN_REPORTS) is measured against at formation time.
	DistinctSubmitters int `json:"distinctSubmitters"`

	// Set only on /crises/near responses (relative to the queried point).
	DistanceKm *float64 `json:"distanceKm,omitempty"`
	Covers     *bool    `json:"covers,omitempty"` // true if the queried point is within radiusKm
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

	// H3 hotspot geometry — populated only by the H3 aggregation (AreaGroupsH3); the
	// place-based grouping leaves these nil (so the response stays identical). H3 is the
	// resolution-8 cell id; Lat/Lng its report centroid for client rendering. POINTERS,
	// not float64+omitempty, so a legitimate 0.0 centroid (equator / prime meridian) is
	// emitted rather than silently dropped.
	H3  string   `json:"h3,omitempty"`
	Lat *float64 `json:"lat,omitempty"`
	Lng *float64 `json:"lng,omitempty"`
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

// DamageTierCounts is the canonical damage breakdown: every report rolls into
// exactly one of the 3 mandated tiers, so Minimal+Partial+Complete always equals
// TotalReports. This is the breakdown clients chart.
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

// TimeBucket is one activity bucket; `hour` is the bucket index in
// StatsOverview.TimeSeriesUnit steps ago (0 = now). The json key predates the
// adaptive daily bucketing and is kept for dashboard compatibility.
type TimeBucket struct {
	Hour  int `json:"hour"`
	Count int `json:"count"`
}

type StatsOverview struct {
	TotalReports int `json:"totalReports"`
	// DamageTierCounts is the canonical breakdown (Minimal+Partial+Complete == TotalReports).
	DamageTierCounts   DamageTierCounts   `json:"damageTierCounts"`
	VerificationCounts VerificationCounts `json:"verificationCounts"`
	SyncedCount        int                `json:"syncedCount"`
	SyncedPct          int                `json:"syncedPct"`
	// Headline damage percentages off the tier rollup. CompletePct = the complete tier;
	// SeverePlusPct = partial+complete (the "heavy damage" headline).
	CompletePct   int `json:"completePct"`
	DestroyedPct  int `json:"destroyedPct"` // alias of completePct (kept for dashboard compatibility)
	SeverePlusPct int `json:"severePlusPct"`

	Areas          []AreaGroup  `json:"areas"`
	TimeSeries     []TimeBucket `json:"timeSeries"`
	TimeSeriesUnit string       `json:"timeSeriesUnit"` // "hour" | "day" — see store.Reports.TimeSeries
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
