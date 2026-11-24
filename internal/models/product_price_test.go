package models

import (
	"math"
	"testing"
)

func TestProductPriceContract(t *testing.T) {
	for _, price := range []float64{0, -1, math.SmallestNonzeroFloat64 * -1, MaxProductPrice + 1, 1e30, math.Inf(1), math.Inf(-1), math.NaN()} {
		if ValidProductPrice(price) {
			t.Errorf("accepted invalid or unorderable price %v", price)
		}
	}
	for _, price := range []float64{0.01, 10000, MaxProductPrice} {
		if !ValidProductPrice(price) {
			t.Errorf("rejected valid price %v", price)
		}
	}
}
