package model

import "testing"

func TestValidateScenarioName(t *testing.T) {
	for _, valid := range []string{"checkout", "trip-plan", "a1"} {
		if err := ValidateScenarioName(valid); err != nil {
			t.Fatalf("%q: %v", valid, err)
		}
	}
	for _, invalid := range []string{"Trip Plan", "-checkout", "checkout_1", ""} {
		if err := ValidateScenarioName(invalid); err == nil {
			t.Fatalf("expected %q to fail", invalid)
		}
	}
}

func TestNodeKeyStringIsStable(t *testing.T) {
	key := NodeKey{Service: "planner", Name: "provider.search", Kind: SpanKindClient}
	if got := key.String(); got != "planner/provider.search/CLIENT" {
		t.Fatalf("got %q", got)
	}
}
