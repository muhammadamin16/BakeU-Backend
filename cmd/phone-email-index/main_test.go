package main

import "testing"

func TestStagingApplyGuard(t *testing.T) {
	for _, v := range []struct {
		env, db, confirmation string
		want                  bool
	}{
		{"staging", "bakeu_staging", "bakeu_staging", true},
		{"production", "bakeu_staging", "bakeu_staging", false},
		{"staging", "bakeu_production", "bakeu_production", false},
		{"staging", "bakeu_staging", "", false},
		{"staging", "bakeu_staging", "other_staging", false},
	} {
		if got := stagingAllowed(v.env, v.db, v.confirmation); got != v.want {
			t.Fatalf("guard: %v", v)
		}
	}
}
