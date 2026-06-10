package service

import (
	"testing"

	"github.com/stepanok/beacon-server/internal/model"
)

// TestDefaultFormSections locks the default modular schema: the three
// Appendix-1 sections in order, every section OPTIONAL by default, and the
// option values IDENTICAL to the wire values the mobile client already sends
// in the modular blob (enum .name.lowercase() — never invent new ones).
func TestDefaultFormSections(t *testing.T) {
	sections := DefaultFormSections()
	if len(sections) != 3 {
		t.Fatalf("want 3 default sections, got %d", len(sections))
	}

	wantValues := map[string][]string{
		"electricity":    {"none_observed", "minor", "moderate", "severe", "destroyed", "unknown"},
		"healthServices": {"fully_functional", "partially_functional", "largely_disrupted", "not_functioning", "unknown"},
		"pressingNeeds":  {"food_water", "cash", "healthcare", "shelter", "livelihoods", "wash", "basic_services", "protection", "local_support", "other"},
	}
	wantTypes := map[string]string{"electricity": "single", "healthServices": "single", "pressingNeeds": "multi"}
	order := []string{"electricity", "healthServices", "pressingNeeds"}

	for i, s := range sections {
		if s.Key != order[i] {
			t.Errorf("section %d key = %q, want %q", i, s.Key, order[i])
		}
		if s.Required {
			t.Errorf("section %q must default to optional (required=false)", s.Key)
		}
		if s.Type != wantTypes[s.Key] {
			t.Errorf("section %q type = %q, want %q", s.Key, s.Type, wantTypes[s.Key])
		}
		want := wantValues[s.Key]
		if len(s.Options) != len(want) {
			t.Fatalf("section %q has %d options, want %d", s.Key, len(s.Options), len(want))
		}
		for j, o := range s.Options {
			if o.Value != want[j] {
				t.Errorf("section %q option %d value = %q, want %q (existing mobile wire value)", s.Key, j, o.Value, want[j])
			}
			if o.Label == "" {
				t.Errorf("section %q option %q has an empty label", s.Key, o.Value)
			}
		}
		// Only the multi-select needs the "Other → specify" free-text affordance.
		if got, want := s.AllowOtherText, s.Key == "pressingNeeds"; got != want {
			t.Errorf("section %q allowOtherText = %v, want %v", s.Key, got, want)
		}
	}
}

// TestApplyFormOverrides locks the override semantics: nil = defaults verbatim,
// required flips only the listed sections, disabled drops sections entirely,
// and unknown/stale keys in a stored override row are ignored (fail-open).
func TestApplyFormOverrides(t *testing.T) {
	// nil overrides → defaults unchanged.
	got := ApplyFormOverrides(DefaultFormSections(), nil)
	if len(got) != 3 || got[0].Required || got[1].Required || got[2].Required {
		t.Errorf("nil overrides must return the defaults unchanged, got %+v", got)
	}

	// required flips exactly the listed section.
	got = ApplyFormOverrides(DefaultFormSections(), &model.FormOverrides{Required: []string{"electricity"}})
	if len(got) != 3 {
		t.Fatalf("required override must not drop sections, got %d", len(got))
	}
	if !got[0].Required || got[1].Required || got[2].Required {
		t.Errorf("only electricity must become required, got %v %v %v", got[0].Required, got[1].Required, got[2].Required)
	}

	// disabled removes the section from the form.
	got = ApplyFormOverrides(DefaultFormSections(), &model.FormOverrides{Disabled: []string{"healthServices"}})
	if len(got) != 2 || got[0].Key != "electricity" || got[1].Key != "pressingNeeds" {
		t.Errorf("disabling healthServices must leave [electricity pressingNeeds], got %+v", got)
	}

	// combined + unknown keys ignored.
	got = ApplyFormOverrides(DefaultFormSections(), &model.FormOverrides{
		Required: []string{"pressingNeeds", "ghost"},
		Disabled: []string{"electricity", "stale"},
	})
	if len(got) != 2 || got[0].Key != "healthServices" || got[1].Key != "pressingNeeds" {
		t.Fatalf("combined overrides resolved wrong sections: %+v", got)
	}
	if got[0].Required || !got[1].Required {
		t.Errorf("only pressingNeeds must be required, got %v %v", got[0].Required, got[1].Required)
	}
}

// TestValidateFormOverrides locks the PATCH body validation: only known section
// keys are accepted and a key cannot be both required and disabled.
func TestValidateFormOverrides(t *testing.T) {
	ok := []model.FormOverrides{
		{},
		{Required: []string{"electricity", "healthServices", "pressingNeeds"}},
		{Disabled: []string{"pressingNeeds"}},
		{Required: []string{"electricity"}, Disabled: []string{"healthServices"}},
	}
	for _, ov := range ok {
		if err := ValidateFormOverrides(ov); err != nil {
			t.Errorf("ValidateFormOverrides(%+v) = %v, want nil", ov, err)
		}
	}

	bad := []model.FormOverrides{
		{Required: []string{"nope"}},
		{Disabled: []string{"what3words"}},
		{Required: []string{"electricity"}, Disabled: []string{"electricity"}},
	}
	for _, ov := range bad {
		err := ValidateFormOverrides(ov)
		if err == nil {
			t.Errorf("ValidateFormOverrides(%+v) = nil, want error", ov)
			continue
		}
		if _, isVE := err.(ValidationError); !isVE {
			t.Errorf("ValidateFormOverrides(%+v) error type %T, want ValidationError (→ 400)", ov, err)
		}
	}
}
