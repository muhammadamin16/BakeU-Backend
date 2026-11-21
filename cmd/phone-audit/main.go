// phone-audit inventories legacy phone values without writing to MongoDB.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
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

type comparison struct {
	SQLiteMissingOrInvalid     int    `json:"sqliteMissingOrInvalid"`
	SQLiteConflictReview       int    `json:"sqliteConflictReview"`
	SQLiteNeedsOwnerProof      int    `json:"sqliteNeedsOwnerProof"`
	SQLiteMatchesVerifiedMongo int    `json:"sqliteMatchesVerifiedMongo"`
	Mongo                      report `json:"mongo"`
	SQLite                     report `json:"sqlite"`
	SharedPhones               int    `json:"sharedPhones"`
	MongoUsersWithSharedPhone  int    `json:"mongoUsersWithSharedPhone"`
	SQLiteUsersWithSharedPhone int    `json:"sqliteUsersWithSharedPhone"`
}

func comparePhones(input io.Reader, counts report, mongoPhones, verifiedMongoPhones map[string]int) (comparison, error) {
	result := comparison{Mongo: counts}
	sqlitePhones := make(map[string]int)
	decoder := json.NewDecoder(input)
	for {
		var phone *string
		err := decoder.Decode(&phone)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return comparison{}, err
		}
		valueType, valueBytes, err := bson.MarshalValue(phone)
		if err != nil {
			return comparison{}, err
		}
		result.SQLite.record(bson.RawValue{Type: valueType, Value: valueBytes}, sqlitePhones)
	}
	result.SQLiteMissingOrInvalid = result.SQLite.Missing + result.SQLite.Invalid
	for phone, users := range sqlitePhones {
		if verifiedMongoPhones[phone] > 0 {
			result.SQLiteMatchesVerifiedMongo += users
		}
		if users > 1 || mongoPhones[phone] > 0 || verifiedMongoPhones[phone] > 0 {
			result.SQLiteConflictReview += users
		} else {
			result.SQLiteNeedsOwnerProof += users
		}
		if mongoUsers := mongoPhones[phone]; mongoUsers > 0 {
			result.SharedPhones++
			result.MongoUsersWithSharedPhone += mongoUsers
			result.SQLiteUsersWithSharedPhone += users
		}
	}
	return result, nil
}

func main() {
	compareSQLite := flag.Bool("compare-sqlite", false, "read SQLite phone values as JSON strings or null from stdin")
	flag.Parse()
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
		options.Find().SetProjection(bson.M{"_id": 0, "phone": 1, "verifiedPhoneE164": 1}))
	if err != nil {
		log.Fatal("cannot read MongoDB users")
	}
	defer cursor.Close(ctx)

	var counts report
	seen := make(map[string]int)
	verified := make(map[string]int)
	var verifiedCounts report
	for cursor.Next(ctx) {
		counts.record(cursor.Current.Lookup("phone"), seen)
		verifiedCounts.record(cursor.Current.Lookup("verifiedPhoneE164"), verified)
	}
	if cursor.Err() != nil {
		log.Fatal("MongoDB scan incomplete")
	}
	var result any = counts
	if *compareSQLite {
		compared, err := comparePhones(os.Stdin, counts, seen, verified)
		if err != nil {
			log.Fatal("invalid SQLite phone input")
		}
		result = compared
	}
	if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
		log.Fatal("cannot write report")
	}
}
