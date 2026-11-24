package repository

import (
	"context"
	"errors"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

const OptionalEmailIndex = "unique_nonempty_email"

var ErrPhoneEmailIndexMigration = errors.New("PHONE_EMAIL_INDEX_MIGRATION_REQUIRED: stage the optional-email index change before enabling phone accounts; existing users are retained")

type EmailIndexPlan struct {
	OptionalEmailUnique   bool     `json:"optionalEmailUnique"`
	BlockingLegacyIndexes []string `json:"blockingLegacyIndexes"`
}

// InspectEmailIndexes is read-only; missing email must not be treated as one
// globally unique null value. No startup code drops existing indexes.
func InspectEmailIndexes(ctx context.Context, db *mongo.Database) (EmailIndexPlan, error) {
	plan := EmailIndexPlan{BlockingLegacyIndexes: []string{}}
	cursor, err := db.Collection("users").Indexes().List(ctx)
	if err != nil {
		return plan, err
	}
	defer cursor.Close(ctx)
	for cursor.Next(ctx) {
		var index struct {
			Name    string `bson:"name"`
			Key     bson.D `bson:"key"`
			Unique  bool   `bson:"unique"`
			Sparse  bool   `bson:"sparse"`
			Partial bson.M `bson:"partialFilterExpression"`
		}
		if err := cursor.Decode(&index); err != nil {
			return plan, err
		}
		if !index.Unique || len(index.Key) != 1 || index.Key[0].Key != "email" {
			continue
		}
		if index.Name == OptionalEmailIndex && index.Partial != nil {
			plan.OptionalEmailUnique = true
		}
		if !index.Sparse && len(index.Partial) == 0 {
			plan.BlockingLegacyIndexes = append(plan.BlockingLegacyIndexes, index.Name)
		}
	}
	return plan, cursor.Err()
}

// Additive: protects all actual legacy email addresses. Omitting email on new
// phone-only users does not require synthetic addresses or password hashes.
func EnsureOptionalEmailIndex(ctx context.Context, db *mongo.Database) error {
	_, err := db.Collection("users").Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{{Key: "email", Value: 1}},
		Options: options.Index().SetName(OptionalEmailIndex).SetUnique(true).
			SetPartialFilterExpression(bson.M{"email": bson.M{"$type": "string", "$gt": ""}}),
	})
	return err
}

func CheckPhoneEmailIndexes(ctx context.Context, db *mongo.Database) error {
	plan, err := InspectEmailIndexes(ctx, db)
	if err != nil {
		return err
	}
	if !plan.OptionalEmailUnique || len(plan.BlockingLegacyIndexes) > 0 {
		return ErrPhoneEmailIndexMigration
	}
	return nil
}

// ApplyStagingEmailIndexPlan must be invoked explicitly by the guarded staging
// CLI, never startup. Build the replacement constraint BEFORE removing only the
// known full email indexes. It never edits user/order/session documents.
func ApplyStagingEmailIndexPlan(ctx context.Context, db *mongo.Database) error {
	if err := EnsureOptionalEmailIndex(ctx, db); err != nil {
		return err
	}
	plan, err := InspectEmailIndexes(ctx, db)
	if err != nil {
		return err
	}
	for _, name := range plan.BlockingLegacyIndexes {
		if err := db.Collection("users").Indexes().DropOne(ctx, name); err != nil {
			return err
		}
	}
	return CheckPhoneEmailIndexes(ctx, db)
}
