package main

import (
	"strings"
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

func TestComparePhones(t *testing.T) {
	input := strings.NewReader("\"901234567\"\n\"+998901234567\"\nnull\n\"invalid\"\n\"+12025550123\"\n")
	got, err := comparePhones(input, report{Users: 3}, map[string]int{
		"+998901234567": 2, "+998911234567": 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := comparison{
		Mongo:        report{Users: 3},
		SQLite:       report{Users: 5, Missing: 1, Invalid: 1, NeedsNormalization: 1, CollisionGroups: 1, UsersInCollision: 2},
		SharedPhones: 1, MongoUsersWithSharedPhone: 2, SQLiteUsersWithSharedPhone: 2,
	}
	if got != want {
		t.Fatalf("comparison = %+v; want %+v", got, want)
	}
	if _, err := comparePhones(strings.NewReader("{"), report{}, nil); err == nil {
		t.Fatal("expected malformed input to fail")
	}
}
