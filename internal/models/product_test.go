package models

import "testing"

func TestLegacyVariantIDsAreStableWithoutMutatingStoredData(t *testing.T) {
	p := Product{ID: "product", Variants: []ProductVariant{{Name: "Small", Price: 10}, {Name: "Large", Price: 20}}}
	p.ExposeVariantIDs()
	first, second := p.Variants[0].ID, p.Variants[1].ID
	q := Product{ID: "product", Variants: []ProductVariant{{Name: "Large", Price: 30}, {Name: "Small", Price: 15}}}
	q.ExposeVariantIDs()
	if q.Variants[0].ID != second || q.Variants[1].ID != first {
		t.Fatal("price change/reordering changed legacy IDs")
	}
	p.Variants[0].Name = "Renamed"
	p.ExposeVariantIDs()
	if p.Variants[0].ID != first {
		t.Fatal("an established ID changed after rename")
	}
}
