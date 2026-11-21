package router

import (
	"bakeu/internal/auth"
	"bakeu/internal/config"
	"bakeu/internal/models"
	"bakeu/internal/repository"
	"bytes"
	"context"
	"encoding/json"
	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestVerifiedSelfServicePreservesBuyerIdentityAndModeration(t *testing.T) {
	uri := os.Getenv("BAKEU_TEST_MONGO_URI")
	if !strings.HasPrefix(uri, "mongodb://127.0.0.1:") {
		t.Skip("requires disposable replica set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Disconnect(ctx)
	db := client.Database("bakeu_self_service_" + bson.NewObjectID().Hex())
	defer db.Drop(ctx)
	if err := repository.NewTelegramRepo(db).EnsureIndexes(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Collection("markets").Indexes().CreateOne(ctx, mongo.IndexModel{Keys: bson.D{{Key: "sellerId", Value: 1}}, Options: options.Index().SetUnique(true)}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	users := repository.NewUserRepo(db)
	user := func(name, phone, actor string, role models.Role) *models.User {
		t.Helper()
		u := &models.User{Name: name, Phone: phone, VerifiedPhoneE164: phone, PhoneVerifiedAt: &now, Email: name + "@test.invalid", Role: role}
		if err := users.Create(ctx, u); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Collection("telegram_bindings").InsertOne(ctx, repository.TelegramBinding{TelegramID: actor, UserID: u.UID, VerifiedPhoneE164: phone, ConfirmedAt: now, BotStarted: true}); err != nil {
			t.Fatal(err)
		}
		return u
	}
	buyer := user("Buyer", "+998901111111", "911", models.RoleCustomer)
	vendor := user("Vendor", "+998901111112", "912", models.RoleSeller)
	adminUser := user("Admin", "+998901111113", "913", models.RoleCustomer)
	vendorShop := &models.Market{SellerID: vendor.UID, Status: models.MarketApproved, Fulfillment: models.FulfillmentPickup, Name: "Other bakery"}
	if err := repository.NewMarketRepo(db).Create(ctx, vendorShop); err != nil {
		t.Fatal(err)
	}
	product := &models.Product{SellerID: vendor.UID, Title: "Bread", Price: 10000, Available: true}
	if _, err := repository.NewProductRepo(db).Create(ctx, product); err != nil {
		t.Fatal(err)
	}
	const secret = "self-service-test-jwt-secret"
	const bridge = "self-service-bridge-test-only-with-more-than-32-characters"
	jwt := auth.NewManager(secret)
	token := func(u *models.User) string { v, _ := jwt.Sign(u.UID); return v }
	cfg := config.Config{JWTSecret: secret, TelegramBridgeToken: bridge, SellerSelfService: true, AdminUserIDs: []string{adminUser.UID}}
	gin.SetMode(gin.TestMode)
	r := New(db, cfg)
	call := func(want int, method, path string, body any, bearer, actor, expectedUID, key string) map[string]any {
		t.Helper()
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest(method, path, bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		if actor != "" {
			req.Header.Set("X-Telegram-User-Id", actor)
		}
		if expectedUID != "" {
			req.Header.Set("X-BakeU-Account", expectedUID)
		}
		if key != "" {
			req.Header.Set("Idempotency-Key", key)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		out := map[string]any{}
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		if w.Code != want {
			t.Fatalf("%s %s got %d want %d: %s", method, path, w.Code, want, w.Body.String())
		}
		return out
	}
	buy := gin.H{"items": []gin.H{{"productId": product.ID, "qty": 1}}, "fulfillment": "pickup", "scheduledAt": now.Add(24 * time.Hour).Format(time.RFC3339)}
	call(201, "POST", "/api/orders", buy, token(buyer), "", buyer.UID, "before-shop")
	call(401, "GET", "/api/integrations/telegram/diagnostics", nil, "", "", "", "")
	diagnostics := call(200, "GET", "/api/integrations/telegram/diagnostics", nil, bridge, "", "", "")
	if diagnostics["databaseConnected"] != true || diagnostics["transactionsAvailable"] != true {
		t.Fatal("replica readiness missing")
	}
	call(200, "GET", "/readyz", nil, "", "", "", "")
	favorite := call(200, "PUT", "/api/favorites/"+product.ID, nil, token(buyer), "", buyer.UID, "")
	if len(favorite["productIds"].([]any)) != 1 || favorite["uid"] != buyer.UID {
		t.Fatal("favorite not saved to owner")
	}
	call(409, "DELETE", "/api/favorites/"+product.ID, nil, token(buyer), "", vendor.UID, "")
	call(200, "PUT", "/api/favorites/"+product.ID, nil, token(buyer), "", buyer.UID, "")
	call(409, "DELETE", "/api/favorites?revision="+favorite["revision"].(string), nil, token(buyer), "", buyer.UID, "")
	favorite = call(200, "GET", "/api/favorites", nil, token(buyer), "", buyer.UID, "")
	if len(favorite["productIds"].([]any)) != 1 {
		t.Fatal("favorite disappeared after stale clear")
	}
	otherFavorites := call(200, "GET", "/api/favorites", nil, token(vendor), "", vendor.UID, "")
	if len(otherFavorites["productIds"].([]any)) != 0 {
		t.Fatal("cross-account favorites exposed")
	}
	call(200, "DELETE", "/api/favorites?revision="+favorite["revision"].(string), nil, token(buyer), "", buyer.UID, "")
	open := gin.H{"firstName": "Buyer", "lastName": "Owner", "name": "My bakery", "address": "Tashkent 1", "lat": 0, "lng": 0, "userId": vendor.UID, "shopId": vendorShop.ID}
	call(409, "POST", "/api/market/self-service", open, token(buyer), "", vendor.UID, "")
	var wg sync.WaitGroup
	opened := make([]map[string]any, 2)
	for i := range opened {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			opened[i] = call(200, "POST", "/api/market/self-service", open, token(buyer), "", buyer.UID, "")
		}(i)
	}
	wg.Wait()
	shopID := opened[0]["id"].(string)
	if opened[1]["id"] != shopID || opened[0]["sellerId"] != buyer.UID || opened[0]["status"] != "approved" {
		t.Fatal("duplicate or foreign shop activation")
	}
	if n, _ := db.Collection("markets").CountDocuments(ctx, bson.M{"sellerId": buyer.UID}); n != 1 {
		t.Fatal("duplicate shops")
	}
	if n, _ := db.Collection("market_events").CountDocuments(ctx, bson.M{"marketId": shopID}); n != 1 {
		t.Fatal("duplicate activation events")
	}
	me := call(200, "GET", "/api/me", nil, token(buyer), "", buyer.UID, "")
	if me["uid"] != buyer.UID || me["role"] != "seller" {
		t.Fatal("identity changed during promotion")
	}
	call(201, "POST", "/api/orders", buy, token(buyer), "", buyer.UID, "after-shop")
	list := call(200, "GET", "/api/orders", nil, token(buyer), "", buyer.UID, "")
	if len(list["placed"].([]any)) != 2 {
		t.Fatal("buyer history disappeared after opening shop")
	}
	identity := call(200, "GET", "/api/integrations/telegram/me", nil, bridge, "911", "", "")
	if identity["shopId"] != shopID || identity["user"].(map[string]any)["canBuy"] != true {
		t.Fatal("Telegram lost buyer capability")
	}
	call(200, "GET", "/api/integrations/telegram/orders?audience=buyer", nil, bridge, "911", "", "")
	first := list["placed"].([]any)[0].(map[string]any)
	call(403, "PATCH", "/api/integrations/telegram/orders/"+first["id"].(string)+"/status", gin.H{"expectedStatus": "pending", "status": "accepted"}, bridge, "911", "", "foreign-status")
	ownProduct := call(201, "POST", "/api/products", gin.H{"title": "Own bread", "price": 7000, "available": true}, token(buyer), "", buyer.UID, "")
	call(403, "POST", "/api/cart/items", gin.H{"productId": ownProduct["id"], "qty": 1}, token(buyer), "", buyer.UID, "")
	call(403, "POST", "/api/orders", gin.H{"items": []gin.H{{"productId": ownProduct["id"], "qty": 1}}, "fulfillment": "pickup", "scheduledAt": now.Add(time.Hour).Format(time.RFC3339)}, token(buyer), "", buyer.UID, "own-order")
	moderation := gin.H{"expectedStatus": "approved", "status": "suspended", "reason": "Owner review"}
	call(403, "PATCH", "/api/admin/markets/"+shopID+"/status", moderation, token(buyer), "", buyer.UID, "")
	call(200, "PATCH", "/api/admin/markets/"+shopID+"/status", moderation, token(adminUser), "", adminUser.UID, "")
	call(409, "PATCH", "/api/admin/markets/"+shopID+"/status", moderation, token(adminUser), "", adminUser.UID, "")
	call(404, "GET", "/api/markets/"+shopID, nil, "", "", "", "")
	call(403, "POST", "/api/market/self-service", open, token(buyer), "", buyer.UID, "")
	call(200, "PATCH", "/api/admin/markets/"+shopID+"/status", gin.H{"expectedStatus": "suspended", "status": "approved", "reason": "Review complete"}, token(adminUser), "", adminUser.UID, "")
	call(200, "GET", "/api/markets/"+shopID, nil, "", "", "", "")
	call(200, "GET", "/api/admin/markets", nil, token(adminUser), "", adminUser.UID, "")
	if _, err := db.Collection("users").UpdateOne(ctx, bson.M{"_id": buyer.UID}, bson.M{"$set": bson.M{"frozen": true}}); err != nil {
		t.Fatal(err)
	}
	call(403, "POST", "/api/market/self-service", open, token(buyer), "", buyer.UID, "")
	disabled := cfg
	disabled.SellerSelfService = false
	r = New(db, disabled)
	call(404, "POST", "/api/market/self-service", open, token(vendor), "", vendor.UID, "")
}
