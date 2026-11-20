package handlers

import "testing"

func TestMarketCoordinates(t *testing.T) {
	lat, lng, err := marketCoordinates(" 0 ", "69.279")
	if err != nil || lat == nil || lng == nil || *lat != 0 || *lng != 69.279 {
		t.Fatalf("valid coordinates: %v, %v, %v", lat, lng, err)
	}
	lat, lng, err = marketCoordinates("", "")
	if err != nil || lat != nil || lng != nil {
		t.Fatalf("omitted coordinates: %v, %v, %v", lat, lng, err)
	}
	for _, pair := range [][2]string{{"", "69"}, {"91", "69"}, {"41", "-181"}, {"NaN", "69"}, {"41", "Inf"}} {
		if _, _, err := marketCoordinates(pair[0], pair[1]); err == nil {
			t.Errorf("accepted invalid coordinates %q", pair)
		}
	}
}
