package router

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
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
	"golang.org/x/crypto/bcrypt"
)

func TestTelegramOrdersRealMongo(t *testing.T) {
	uri := os.Getenv("BAKEU_TEST_MONGO_URI")
	if !strings.HasPrefix(uri, "mongodb://127.0.0.1:") {
		t.Skip("requires disposable loopback MongoDB replica set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	client, e := mongo.Connect(options.Client().ApplyURI(uri))
	if e != nil {
		t.Fatal(e)
	}
	defer client.Disconnect(ctx)
	db := client.Database("bakeu_telegram_test_" + bson.NewObjectID().Hex())
	defer db.Drop(ctx)
	tr := repository.NewTelegramRepo(db)
	if e = tr.EnsureIndexes(ctx); e != nil {
		t.Fatal(e)
	}
	const secret = "test-telegram-bridge-secret-with-at-least-32-characters"
	const jwtSecret = "test-jwt-secret-which-is-different"
	const password = "test-password-123"
	hash, _ := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	user := func(role models.Role, name, phone string) *models.User {
		u := &models.User{Role: role, Name: name, Phone: phone, Email: name + "@test.invalid", PasswordHash: string(hash), CreatedAt: time.Now().UTC()}
		if e := repository.NewUserRepo(db).Create(ctx, u); e != nil {
			t.Fatal(e)
		}
		return u
	}
	buyer := user(models.RoleCustomer, "buyer", "+998901234567")
	seller := user(models.RoleSeller, "seller", "+998901234568")
	other := user(models.RoleSeller, "other", "+998901234569")
	market := func(u *models.User) string {
		id := bson.NewObjectID().Hex()
		_, e := db.Collection("markets").InsertOne(ctx, models.Market{ID: id, SellerID: u.UID, Name: u.Name, Status: models.MarketApproved, Fulfillment: models.FulfillmentBoth, Address: "Tashkent"})
		if e != nil {
			t.Fatal(e)
		}
		return id
	}
	shop1, shop2 := market(seller), market(other)
	product := func(u *models.User, price float64) string {
		id := bson.NewObjectID().Hex()
		_, e := db.Collection("products").InsertOne(ctx, models.Product{ID: id, SellerID: u.UID, Title: "Cake", Price: price, Available: true})
		if e != nil {
			t.Fatal(e)
		}
		return id
	}
	p1, p2 := product(seller, 50000), product(other, 12000)
	token := func(u *models.User) string {
		v, e := auth.NewManager(jwtSecret).Sign(u.UID)
		if e != nil {
			t.Fatal(e)
		}
		return v
	}
	gin.SetMode(gin.TestMode)
	r := New(db, config.Config{JWTSecret: jwtSecret, TelegramBridgeToken: secret, TelegramSiteURL: "https://bakeu.test"})
	call := func(method, path string, body any, bearer, actor, key string) (int, map[string]any) {
		var raw []byte
		if body != nil {
			raw, _ = json.Marshal(body)
		}
		req := httptest.NewRequest(method, path, bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		if actor != "" {
			req.Header.Set("X-Telegram-User-Id", actor)
		}
		if key != "" {
			req.Header.Set("Idempotency-Key", key)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		out := map[string]any{}
		if e := json.Unmarshal(w.Body.Bytes(), &out); e != nil {
			t.Errorf("invalid JSON %s: %s", path, w.Body.String())
		}
		return w.Code, out
	}
	expect := func(want int, method, path string, body any, bearer, actor, key string) map[string]any {
		t.Helper()
		code, out := call(method, path, body, bearer, actor, key)
		if code != want {
			t.Fatalf("%s %s: got %d want %d: %v", method, path, code, want, out)
		}
		return out
	}
	bridge := "/api/integrations/telegram"
	expect(401, "GET", bridge+"/me", nil, "", "888", "")
	expect(403, "GET", bridge+"/me", nil, secret, "-100", "")
	if expect(200, "GET", bridge+"/me", nil, secret, "888", "")["linked"] != false {
		t.Fatal("phone must not imply a binding")
	}
	bind := func(u *models.User, actor, role string) {
		challenge := expect(200, "POST", bridge+"/link/challenges", gin.H{"phone": u.Phone, "requestedRole": role}, secret, actor, "")
		id := challenge["id"].(string)
		if actor == "888" {
			expect(403, "POST", "/api/me/telegram/link/confirm", gin.H{"challengeId": id, "password": password}, token(buyer), "", "")
		}
		expect(200, "POST", "/api/me/telegram/link/confirm", gin.H{"challengeId": id, "password": password}, token(u), "", "")
		expect(409, "POST", "/api/me/telegram/link/confirm", gin.H{"challengeId": id, "password": password}, token(u), "", "")
	}
	bind(seller, "888", "SELLER")
	bind(buyer, "999", "CLIENT")
	bind(other, "777", "SELLER")
	expect(403, "GET", bridge+"/shop", nil, secret, "999", "")
	own := expect(200, "GET", bridge+"/shop", nil, secret, "888", "")
	if own["ownerId"] != seller.UID {
		t.Fatal("wrong shop owner")
	}
	future := time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339)
	body := gin.H{"items": []gin.H{{"productId": p1, "qty": 2, "price": 1}, {"productId": p2, "qty": 1}}, "deliveries": gin.H{shop1: gin.H{"fulfillment": "delivery", "deliveryAddress": "Tashkent", "scheduledAt": future, "location": gin.H{"latitude": 0, "longitude": 0}}, shop2: gin.H{"fulfillment": "pickup", "deliveryAddress": "private", "scheduledAt": future, "location": gin.H{"latitude": 41, "longitude": 69}}}}
	created := expect(201, "POST", "/api/orders", body, token(buyer), "", "checkout-one")
	rawOrders, _ := json.Marshal(created["orders"])
	var orders []models.Order
	if e = json.Unmarshal(rawOrders, &orders); e != nil {
		t.Fatal(e)
	}
	if len(orders) != 2 {
		t.Fatal("multi-seller checkout must create two orders")
	}
	var delivery, pickup models.Order
	for _, o := range orders {
		if o.SellerID == seller.UID {
			delivery = o
		} else {
			pickup = o
		}
	}
	if delivery.Total != 100000 || delivery.Location == nil || delivery.Location.Latitude != 0 || delivery.BuyerPhone != buyer.Phone {
		t.Fatal("server price/contact/location snapshot missing")
	}
	if pickup.Location != nil || pickup.DeliveryAddress != "" {
		t.Fatal("pickup retained buyer GPS/address")
	}
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); expect(200, "POST", "/api/orders", body, token(buyer), "", "checkout-one") }()
	}
	wg.Wait()
	count, _ := db.Collection("orders").CountDocuments(ctx, bson.M{})
	if count != 2 {
		t.Fatal("checkout replay duplicated orders")
	}
	altered := gin.H{"items": []gin.H{{"productId": p1, "qty": 3}}, "fulfillment": "delivery", "deliveryAddress": "Tashkent", "scheduledAt": future}
	expect(409, "POST", "/api/orders", altered, token(buyer), "", "checkout-one")
	partial := gin.H{"items": []gin.H{{"productId": p1, "qty": 1}}, "fulfillment": "delivery", "deliveryAddress": "Tashkent", "scheduledAt": future, "location": gin.H{"latitude": 41}}
	expect(400, "POST", "/api/orders", partial, token(buyer), "", "partial-location")
	expect(403, "GET", bridge+"/orders/"+delivery.ID, nil, secret, "777", "")
	view := expect(200, "GET", bridge+"/orders/"+delivery.ID, nil, secret, "999", "")
	if view["address"] != "Tashkent" {
		t.Fatal("missing address projection")
	}
	expect(403, "PATCH", bridge+"/orders/"+delivery.ID+"/status", gin.H{"expectedStatus": "pending", "status": "accepted"}, secret, "999", "tg:denied")
	from := "pending"
	for i, to := range []string{"accepted", "preparing", "ready", "delivering", "delivered", "completed"} {
		path := bridge + "/orders/" + delivery.ID + "/status"
		req := gin.H{"expectedStatus": from, "status": to}
		key := fmt.Sprintf("tg:status-%d", i)
		expect(200, "PATCH", path, req, secret, "888", key)
		expect(200, "PATCH", path, req, secret, "888", key)
		expect(409, "PATCH", path, req, secret, "888", key+"-stale")
		from = to
	}
	stored, e := repository.NewOrderRepo(db).Get(ctx, delivery.ID)
	if e != nil || len(stored.History) != 7 || stored.Status != models.OrderCompleted {
		t.Fatal("status/history was not persisted exactly once")
	}
	from = "pending"
	for i, to := range []string{"accepted", "preparing", "ready", "completed"} {
		if to == "completed" {
			expect(409, "PATCH", bridge+"/orders/"+pickup.ID+"/status", gin.H{"expectedStatus": "ready", "status": "delivering"}, secret, "777", "tg:pickup-courier")
		}
		expect(200, "PATCH", bridge+"/orders/"+pickup.ID+"/status", gin.H{"expectedStatus": from, "status": to}, secret, "777", fmt.Sprintf("tg:pickup-%d", i))
		from = to
	}
	// New repo/handler instances read the same Mongo records after a simulated restart.
	reloaded := New(db, config.Config{JWTSecret: jwtSecret, TelegramBridgeToken: secret, TelegramSiteURL: "https://bakeu.test"})
	r = reloaded
	expect(200, "GET", bridge+"/orders?audience=buyer&view=history&page=0&limit=5", nil, secret, "999", "")
	inbox := expect(200, "GET", "/api/me/notifications", nil, token(buyer), "", "")
	if len(inbox["items"].([]any)) != 12 {
		t.Fatalf("missing durable buyer notifications: %v", inbox)
	}
	// Outbox begin and ACK are idempotent, and an expired sending lease is uncertain, not resendable.
	jobs, e := tr.Claim(ctx)
	if e != nil || len(jobs) == 0 {
		t.Fatalf("claim: %v", e)
	}
	job := jobs[0]
	started, e := tr.Begin(ctx, job.ID, job.LeaseToken)
	if e != nil || !started {
		t.Fatalf("begin: %v", e)
	}
	started, e = tr.Begin(ctx, job.ID, job.LeaseToken)
	if e != nil || started {
		t.Fatal("duplicate begin authorized another send")
	}
	_, _ = db.Collection("order_notifications").UpdateOne(ctx, bson.M{"_id": job.ID}, bson.M{"$set": bson.M{"leaseUntil": time.Now().Add(-time.Minute)}})
	_, e = tr.Claim(ctx)
	if e != nil {
		t.Fatal(e)
	}
	var n repository.Notification
	_ = db.Collection("order_notifications").FindOne(ctx, bson.M{"_id": job.ID}).Decode(&n)
	if n.State != "uncertain" {
		t.Fatal("sending lease may not be replayed after worker loss")
	}
	if e = tr.Ack(ctx, job.ID, job.LeaseToken, 123); e != nil {
		t.Fatal(e)
	}
	if e = tr.Ack(ctx, job.ID, job.LeaseToken, 123); e != nil {
		t.Fatal(e)
	}

	// Preserve the earlier five-state API and the first Codex's durable history format.
	legacyID := bson.NewObjectID().Hex()
	_, e = db.Collection("orders").InsertOne(ctx, models.Order{ID: legacyID, CustomerID: buyer.UID, SellerID: seller.UID, Items: []models.OrderItem{{ProductID: p1, Title: "Legacy cake", Price: 1000, Qty: 1}}, Total: 1000, Status: models.OrderPending, CreatedAt: time.Now().UTC()})
	if e != nil {
		t.Fatal(e)
	}
	legacy := expect(200, "PATCH", "/api/orders/"+legacyID+"/status", gin.H{"status": "preparing"}, token(seller), "", "")
	if legacy["status"] != "preparing" || len(legacy["history"].([]any)) != 1 {
		t.Fatal("legacy status/history compatibility lost")
	}
	// Distinct concurrent clicks must produce one committed event, not two histories.
	raceID := bson.NewObjectID().Hex()
	raceOrder := delivery
	raceOrder.ID = raceID
	raceOrder.Status = models.OrderPending
	raceOrder.History = []models.OrderEvent{{Status: models.OrderPending, At: time.Now().UTC(), Version: 1}}
	raceOrder.Version = 1
	if _, e = db.Collection("orders").InsertOne(ctx, raceOrder); e != nil {
		t.Fatal(e)
	}
	results := make(chan int, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			code, _ := call("PATCH", bridge+"/orders/"+raceID+"/status", gin.H{"expectedStatus": "pending", "status": "accepted"}, secret, "888", fmt.Sprintf("tg:race-%d", i))
			results <- code
		}(i)
	}
	wg.Wait()
	close(results)
	success, conflict := 0, 0
	for code := range results {
		if code == 200 {
			success++
		}
		if code == 409 {
			conflict++
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatal("concurrent transitions were not serialized")
	}
	raced, e := repository.NewOrderRepo(db).Get(ctx, raceID)
	if e != nil || len(raced.History) != 2 {
		t.Fatal("race duplicated history")
	}

	// Binding uniqueness survives a second actor with the same valid account proof.
	challenge := expect(200, "POST", bridge+"/link/challenges", gin.H{"phone": buyer.Phone, "requestedRole": "CLIENT"}, secret, "123", "")
	expect(409, "POST", "/api/me/telegram/link/confirm", gin.H{"challengeId": challenge["id"], "password": password}, token(buyer), "", "")
}
