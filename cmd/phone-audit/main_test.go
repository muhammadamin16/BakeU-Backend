package main

import (
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"
)

func TestPhoneReport(t *testing.T) {
	var counts report
	seen := make(map[string]int)
	for _, doc := range []bson.M{
		{}, {"phone": nil}, {"phone": 998901234567}, {"phone": "invalid"},
		{"phone": "+998901234567"}, {"phone": "998 90 123 45 67"},
		{"phone": "901234567"}, {"phone": "+12025550123"},
	} {
		raw, err := bson.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		counts.record(bson.Raw(raw).Lookup("phone"), seen)
	}
	want := report{Users: 8, Missing: 2, Invalid: 2, NeedsNormalization: 2, CollisionGroups: 1, UsersInCollision: 3}
	if counts != want {
		t.Fatalf("report = %+v; want %+v", counts, want)
	}
}
