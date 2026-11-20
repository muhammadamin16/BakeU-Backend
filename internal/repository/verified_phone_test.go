package repository

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"bakeu/internal/models"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

func TestVerifiedPhoneOwnershipRealMongo(t *testing.T) {
	uri := os.Getenv("BAKEU_TEST_MONGO_URI")
	if !strings.HasPrefix(uri, "mongodb://127.0.0.1:") { t.Skip("requires disposable loopback replica set") }
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil { t.Fatal(err) }
	defer client.Disconnect(ctx)
	db := client.Database("bakeu_phone_test_" + bson.NewObjectID().Hex())
	defer db.Drop(ctx)
	repo, users := NewTelegramRepo(db), NewUserRepo(db)
	if err := repo.EnsureIndexes(ctx); err != nil { t.Fatal(err) }
	phone := "+998901234567"
	u := &models.User{Email: "first@test.invalid", Role: models.RoleCustomer, Phone: phone}
	v := &models.User{Email: "second@test.invalid", Role: models.RoleCustomer, Phone: phone}
	for _, user := range []*models.User{u, v} { if err := users.Create(ctx, user); err != nil { t.Fatal(err) } }
	// A mutable phone duplicated on legacy profiles never links accounts.
	_, err = db.Collection("telegram_bindings").InsertOne(ctx, TelegramBinding{TelegramID: "303", UserID: u.UID, ConfirmedAt: time.Now(), BotStarted: true})
	if err != nil { t.Fatal(err) }
	if who, err := repo.Identity(ctx, "303"); err != nil || who != nil { t.Fatalf("legacy binding trusted: %v %v", who, err) }
	// Remove only this synthetic legacy fixture to exercise independent concurrent proofs.
	if _, err := db.Collection("telegram_bindings").DeleteOne(ctx, bson.M{"_id": "303"}); err != nil { t.Fatal(err) }
	actors := []string{"101", "202"}
	accounts := []*models.User{u, v}
	tokens := make([]string, 2)
	for i := range tokens {
		tokens[i], _, err = repo.Challenge(ctx, actors[i], phone, models.RoleCustomer)
		if err != nil { t.Fatal(err) }
	}
	var wg sync.WaitGroup
	results := make([]error, 2)
	for i := range tokens { wg.Add(1); go func(i int) { defer wg.Done(); results[i] = repo.Confirm(ctx, tokens[i], accounts[i].UID) }(i) }
	wg.Wait()
	winner, loser := 0, 1
	if results[0] != nil { winner, loser = 1, 0 }
	if results[winner] != nil || !errors.Is(results[loser], ErrConflict) { t.Fatalf("phone was not claimed exactly once: %v", results) }
	proven, err := users.Get(ctx, accounts[winner].UID)
	if err != nil || !proven.PhoneVerified() || proven.VerifiedPhoneE164 != phone { t.Fatalf("proof missing: %v %v", proven, err) }
	unproven, err := users.Get(ctx, accounts[loser].UID)
	if err != nil || unproven.PhoneVerified() { t.Fatal("failed claim partially committed") }
	var failed LinkChallenge
	if err := db.Collection("telegram_challenges").FindOne(ctx, bson.M{"_id": digest(tokens[loser])}).Decode(&failed); err != nil || failed.Used { t.Fatal("failed proof consumed challenge") }
	// Editing a contact preserves the unique identity and existing Telegram access.
	updated, err := users.UpdateProfile(ctx, proven.UID, "New name", "+998909999999", "New address")
	if err != nil || updated.VerifiedPhoneE164 != phone || !updated.PhoneVerifiedAt.Equal(*proven.PhoneVerifiedAt) { t.Fatal("profile rewrote identity") }
	if who, err := repo.Identity(ctx, actors[winner]); err != nil || who == nil || who.User.ID != proven.UID { t.Fatalf("contact edit broke binding: %v %v", who, err) }
	// Fresh proofs must still match the proven identity, never the newly edited phone.
	wrong, _, err := repo.Challenge(ctx, actors[winner], updated.Phone, models.RoleCustomer)
	if err != nil { t.Fatal(err) }
	if err := repo.Confirm(ctx, wrong, proven.UID); !errors.Is(err, ErrForbidden) { t.Fatalf("editable phone replaced identity: %v", err) }
	again, _, err := repo.Challenge(ctx, actors[winner], phone, models.RoleCustomer)
	if err != nil { t.Fatal(err) }
	if err := repo.Confirm(ctx, again, proven.UID); err != nil { t.Fatal(err) }
}
