// Package migration inventories untrusted legacy data without inferring ownership.
package migration

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sort"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

type MongoUser struct {
	ID            string `json:"id"`
	IDType        string `json:"idType"`
	Email         string `json:"email"`
	Phone         string `json:"phone"`
	VerifiedPhone string `json:"verifiedPhoneE164"`
	PhoneVerified bool   `json:"phoneVerified"`
	Role          string `json:"role"`
}
type Reference struct {
	Collection string `json:"collection"`
	ID         string `json:"id"`
	Field      string `json:"field"`
	UserID     string `json:"userId"`
}
type Mapping struct {
	Namespace string `json:"namespace" bson:"namespace"`
	SourceID  string `json:"sourceId" bson:"sourceId"`
	TargetUID string `json:"targetUID" bson:"targetUID"`
}
type MongoInventory struct {
	SchemaVersion int              `json:"schemaVersion"`
	CapturedAt    time.Time        `json:"capturedAt"`
	Counts        map[string]int64 `json:"counts"`
	Users         []MongoUser      `json:"users"`
	References    []Reference      `json:"references"`
	Mappings      []Mapping        `json:"mappings"`
}

func id(value any) (string, string) {
	switch v := value.(type) {
	case string:
		return v, "string"
	case bson.ObjectID:
		return v.Hex(), "objectId"
	default:
		return "", "unsupported"
	}
}
func text(value any) string { v, _ := value.(string); return v }

func InventoryMongo(ctx context.Context, db *mongo.Database) (MongoInventory, error) {
	out := MongoInventory{SchemaVersion: 1, CapturedAt: time.Now().UTC(), Counts: map[string]int64{}, Users: []MongoUser{}, References: []Reference{}, Mappings: []Mapping{}}
	cur, err := db.Collection("users").Find(ctx, bson.M{}, options.Find().SetSort(bson.D{{Key: "_id", Value: 1}}).SetProjection(bson.M{"email": 1, "phone": 1, "verifiedPhoneE164": 1, "phoneVerifiedAt": 1, "role": 1}))
	if err != nil {
		return out, err
	}
	defer cur.Close(ctx)
	for cur.Next(ctx) {
		var row bson.M
		if err := cur.Decode(&row); err != nil {
			return out, err
		}
		uid, kind := id(row["_id"])
		at, proven := row["phoneVerifiedAt"].(bson.DateTime)
		out.Users = append(out.Users, MongoUser{ID: uid, IDType: kind, Email: text(row["email"]), Phone: text(row["phone"]), VerifiedPhone: text(row["verifiedPhoneE164"]), PhoneVerified: proven && !at.Time().IsZero() && text(row["verifiedPhoneE164"]) != "", Role: text(row["role"])})
	}
	if err := cur.Err(); err != nil {
		return out, err
	}
	out.Counts["users"] = int64(len(out.Users))
	for collection, fields := range map[string][]string{"markets": {"sellerId"}, "products": {"sellerId"}, "orders": {"customerId", "sellerId"}, "reviews": {"customerId"}, "carts": {"_id"}, "favorites": {"_id"}, "auth_sessions": {"userId"}, "order_notifications": {"userId"}, "telegram_bindings": {"userId"}} {
		projection := bson.M{}
		for _, field := range fields {
			projection[field] = 1
		}
		cursor, e := db.Collection(collection).Find(ctx, bson.M{}, options.Find().SetProjection(projection))
		if e != nil {
			return out, e
		}
		var count int64
		for cursor.Next(ctx) {
			var row bson.M
			if e = cursor.Decode(&row); e != nil {
				cursor.Close(ctx)
				return out, e
			}
			count++
			recordID, _ := id(row["_id"])
			for _, field := range fields {
				uid, _ := id(row[field])
				out.References = append(out.References, Reference{Collection: collection, ID: recordID, Field: field, UserID: uid})
			}
		}
		e = cursor.Err()
		cursor.Close(ctx)
		if e != nil {
			return out, e
		}
		out.Counts[collection] = count
	}
	cursor, err := db.Collection("legacy_identity_mappings").Find(ctx, bson.M{}, options.Find().SetProjection(bson.M{"_id": 0, "namespace": 1, "sourceId": 1, "targetUID": 1}))
	if err != nil {
		return out, err
	}
	defer cursor.Close(ctx)
	if err = cursor.All(ctx, &out.Mappings); err != nil {
		return out, err
	}
	sort.Slice(out.References, func(i, j int) bool {
		a, b := out.References[i], out.References[j]
		if a.Collection != b.Collection {
			return a.Collection < b.Collection
		}
		if a.ID != b.ID {
			return a.ID < b.ID
		}
		return a.Field < b.Field
	})
	return out, nil
}

func WritePrivateJSON(path string, value any) error {
	if path == "" {
		return errors.New("private output path required")
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	if err = json.NewEncoder(file).Encode(value); err != nil {
		file.Close()
		os.Remove(path)
		return err
	}
	return file.Close()
}
