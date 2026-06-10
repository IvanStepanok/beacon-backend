package model

// FormOption is one selectable answer in a modular form section. Value is the
// wire value the mobile client stores in the report's modular blob (the enum
// `.name.lowercase()` the app already sends today); Label is the Appendix-1
// display text.
type FormOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// FormSection is one modular capture-form question (an Appendix-1 secondary
// impact). Sections are OPTIONAL by default (the modular framing); a crisis can
// flip Required per section via its stored FormOverrides.
type FormSection struct {
	Key            string       `json:"key"`
	Title          string       `json:"title"`
	Type           string       `json:"type"` // single | multi
	Required       bool         `json:"required"`
	AllowOtherText bool         `json:"allowOtherText,omitempty"` // multi with an "other" free-text detail
	Options        []FormOption `json:"options"`
}

// FormSchema is the GET /form-schema response envelope: the resolved (defaults
// + per-crisis overrides) modular sections the capture flow should render.
type FormSchema struct {
	Sections []FormSection `json:"sections"`
}

// FormOverrides is a crisis's per-section adjustment, stored verbatim in
// crises.form_overrides (jsonb) and the PATCH /crises/{id}/form body: section
// keys in Required are flipped to required, keys in Disabled are dropped from
// the form entirely.
type FormOverrides struct {
	Required []string `json:"required"`
	Disabled []string `json:"disabled"`
}
