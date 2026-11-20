// phone-audit inventories legacy phone values without writing to MongoDB.
package main

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"bakeu/internal/identity"
)

type report struct {
	Users              int `json:"users"`
	Missing            int `json:"missing"`
	Invalid            int `json:"invalid"`
	NeedsNormalization int `json:"needsNormalization"`
	CollisionGroups    int `json:"collisionGroups"`
	UsersInCollision   int `json:"usersInCollision"`
}

func (r *report) record(value bson.RawValue, seen map[string]int) {
	r.Users++
	if value.IsZero() || value.Type == bson.TypeNull {
		r.Missing++
		return
	}
	phone, ok := value.StringValueOK()
	if !ok {
		r.Invalid++
		return
	}
	if strings.TrimSpace(phone) == "" {
		r.Missing++
		return
	}
	normalized, err := identity.NormalizePhone(phone)
	if err != nil {
		r.Invalid++
		return
	}
	if phone != normalized {
		r.NeedsNormalization++
	}
	seen[normalized]++
	switch seen[normalized] {
	case 2:
		r.CollisionGroups++
		r.UsersInCollision += 2
	case 1:
	default:
		r.UsersInCollision++
	}
}

func main() {
	uri, database := os.Getenv("MONGODB_URI"), os.Getenv("MONGODB_DB")
	if uri == "" || database == "" {
		log.Fatal("set MONGODB_URI and MONGODB_DB explicitly")
	}
	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		log.Fatal("invalid MongoDB connection configuration")
	}
	defer client.Disconnect(context.Background())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cursor, err := client.Database(database).Collection("users").Find(ctx, bson.D{},
		options.Find().SetProjection(bson.M{"_id": 0, "phone": 1}))
	if err != nil {
		log.Fatal("cannot read MongoDB users")
	}
	defer cursor.Close(ctx)

	var counts report
	seen := make(map[string]int)
	for cursor.Next(ctx) {
		counts.record(cursor.Current.Lookup("phone"), seen)
	}
	if cursor.Err() != nil {
		log.Fatal("MongoDB scan incomplete")
	}
	if err := json.NewEncoder(os.Stdout).Encode(counts); err != nil {
		log.Fatal("cannot write report")
	}
}
