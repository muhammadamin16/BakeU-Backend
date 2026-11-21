package migration

import (
	"context"
	"errors"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"strings"
	"time"
)

type StagedClaims struct {
	Inserted       int `json:"pendingClaimsInserted"`
	Existing       int `json:"existingClaimsPreserved"`
	GoUsersCreated int `json:"goUsersCreated"`
}

// StagePendingClaims is intentionally limited to disposable staging namespaces.
// It seeds a quarantine/claim ledger, never users, roles, shops or auth bindings.
// Actual claims require independent owner proof and an approved migration.
func StagePendingClaims(ctx context.Context, db *mongo.Database, plan Plan) (StagedClaims, error) {
	var out StagedClaims
	if !strings.HasPrefix(db.Name(), "bakeu_migration_staging_") || plan.SchemaVersion != 1 || !plan.DryRun || plan.Hash != plan.Digest() {
		return out, errors.New("staging namespace and intact reviewed plan required")
	}
	for _, e := range plan.Entries {
		if e.Action == "hold_for_claim" && (e.SourceID == "" || !e.RequireLegacyProof || !e.RequirePhoneProof || len(e.TargetCandidates) > 0 || e.Key != Hash([]string{plan.Namespace, e.SourceID}) || (e.ProposedRole != "customer" && e.ProposedRole != "seller")) {
			return out, errors.New("plan cannot authorize account ownership")
		}
	}
	for _, e := range plan.Entries {
		if e.Action != "hold_for_claim" {
			continue
		}
		result, err := db.Collection("legacy_migration_claims").UpdateOne(ctx, bson.M{"_id": e.Key}, bson.M{"$setOnInsert": bson.M{"namespace": plan.Namespace, "sourceId": e.SourceID, "state": "pending", "requiresOwnershipProof": true, "proposedRole": e.ProposedRole, "planHash": plan.Hash, "sourceSnapshotSha256": plan.SourceSHA256, "createdAt": time.Now().UTC()}}, options.UpdateOne().SetUpsert(true))
		if err != nil {
			return out, err
		}
		if result.UpsertedCount == 1 {
			out.Inserted++
		} else {
			out.Existing++
		}
	}
	return out, nil
}
