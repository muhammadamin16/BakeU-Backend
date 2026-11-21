package migration

import (
	"context"
	"encoding/json"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"os"
	"strings"
	"testing"
	"time"
)

func sourceFixture() SQLiteInventory {
	return SQLiteInventory{SchemaVersion: 1, Namespace: "bakeu-bot", SQLiteSHA256: strings.Repeat("a", 64), IntegrityCheck: "ok", Users: []LegacyUser{{SourceID: "unique", Email: "unique@test.invalid", Phone: "+998901234561", Role: "client", ProfileValid: true, PasswordAlgorithm: "node-scrypt"}, {SourceID: "candidate", Phone: "+998901234562", Email: "same@test.invalid", Role: "seller", ProfileValid: true}, {SourceID: "mapped", Phone: "+998901234563", Role: "client", ProfileValid: true}, {SourceID: "invalid", Phone: "bad", Role: "admin", ProfileValid: true}}, Applications: []LegacyApplication{{SourceID: "orphan", UserID: "missing"}}}
}
func TestPlanDoesNotInferOwnershipOrCopyPasswords(t *testing.T) {
	source := sourceFixture()
	target := MongoInventory{SchemaVersion: 1, Users: []MongoUser{{ID: "existing", IDType: "string", Email: "same@test.invalid", VerifiedPhone: "+998901234562", PhoneVerified: true}}, Mappings: []Mapping{{Namespace: "bakeu-bot", SourceID: "mapped", TargetUID: "existing"}}, References: []Reference{{Collection: "orders", UserID: "deleted"}}}
	plan, err := BuildPlan(source, target)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Summary.Create != 0 || plan.Summary.Merge != 0 || plan.Summary.Skip != 1 || plan.Summary.Conflict != 1 || plan.Summary.Manual != 1 || plan.Summary.RequireVerification != 1 || plan.Summary.SQLiteOrphanApplications != 1 || plan.Summary.MongoOrphanReferences != 1 {
		t.Fatalf("unsafe or incomplete triage: %+v", plan.Summary)
	}
	for _, entry := range plan.Entries {
		if entry.SourceID == "candidate" && (entry.Action != "conflict_review" || !entry.RequireLegacyProof || !entry.RequirePhoneProof) {
			t.Fatal("a matching verified phone implied legacy ownership")
		}
	}
	raw, _ := json.Marshal(plan.Summary)
	if strings.Contains(string(raw), "998") || strings.Contains(string(raw), "test.invalid") {
		t.Fatal("aggregate report exposes contacts")
	}
	again, err := BuildPlan(source, target)
	if err != nil || again.Hash != plan.Hash {
		t.Fatal("plan is not deterministic")
	}
}
func TestPendingClaimStagingIsIdempotentAndCannotImportAccounts(t *testing.T) {
	uri := os.Getenv("BAKEU_TEST_MONGO_URI")
	if !strings.HasPrefix(uri, "mongodb://127.0.0.1:") {
		t.Skip("requires disposable loopback Mongo")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Disconnect(ctx)
	db := client.Database("bakeu_migration_staging_" + bson.NewObjectID().Hex())
	defer db.Drop(ctx)
	plan, err := BuildPlan(sourceFixture(), MongoInventory{SchemaVersion: 1})
	if err != nil {
		t.Fatal(err)
	}
	one, err := StagePendingClaims(ctx, db, plan)
	if err != nil {
		t.Fatal(err)
	}
	two, err := StagePendingClaims(ctx, db, plan)
	if err != nil {
		t.Fatal(err)
	}
	if one.Inserted != 1 || two.Inserted != 0 || two.Existing != 1 || one.GoUsersCreated != 0 {
		t.Fatal("claim ledger was duplicated or assigned an account")
	}
	for _, collection := range []string{"users", "markets", "telegram_bindings", "auth_sessions"} {
		if n, _ := db.Collection(collection).CountDocuments(ctx, bson.M{}); n != 0 {
			t.Fatal("staging assigned application identity")
		}
	}
	if _, err := StagePendingClaims(ctx, client.Database("bakeu"), plan); err == nil {
		t.Fatal("production namespace accepted")
	}
	plan.Entries[0].ProposedRole = "admin"
	if _, err := StagePendingClaims(ctx, db, plan); err == nil {
		t.Fatal("tampered plan accepted")
	}
}
