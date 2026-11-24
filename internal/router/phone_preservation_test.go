package router

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"bakeu/internal/auth"
	"bakeu/internal/config"
	"bakeu/internal/models"
	"bakeu/internal/mongoclient"
	"bakeu/internal/repository"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"golang.org/x/crypto/bcrypt"
)

func TestPhoneIndexStagingPlanPreservesLegacyAccountsOrdersAndSessions(t *testing.T) {
	uri := os.Getenv("BAKEU_TEST_MONGO_URI")
	if !strings.HasPrefix(uri, "mongodb://127.0.0.1:") {
		t.Skip("requires disposable loopback replica set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Disconnect(ctx)
	db := client.Database("bakeu_phone_preserve_staging_test_" + bson.NewObjectID().Hex())
	defer db.Drop(ctx)
	if _, err := db.Collection("users").Indexes().CreateOne(ctx, mongo.IndexModel{Keys: bson.D{{Key: "email", Value: 1}}, Options: options.Index().SetUnique(true)}); err != nil {
		t.Fatal(err)
	}
	uid := bson.NewObjectID().Hex()
	hash, _ := bcrypt.GenerateFromPassword([]byte("legacy-password-123"), bcrypt.MinCost)
	legacy := bson.M{"_id": uid, "email": "legacy@test.invalid", "passwordHash": string(hash), "phone": "+998901234567", "name": "Legacy Customer", "role": "customer", "createdAt": time.Now().UTC().Truncate(time.Millisecond), "unknownLegacyField": bson.M{"keep": true}}
	if _, err := db.Collection("users").InsertOne(ctx, legacy); err != nil {
		t.Fatal(err)
	}
	order := models.Order{ID: bson.NewObjectID().Hex(), CustomerID: uid, SellerID: bson.NewObjectID().Hex(), Status: models.OrderPending, Items: []models.OrderItem{{Title: "Existing cake", Price: 100, Qty: 1}}, Total: 100, CreatedAt: time.Now().UTC().Truncate(time.Millisecond)}
	if _, err := db.Collection("orders").InsertOne(ctx, order); err != nil {
		t.Fatal(err)
	}
	sessions := repository.NewSessionRepo(db)
	oldSession, refresh, err := sessions.Create(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	jwtm := auth.NewManager("test-preserved-jwt-secret")
	oldAccess, err := jwtm.SignSession(uid, oldSession.ID)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := func(collection, id string) bson.M {
		t.Helper()
		var out bson.M
		if err := db.Collection(collection).FindOne(ctx, bson.M{"_id": id}).Decode(&out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	beforeUser, beforeOrder, beforeSession := snapshot("users", uid), snapshot("orders", order.ID), snapshot("auth_sessions", oldSession.ID)
	cfg := config.Config{MongoURI: uri, MongoDB: db.Name(), JWTSecret: "test-preserved-jwt-secret", PhoneAccounts: true, RefreshSessions: true, TelegramBotUsername: "BakeUuzBot", TelegramBridgeToken: strings.Repeat("bridge", 8), BrowserOrigins: []string{"https://staging.test"}}
	// Startup must diagnose, never silently drop the legacy constraint.
	if c, err := mongoclient.New(ctx, cfg); !errors.Is(err, repository.ErrPhoneEmailIndexMigration) {
		if c != nil {
			c.Close()
		}
		t.Fatalf("missing explicit migration gate: %v", err)
	}
	plan, err := repository.InspectEmailIndexes(ctx, db)
	if err != nil || !plan.OptionalEmailUnique || len(plan.BlockingLegacyIndexes) != 1 || plan.BlockingLegacyIndexes[0] != "email_1" {
		t.Fatalf("bad read-only plan: %+v %v", plan, err)
	}
	if err := repository.ApplyStagingEmailIndexPlan(ctx, db); err != nil {
		t.Fatal(err)
	}
	// Idempotent and preserves unique real legacy emails.
	if err := repository.ApplyStagingEmailIndexPlan(ctx, db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Collection("users").InsertOne(ctx, bson.M{"_id": bson.NewObjectID().Hex(), "email": "legacy@test.invalid"}); !mongo.IsDuplicateKeyError(err) {
		t.Fatal("lost legacy email uniqueness")
	}
	c, err := mongoclient.New(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	if !reflect.DeepEqual(beforeUser, snapshot("users", uid)) || !reflect.DeepEqual(beforeOrder, snapshot("orders", order.ID)) || !reflect.DeepEqual(beforeSession, snapshot("auth_sessions", oldSession.ID)) {
		t.Fatal("index plan/startup changed existing documents")
	}
	router := New(db, cfg)
	call := func(path string, body any, bearer string) map[string]any {
		t.Helper()
		raw, _ := json.Marshal(body)
		method := "GET"
		if body != nil {
			method = "POST"
		}
		req := httptest.NewRequest(method, path, bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
		var data map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &data)
		return data
	}
	if call("/api/me", nil, oldAccess)["uid"] != uid {
		t.Fatal("old access session lost")
	}
	if call("/api/auth/login", map[string]string{"email": "legacy@test.invalid", "password": "legacy-password-123"}, "")["user"].(map[string]any)["uid"] != uid {
		t.Fatal("legacy credential compatibility lost")
	}
	if session, _, err := sessions.Rotate(ctx, refresh); err != nil || session.UserID != uid {
		t.Fatal("existing refresh session lost")
	}
	// No mutable legacy phone is automatically promoted to verified ownership.
	if user, err := repository.NewUserRepo(db).Get(ctx, uid); err != nil || user.PhoneVerified() {
		t.Fatal("unverified legacy phone was claimed automatically")
	}
	if !reflect.DeepEqual(beforeOrder, snapshot("orders", order.ID)) {
		t.Fatal("historical order changed")
	}
}
