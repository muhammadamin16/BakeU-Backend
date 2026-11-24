package router

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"mime/multipart"
	"net/http/httptest"
	"os"
	"reflect"
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

// No deployment URI is read. Every write/drop belongs to a new random fixture.
type p0Fixture struct {
	t                           *testing.T
	ctx                         context.Context
	db                          *mongo.Database
	r                           *gin.Engine
	seller, other, buyer, admin models.User
	market                      models.Market
	jwt                         *auth.Manager
}

func newP0Fixture(t *testing.T) *p0Fixture {
	t.Helper()
	uri := os.Getenv("BAKEU_TEST_MONGO_URI")
	if !strings.HasPrefix(uri, "mongodb://127.0.0.1:") {
		t.Skip("requires disposable loopback replica set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	db := client.Database("bakeu_p0_regression_" + bson.NewObjectID().Hex())
	t.Cleanup(func() { _ = db.Drop(ctx); _ = client.Disconnect(ctx); cancel() })
	f := &p0Fixture{t: t, ctx: ctx, db: db, jwt: auth.NewManager("p0-local-test-only-secret")}
	now := time.Now().UTC().Truncate(time.Millisecond)
	for i, u := range []*models.User{&f.seller, &f.other, &f.buyer, &f.admin} {
		*u = models.User{UID: bson.NewObjectID().Hex(), Name: "P0 fixture", Role: models.RoleCustomer, PhoneVerifiedAt: &now, CreatedAt: now}
		u.VerifiedPhoneE164 = []string{"+998900000011", "+998900000012", "+998900000013", "+998900000014"}[i]
		u.Phone = u.VerifiedPhoneE164
		if i < 2 {
			u.Role = models.RoleSeller
		}
		if _, err := db.Collection("users").InsertOne(ctx, u); err != nil {
			t.Fatal(err)
		}
	}
	for _, uid := range []string{f.seller.UID, f.other.UID} {
		m := models.Market{ID: bson.NewObjectID().Hex(), SellerID: uid, Name: "P0 bakery", Status: models.MarketApproved, Fulfillment: models.FulfillmentPickup, CreatedAt: now, UpdatedAt: now}
		if _, err := db.Collection("markets").InsertOne(ctx, m); err != nil {
			t.Fatal(err)
		}
		if uid == f.seller.UID {
			f.market = m
		}
	}
	if err := repository.NewTelegramRepo(db).EnsureIndexes(ctx); err != nil {
		t.Fatal(err)
	}
	gin.SetMode(gin.TestMode)
	f.r = New(db, config.Config{JWTSecret: "p0-local-test-only-secret", AdminUserIDs: []string{f.admin.UID}, SellerSelfService: true})
	return f
}

func (f *p0Fixture) call(want int, method, path string, body any, u *models.User) *httptest.ResponseRecorder {
	f.t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		f.t.Fatal(err)
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	if method == "POST" && path == "/api/orders" {
		req.Header.Set("Idempotency-Key", "p0-fixture-"+bson.NewObjectID().Hex())
	}
	if u != nil {
		token, err := f.jwt.Sign(u.UID)
		if err != nil {
			f.t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	f.r.ServeHTTP(w, req)
	if w.Code != want {
		f.t.Fatalf("%s %s = %d, want %d: %s", method, path, w.Code, want, w.Body.String())
	}
	return w
}

func (f *p0Fixture) snapshot(collection, id string) bson.M {
	f.t.Helper()
	var out bson.M
	if err := f.db.Collection(collection).FindOne(f.ctx, bson.M{"_id": id}).Decode(&out); err != nil {
		f.t.Fatal(err)
	}
	return out
}

func TestP0ProductsPriceMediaAndArchivePreserveHistory(t *testing.T) {
	f := newP0Fixture(t)
	base := gin.H{"title": "P0 cake", "price": 10000, "available": true}
	for _, price := range []float64{0, -1, models.MaxProductPrice + 1, 1e30} {
		base["price"] = price
		f.call(400, "POST", "/api/products", base, &f.seller)
	}
	base["price"] = models.MaxProductPrice
	var product models.Product
	if err := json.Unmarshal(f.call(201, "POST", "/api/products", base, &f.seller).Body.Bytes(), &product); err != nil {
		t.Fatal(err)
	}
	path := "/api/products/" + product.ID
	if _, err := f.db.Collection("products").UpdateOne(f.ctx, bson.M{"_id": product.ID}, bson.M{"$set": bson.M{"legacyExtra": bson.M{"preserve": true}}}); err != nil {
		t.Fatal(err)
	}
	before := f.snapshot("products", product.ID)
	base["price"] = 1e30
	f.call(400, "PUT", path, base, &f.seller)
	if !reflect.DeepEqual(before, f.snapshot("products", product.ID)) {
		t.Fatal("invalid edit changed stored product")
	}
	base["price"] = 10000
	f.call(403, "PUT", path, base, &f.other)
	f.call(403, "DELETE", path, nil, &f.other)
	f.call(403, "POST", "/api/products", base, &f.buyer)
	var imageBytes bytes.Buffer
	if err := png.Encode(&imageBytes, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	upload := func(want int, data []byte, owner models.User) *httptest.ResponseRecorder {
		t.Helper()
		var body bytes.Buffer
		form := multipart.NewWriter(&body)
		part, err := form.CreateFormFile("image", "fixture.png")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = part.Write(data); err != nil {
			t.Fatal(err)
		}
		if err = form.Close(); err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest("POST", path+"/image", &body)
		req.Header.Set("Content-Type", form.FormDataContentType())
		token, _ := f.jwt.Sign(owner.UID)
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		f.r.ServeHTTP(w, req)
		if w.Code != want {
			t.Fatalf("upload=%d want%d: %s", w.Code, want, w.Body.String())
		}
		return w
	}
	upload(403, imageBytes.Bytes(), f.other)
	upload(400, []byte("<script>not an image</script>"), f.seller)
	var media struct {
		ImageURLs []string `json:"imageURLs"`
	}
	if err := json.Unmarshal(upload(200, imageBytes.Bytes(), f.seller).Body.Bytes(), &media); err != nil || len(media.ImageURLs) != 1 {
		t.Fatal("image URL not persisted", err)
	}
	w := f.call(200, "GET", media.ImageURLs[0], nil, nil)
	if w.Header().Get("Content-Type") != "image/png" || w.Header().Get("X-Content-Type-Options") != "nosniff" || !bytes.Equal(w.Body.Bytes(), imageBytes.Bytes()) {
		t.Fatal("GridFS image round trip failed")
	}
	for range 4 {
		upload(200, imageBytes.Bytes(), f.seller)
	}
	upload(400, imageBytes.Bytes(), f.seller)
	if n, err := f.db.Collection("fs.files").CountDocuments(f.ctx, bson.M{}); err != nil || n != 5 {
		t.Fatal("image cap or rejected-upload cleanup failed")
	}
	base["title"] = "Edited P0 cake"
	f.call(200, "PUT", path, base, &f.seller)
	stored := f.snapshot("products", product.ID)
	if stored["legacyExtra"] == nil || len(stored["imageURLs"].(bson.A)) != 5 {
		t.Fatal("product edit lost legacy data/media")
	}
	orderResponse := f.call(201, "POST", "/api/orders", gin.H{"items": []gin.H{{"productId": product.ID, "qty": 1}}, "fulfillment": "pickup", "scheduledAt": time.Now().Add(24 * time.Hour).Format(time.RFC3339)}, &f.buyer)
	var checkout struct {
		Orders []models.Order `json:"orders"`
	}
	if err := json.Unmarshal(orderResponse.Body.Bytes(), &checkout); err != nil || len(checkout.Orders) != 1 {
		t.Fatal("checkout failed", err)
	}
	orderBefore := f.snapshot("orders", checkout.Orders[0].ID)
	// Archive is non-destructive and stale mutation paths cannot resurrect it.
	f.call(204, "DELETE", path, nil, &f.seller)
	f.call(404, "GET", path, nil, nil)
	f.call(404, "PUT", path, base, &f.seller)
	upload(404, imageBytes.Bytes(), f.seller)
	f.call(404, "DELETE", path, nil, &f.seller)
	list := f.call(200, "GET", "/api/products?seller="+f.seller.UID, nil, nil)
	if strings.TrimSpace(list.Body.String()) != "[]" {
		t.Fatal("archived product remained in catalog")
	}
	stored = f.snapshot("products", product.ID)
	if stored["deletedAt"] == nil || stored["available"] != false || stored["legacyExtra"] == nil {
		t.Fatal("archive lost document fields")
	}
	f.call(200, "GET", media.ImageURLs[0], nil, nil)
	if !reflect.DeepEqual(orderBefore, f.snapshot("orders", checkout.Orders[0].ID)) {
		t.Fatal("archive changed historical order")
	}
	f.call(200, "GET", "/api/orders", nil, &f.buyer)
	if count, err := repository.NewProductRepo(f.db).CountBySeller(f.ctx, f.seller.UID); err != nil || count != 0 {
		t.Fatal("archive counted as active product")
	}
	f.call(404, "POST", "/api/orders", gin.H{"items": []gin.H{{"productId": product.ID, "qty": 1}}}, &f.buyer)
}

func TestP0MaximumProductPriceCanBeOrdered(t *testing.T) {
	f := newP0Fixture(t)
	var product models.Product
	w := f.call(201, "POST", "/api/products", gin.H{"title": "Price boundary", "price": models.MaxProductPrice, "available": true}, &f.seller)
	if err := json.Unmarshal(w.Body.Bytes(), &product); err != nil {
		t.Fatal(err)
	}
	f.call(201, "POST", "/api/orders", gin.H{"items": []gin.H{{"productId": product.ID, "qty": 1}}, "fulfillment": "pickup", "scheduledAt": time.Now().Add(24 * time.Hour).Format(time.RFC3339)}, &f.buyer)
}

func TestP0ActiveStatisticsIncludeAllWorkflowsAndIsolateSeller(t *testing.T) {
	f := newP0Fixture(t)
	now := time.Now().UTC()
	statuses := []models.OrderStatus{models.OrderPending, models.OrderAccepted, models.OrderPreparing, models.OrderReady, models.OrderDelivering, models.OrderOutForDelivery}
	for _, s := range append(statuses, models.OrderCompleted, models.OrderDelivered, models.OrderCancelled, models.OrderRejected) {
		if _, err := f.db.Collection("orders").InsertOne(f.ctx, models.Order{ID: bson.NewObjectID().Hex(), SellerID: f.seller.UID, CustomerID: f.buyer.UID, Status: s, Total: 100, CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.db.Collection("orders").InsertOne(f.ctx, models.Order{ID: bson.NewObjectID().Hex(), SellerID: f.other.UID, Status: models.OrderAccepted, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	var data struct {
		Active activeStatistics `json:"activeOrders"`
	}
	if err := json.Unmarshal(f.call(200, "GET", "/api/seller/dashboard", nil, &f.seller).Body.Bytes(), &data); err != nil {
		t.Fatal(err)
	}
	if data.Active.Total != len(statuses) || len(data.Active.Breakdown) != len(statuses) {
		t.Fatalf("omitted active statuses: %+v", data.Active)
	}
	var chartSum float64
	for _, point := range data.Active.Chart {
		chartSum += point.Value
	}
	if chartSum != float64(len(statuses)) {
		t.Fatal("today's chart and total disagree", chartSum)
	}
	for _, status := range statuses {
		if data.Active.Breakdown[string(status)] != 1 {
			t.Fatal("missing status", status)
		}
	}
}

type activeStatistics struct {
	Total     int            `json:"total"`
	Breakdown map[string]int `json:"breakdown"`
	Chart     []struct {
		Value float64 `json:"value"`
	} `json:"chart"`
}

func TestP0MarketPatchesPreserveLegacyFieldsAndRejectStaleDecisions(t *testing.T) {
	f := newP0Fixture(t)
	repo := repository.NewMarketRepo(f.db)
	extra := bson.M{"nested": bson.A{"keep", int32(42)}}
	if _, err := f.db.Collection("markets").UpdateOne(f.ctx, bson.M{"_id": f.market.ID}, bson.M{"$set": bson.M{"legacyExtra": extra, "email": "legacy@fixture.invalid", "latitude": 0.0, "longitude": 0.0}}); err != nil {
		t.Fatal(err)
	}
	before := f.snapshot("markets", f.market.ID)
	moderation := gin.H{"expectedStatus": "approved", "status": "suspended", "reason": "Fixture review"}
	f.call(403, "PATCH", "/api/admin/markets/"+f.market.ID+"/status", moderation, &f.seller)
	f.call(200, "PATCH", "/api/admin/markets/"+f.market.ID+"/status", moderation, &f.admin)
	f.call(409, "PATCH", "/api/admin/markets/"+f.market.ID+"/status", moderation, &f.admin)
	after := f.snapshot("markets", f.market.ID)
	for key, value := range before {
		if key != "status" && key != "updatedAt" && key != "rejectionReason" && key != "reviewedAt" && !reflect.DeepEqual(value, after[key]) {
			t.Fatalf("moderation changed unrelated field %s", key)
		}
	}
	if n, err := f.db.Collection("market_events").CountDocuments(f.ctx, bson.M{"marketId": f.market.ID, "actorId": f.admin.UID}); err != nil || n != 1 {
		t.Fatal("moderation audit not atomic/idempotent")
	}
	m, err := repo.Get(f.ctx, f.market.ID)
	if err != nil {
		t.Fatal(err)
	}
	oldTime := m.UpdatedAt
	m.Status = models.MarketPending
	m.UpdatedAt = time.Now().UTC()
	if err := repo.UpdateExpected(f.ctx, m, models.MarketSuspended, oldTime); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateExpected(f.ctx, m, models.MarketSuspended, oldTime); !errors.Is(err, repository.ErrConflict) {
		t.Fatal("stale application/review was accepted")
	}
	if !reflect.DeepEqual(before["legacyExtra"], f.snapshot("markets", m.ID)["legacyExtra"]) {
		t.Fatal("application/review patch erased legacy data")
	}
	m.Name = "Patched legacy"
	if err := repo.Update(f.ctx, m); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before["legacyExtra"], f.snapshot("markets", m.ID)["legacyExtra"]) {
		t.Fatal("generic market patch erased legacy data")
	}
	// Old draft activation must also preserve unknown fields, owner and createdAt.
	if _, err := f.db.Collection("telegram_bindings").InsertOne(f.ctx, repository.TelegramBinding{TelegramID: "554411", UserID: f.buyer.UID, VerifiedPhoneE164: f.buyer.Phone, ConfirmedAt: time.Now().UTC(), BotStarted: true}); err != nil {
		t.Fatal(err)
	}
	draft := bson.M{"_id": bson.NewObjectID().Hex(), "sellerId": f.buyer.UID, "status": models.MarketDraft, "name": "Old draft", "createdAt": time.Now().Add(-time.Hour).UTC().Truncate(time.Millisecond), "legacyExtra": extra}
	if _, err := f.db.Collection("markets").InsertOne(f.ctx, draft); err != nil {
		t.Fatal(err)
	}
	f.call(200, "POST", "/api/market/self-service", gin.H{"firstName": "P0", "lastName": "Buyer", "name": "Activated draft", "address": "Tashkent fixture address", "lat": 0, "lng": 0}, &f.buyer)
	owner := f.snapshot("users", f.buyer.UID)
	if owner["firstName"] != "P0" || owner["lastName"] != "Buyer" || owner["name"] != "P0 Buyer" {
		t.Fatal("activation name contract diverged from registration")
	}
	stored := f.snapshot("markets", draft["_id"].(string))
	if stored["fulfillment"] != string(models.FulfillmentPickup) {
		t.Fatal("old draft did not receive a valid fulfillment default")
	}
	if !reflect.DeepEqual(stored["legacyExtra"], before["legacyExtra"]) || stored["createdAt"] != bson.NewDateTimeFromTime(draft["createdAt"].(time.Time)) || stored["sellerId"] != f.buyer.UID {
		t.Fatal("activation lost draft identity/data")
	}
}
