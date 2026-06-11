package model

// Analyst roles (reporters are anonymous, not users). Ordered low→high oversight.
const (
	RoleFieldValidator  = "field_validator"
	RoleCOAnalyst       = "co_analyst"
	RoleRegionalAnalyst = "regional_analyst"
	RoleCrisisAdmin     = "crisis_admin"
	RoleExternalViewer  = "external_viewer"
)

var Roles = []string{RoleFieldValidator, RoleCOAnalyst, RoleRegionalAnalyst, RoleCrisisAdmin, RoleExternalViewer}

var RoleLabels = map[string]string{
	RoleFieldValidator:  "Field validator",
	RoleCOAnalyst:       "Country Office analyst",
	RoleRegionalAnalyst: "Regional Bureau analyst",
	RoleCrisisAdmin:     "Crisis Bureau admin",
	RoleExternalViewer:  "External viewer",
}

// User is the analyst account (password hash never serialized).
type User struct {
	ID          string   `json:"id"`
	Email       string   `json:"email"`
	Name        string   `json:"name"`
	Role        string   `json:"role"`
	Region      *string  `json:"region,omitempty"`
	CrisisScope []string `json:"crisisScope"`
}

// CanMutate: every analyst role except the read-only external viewer may
// verify/dispatch. (Field validators are limited to their scope by ScopeAllows.)
func (u User) CanMutate() bool { return u.Role != RoleExternalViewer && u.Role != "" }

// IsViewerTier reports whether the user is a low-trust / read-only viewer who must
// receive the COARSENED public projection (no exact coords / PII / operational
// fields) even when authenticated and in scope. Only external_viewer qualifies; the
// real analyst roles (field_validator, co_analyst, regional_analyst, crisis_admin)
// keep full precision. An unknown/empty role is treated as viewer-tier (fail-closed).
func (u User) IsViewerTier() bool {
	switch u.Role {
	case RoleFieldValidator, RoleCOAnalyst, RoleRegionalAnalyst, RoleCrisisAdmin:
		return false
	default:
		return true
	}
}

// roleRank orders analyst roles by oversight level (higher = more authority).
// Used to gate org-wide actions (e.g. flipping the global damage scale) to the
// senior roles only.
var roleRank = map[string]int{
	RoleFieldValidator:  1,
	RoleCOAnalyst:       2,
	RoleRegionalAnalyst: 3,
	RoleCrisisAdmin:     4,
	RoleExternalViewer:  0, // read-only, lowest
}

// CanManageGlobalConfig reports whether the user may change ORG-WIDE settings that
// affect every client/crisis (the org-wide app_settings store). Restricted to the
// senior oversight roles: Regional Bureau analyst and Crisis Bureau admin. Field
// validators, CO analysts and external viewers are denied.
func (u User) CanManageGlobalConfig() bool {
	return roleRank[u.Role] >= roleRank[RoleRegionalAnalyst]
}

// CanManageCrisis reports whether the user may change a crisis's LIFECYCLE
// (confirm/dismiss/close/reopen an emergent proposal). Restricted to the senior
// oversight roles — Regional Bureau analyst and Crisis Bureau admin — mirroring
// CanManageGlobalConfig. A field_validator or CO analyst keeps verify/task within
// scope but may NOT flip a crisis's status; an external_viewer (rank 0) cannot
// reach mutators at all.
func (u User) CanManageCrisis() bool {
	return roleRank[u.Role] >= roleRank[RoleRegionalAnalyst]
}

// ScopeAll: the user may see every crisis (Regional Bureau / Crisis Bureau / a
// viewer granted org-wide read).
func (u User) ScopeAll() bool {
	for _, c := range u.CrisisScope {
		if c == "*" {
			return true
		}
	}
	return false
}

// ScopeAllows reports whether the user may access the given crisis.
func (u User) ScopeAllows(crisisID string) bool {
	if u.ScopeAll() {
		return true
	}
	for _, c := range u.CrisisScope {
		if c == crisisID {
			return true
		}
	}
	return false
}

type LoginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type LoginResponse struct {
	Token string `json:"token"`
	User  User   `json:"user"`
}
