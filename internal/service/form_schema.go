package service

import (
	"fmt"

	"github.com/stepanok/beacon-server/internal/model"
)

// DefaultFormSections is the built-in modular capture form: the three
// Appendix-1 secondary-impact questions. Every section defaults to OPTIONAL
// (the modular framing — the required core indicators are the fixed report
// fields, not these); a crisis flips requiredness via its stored overrides.
//
// The option VALUES are the exact wire values the mobile client already writes
// into the report's modular blob (enum `.name.lowercase()`, also the documented
// ModularImpacts enums in openapi.yaml) — the schema must never invent new wire
// values for existing options or stored blobs would stop matching the form.
// The LABELS are the verbatim Appendix-1 answer texts.
func DefaultFormSections() []model.FormSection {
	return []model.FormSection{
		{
			Key:      "electricity",
			Title:    "Electricity infrastructure condition",
			Type:     "single",
			Required: false,
			Options: []model.FormOption{
				{Value: "none_observed", Label: "No damage observed"},
				{Value: "minor", Label: "Minor damage (service disruptions but quickly repairable)"},
				{Value: "moderate", Label: "Moderate damage (partial outages requiring repairs)"},
				{Value: "severe", Label: "Severe damage (major infrastructure damaged, prolonged outages)"},
				{Value: "destroyed", Label: "Completely destroyed (no electricity infrastructure functioning)"},
				{Value: "unknown", Label: "Unknown/cannot be assessed"},
			},
		},
		{
			Key:      "healthServices",
			Title:    "Health services functioning",
			Type:     "single",
			Required: false,
			Options: []model.FormOption{
				{Value: "fully_functional", Label: "Fully functional"},
				{Value: "partially_functional", Label: "Partially functional"},
				{Value: "largely_disrupted", Label: "Largely disrupted"},
				{Value: "not_functioning", Label: "Not functioning at all"},
				{Value: "unknown", Label: "Unknown"},
			},
		},
		{
			Key:            "pressingNeeds",
			Title:          "Most pressing needs",
			Type:           "multi",
			Required:       false,
			AllowOtherText: true, // "Other, please specify" → pressingNeedsOther in the blob
			Options: []model.FormOption{
				{Value: "food_water", Label: "Food assistance and safe drinking water"},
				{Value: "cash", Label: "Cash or financial assistance"},
				{Value: "healthcare", Label: "Access to healthcare and essential medicines"},
				{Value: "shelter", Label: "Shelter, housing repair, or temporary accommodation"},
				{Value: "livelihoods", Label: "Restoration of livelihoods or income sources"},
				{Value: "wash", Label: "Water, sanitation, and hygiene (toilets, washing facilities)"},
				{Value: "basic_services", Label: "Restoration of basic services and infrastructure (electricity, roads, schools)"},
				{Value: "protection", Label: "Protection services and psychosocial support"},
				{Value: "local_support", Label: "Support from local authorities and community organizations"},
				{Value: "other", Label: "Other"},
			},
		},
	}
}

// knownFormSectionKeys is the validation set for PATCH /crises/{id}/form,
// derived from the defaults so a newly-added section is accepted automatically.
var knownFormSectionKeys = func() map[string]bool {
	m := map[string]bool{}
	for _, s := range DefaultFormSections() {
		m[s.Key] = true
	}
	return m
}()

// ApplyFormOverrides resolves a crisis's stored overrides over the default
// sections: keys in Disabled are dropped from the form entirely; keys in
// Required are flipped to required. Unknown/stale keys in stored overrides are
// ignored (fail-open to the defaults — a bad override row must never break the
// public capture form). nil overrides return the sections unchanged.
func ApplyFormOverrides(sections []model.FormSection, ov *model.FormOverrides) []model.FormSection {
	if ov == nil {
		return sections
	}
	disabled := map[string]bool{}
	for _, k := range ov.Disabled {
		disabled[k] = true
	}
	required := map[string]bool{}
	for _, k := range ov.Required {
		required[k] = true
	}
	out := make([]model.FormSection, 0, len(sections))
	for _, s := range sections {
		if disabled[s.Key] {
			continue
		}
		if required[s.Key] {
			s.Required = true
		}
		out = append(out, s)
	}
	return out
}

// ValidateFormOverrides checks a PATCH /crises/{id}/form body: every key must
// name a known section, and a key cannot be both required and disabled.
func ValidateFormOverrides(ov model.FormOverrides) error {
	disabled := map[string]bool{}
	for _, k := range ov.Disabled {
		if !knownFormSectionKeys[k] {
			return ValidationError{fmt.Sprintf("unknown form section %q", k)}
		}
		disabled[k] = true
	}
	for _, k := range ov.Required {
		if !knownFormSectionKeys[k] {
			return ValidationError{fmt.Sprintf("unknown form section %q", k)}
		}
		if disabled[k] {
			return ValidationError{fmt.Sprintf("section %q cannot be both required and disabled", k)}
		}
	}
	return nil
}
