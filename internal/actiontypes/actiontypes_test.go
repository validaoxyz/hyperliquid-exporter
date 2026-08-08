package actiontypes

import "testing"

func TestNormalizeAndCategoryAreBounded(t *testing.T) {
	tests := []struct {
		raw      string
		label    string
		known    bool
		category string
	}{
		{raw: "order", label: "order", known: true, category: "trading"},
		{raw: "validatorL1UpdateReferenceOracle", label: "validatorL1UpdateReferenceOracle", known: true, category: "governance"},
		{raw: "noop", label: "noop", known: true, category: "system"},
		{raw: "random-address-or-payload", label: Other, known: false, category: Other},
		{raw: "", label: Other, known: false, category: Other},
	}
	for _, tt := range tests {
		label, known := Normalize(tt.raw)
		if label != tt.label || known != tt.known {
			t.Fatalf("Normalize(%q) = %q/%v", tt.raw, label, known)
		}
		if got := Category(label); got != tt.category {
			t.Fatalf("Category(%q) = %q, want %q", label, got, tt.category)
		}
	}
}

func TestEveryAllowlistedActionHasAnOperationalCategory(t *testing.T) {
	for action := range known {
		if got := Category(action); got == Other {
			t.Errorf("allowlisted action %q has no category", action)
		}
	}
}
