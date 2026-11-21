package router

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"bakeu/internal/auth"
	"bakeu/internal/config"
	"bakeu/internal/models"
	"bakeu/internal/repository"
	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

func TestVariantCheckoutRealMongo(t *testing.T) {
	uri := os.Getenv("BAKEU_TEST_MONGO_URI")
	if !strings.HasPrefix(uri, "mongodb://127.0.0.1:") {
		t.Skip("requires disposable loopback replica set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Disconnect(ctx)
	db := client.Database("bakeu_variant_test_" + bson.NewObjectID().Hex())
	defer db.Drop(ctx)
	users := repository.NewUserRepo(db)
	seller := &models.User{Email: "seller@test.invalid", Phone: "+998901234568", Role: models.RoleSeller}
	buyer := &models.User{Email: "buyer@test.invalid", Phone: "+998901234567", Role: models.RoleCustomer}
	for _, u := range []*models.User{seller, buyer} {
		if err := users.Create(ctx, u); err != nil {
			t.Fatal(err)
		}
	}
	shop := &models.Market{SellerID: seller.UID, Status: models.MarketApproved, Name: "Variant bakery", Fulfillment: models.FulfillmentPickup}
	if err := repository.NewMarketRepo(db).Create(ctx, shop); err != nil {
		t.Fatal(err)
	}
	const secret = "variant-checkout-test-jwt-secret"
	sellerToken, _ := auth.NewManager(secret).Sign(seller.UID)
	buyerToken, _ := auth.NewManager(secret).Sign(buyer.UID)
	gin.SetMode(gin.TestMode)
	r := New(db, config.Config{JWTSecret: secret})
	call := func(want int, method, path string, body any, token, key string) map[string]any {
		t.Helper()
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest(method, path, bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
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
	base := gin.H{"title": "Cake", "price": 10000, "available": true, "variants": []gin.H{{"name": "Small", "price": 25000}, {"name": "Large", "price": 50000}}}
	created := call(201, "POST", "/api/products", base, sellerToken, "")
	id := created["id"].(string)
	variants := created["variants"].([]any)
	small := variants[0].(map[string]any)["id"].(string)
	large := variants[1].(map[string]any)["id"].(string)
	if len(small) != 24 || small == large {
		t.Fatal("missing unique server variant IDs")
	}
	future := time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339)
	checkout := gin.H{"items": []gin.H{{"productId": id, "variantId": small, "qty": 2, "price": 1}, {"productId": id, "variantId": large, "qty": 1}}, "fulfillment": "pickup", "scheduledAt": future}
	call(400, "POST", "/api/cart/items", gin.H{"productId": "missing", "qty": 1}, buyerToken, "")
	call(400, "POST", "/api/cart/items", gin.H{"productId": id, "variantId": "missing", "qty": 1}, buyerToken, "")
	initial := call(200, "GET", "/api/cart", nil, buyerToken, "")
	rev := initial["revision"].(string)
	// Both devices start with the same revision. Exactly one write can win.
	results := make(chan int, 2)
	for range 2 {
		go func() {
			raw, _ := json.Marshal(gin.H{"productId": id, "variantId": small, "qty": 2, "cartRevision": rev})
			req := httptest.NewRequest("POST", "/api/cart/items", bytes.NewReader(raw))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+buyerToken)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			results <- w.Code
		}()
	}
	a, b := <-results, <-results
	if !((a == 200 && b == 409) || (a == 409 && b == 200)) {
		t.Fatalf("concurrent writes returned %d and %d", a, b)
	}
	first := call(200, "GET", "/api/cart", nil, buyerToken, "")
	second := call(200, "POST", "/api/cart/items", gin.H{
		"productId": id, "variantId": large, "qty": 1, "cartRevision": first["revision"],
	}, buyerToken, "")
	checkout["cartRevision"] = first["revision"]
	call(409, "POST", "/api/orders", checkout, buyerToken, "variants-stale")
	if count, err := db.Collection("orders").CountDocuments(ctx, bson.M{}); err != nil || count != 0 {
		t.Fatalf("stale checkout wrote orders: %d, %v", count, err)
	}
	checkout["cartRevision"] = second["revision"]
	orders := call(201, "POST", "/api/orders", checkout, buyerToken, "variants-one")["orders"].([]any)
	if len(orders) != 1 {
		t.Fatal("same seller variants split into separate orders")
	}
	var order models.Order
	raw, _ := json.Marshal(orders[0])
	if e := json.Unmarshal(raw, &order); e != nil {
		t.Fatal(e)
	}
	if order.Total != 100000 || len(order.Items) != 2 {
		t.Fatalf("variant quantities/prices lost: %+v", order)
	}
	for _, item := range order.Items {
		if item.VariantID == small && (item.VariantName != "Small" || item.Price != 25000 || item.Qty != 2) {
			t.Fatal("small snapshot missing")
		}
		if item.VariantID == large && (item.VariantName != "Large" || item.Price != 50000 || item.Qty != 1) {
			t.Fatal("large snapshot missing")
		}
	}
	// Change order and names/prices of variants; IDs and historical receipts persist.
	base["variants"] = []gin.H{{"id": large, "name": "Family", "price": 75000}, {"id": small, "name": "Single", "price": 30000}}
	updated := call(200, "PUT", "/api/products/"+id, base, sellerToken, "")
	if updated["variants"].([]any)[0].(map[string]any)["id"] != large {
		t.Fatal("rename/reorder changed variant identity")
	}
	replayed := call(200, "POST", "/api/orders", checkout, buyerToken, "variants-one")["orders"].([]any)
	if replayed[0].(map[string]any)["total"] != float64(100000) {
		t.Fatal("replay used today's variant prices")
	}
	stored, e := repository.NewOrderRepo(db).Get(ctx, order.ID)
	if e != nil || stored.Total != 100000 {
		t.Fatal("variant snapshot changed in Mongo")
	}
	// A missing/foreign variant is never silently billed at the base price.
	bad := gin.H{"items": []gin.H{{"productId": id, "variantId": bson.NewObjectID().Hex(), "qty": 1}}, "fulfillment": "pickup", "scheduledAt": future}
	call(400, "POST", "/api/orders", bad, buyerToken, "variant-unknown")
	base["variants"] = []gin.H{{"id": bson.NewObjectID().Hex(), "name": "Foreign", "price": 20000}}
	call(400, "PUT", "/api/products/"+id, base, sellerToken, "")
	call(200, "POST", "/api/cart/items", gin.H{"productId": id, "variantId": small, "qty": 2}, buyerToken, "")
	cart := call(200, "POST", "/api/cart/items", gin.H{"productId": id, "variantId": large, "qty": 1}, buyerToken, "")
	if len(cart["items"].([]any)) != 2 {
		t.Fatal("cart replaced another variant")
	}
	cart = call(200, "DELETE", "/api/cart/items/"+id+"?variantId="+small, nil, buyerToken, "")
	if len(cart["items"].([]any)) != 1 {
		t.Fatal("removing one variant removed another")
	}
}
