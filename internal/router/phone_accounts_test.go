package router

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"bakeu/internal/config"
	"bakeu/internal/models"
	"bakeu/internal/repository"
	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

func TestPhoneAccountsRealMongo(t *testing.T) {
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
	db := client.Database("bakeu_phone_accounts_" + bson.NewObjectID().Hex())
	defer db.Drop(ctx)
	if err := repository.NewTelegramRepo(db).EnsureIndexes(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Collection("users").Indexes().CreateOne(ctx, mongo.IndexModel{Keys: bson.D{{Key: "email", Value: 1}}, Options: options.Index().SetUnique(true)}); err != nil {
		t.Fatal(err)
	}
	const bridge = "test-phone-service-key-distinct-from-jwt-with-32-characters"
	cfg := config.Config{JWTSecret: "test-phone-jwt-signing-key", TelegramBridgeToken: bridge, PhoneAccounts: true, TelegramBotUsername: "BakeUuzBot", RefreshSessions: true, BrowserOrigins: []string{"https://bakeu.test"}}
	gin.SetMode(gin.TestMode)
	r := New(db, cfg)
	call := func(method, path string, body any, bearer, actor string, browser bool, cookie string) (int, map[string]any, string) {
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest(method, path, bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		if actor != "" {
			req.Header.Set("X-Telegram-User-Id", actor)
		}
		if browser {
			req.Header.Set("Origin", "https://bakeu.test")
			req.Header.Set("X-BakeU-Session", "browser")
		}
		if cookie != "" {
			req.Header.Set("Cookie", cookie)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		out := map[string]any{}
		if w.Code != 204 {
			_ = json.Unmarshal(w.Body.Bytes(), &out)
		}
		return w.Code, out, w.Header().Get("Set-Cookie")
	}
	expect := func(want int, method, path string, body any, bearer, actor string) map[string]any {
		t.Helper()
		got, out, _ := call(method, path, body, bearer, actor, false, "")
		if got != want {
			t.Fatalf("%s: got %d want %d: %v", path, got, want, out)
		}
		return out
	}
	start := func(purpose, phone, email, role string) string {
		t.Helper()
		payload := gin.H{"purpose": purpose, "phone": phone, "role": role, "name": "Test user", "marketName": "Test bakery"}
		if email != "" {
			payload["email"], payload["password"] = email, "test-password-123"
		}
		out := expect(201, "POST", "/api/auth/phone/start", payload, "", "")
		id := out["id"].(string)
		u, e := url.Parse(out["telegramUrl"].(string))
		if e != nil || u.Host != "t.me" || len(u.Query().Get("start")) > 64 || len(id) != 48 {
			t.Fatal("invalid Telegram launch link")
		}
		return id
	}
	contact := func(id, phone, actor string) string {
		t.Helper()
		return expect(200, "POST", "/api/integrations/telegram/auth/phone/contact", gin.H{"id": id, "phone": phone}, bridge, actor)["code"].(string)
	}
	expect(409, "POST", "/api/auth/signup", gin.H{}, "", "")
	expect(400, "POST", "/api/auth/phone/start", gin.H{"purpose": "register", "phone": "+998901234567", "role": "admin"}, "", "")
	id := start("register", "90 123 45 67", "buyer@test.invalid", "customer")
	if n, _ := db.Collection("users").CountDocuments(ctx, bson.M{}); n != 0 {
		t.Fatal("created user without proof")
	}
	expect(401, "POST", "/api/integrations/telegram/auth/phone/contact", gin.H{"id": id, "phone": "+998901234567"}, "", "401")
	expect(403, "POST", "/api/integrations/telegram/auth/phone/contact", gin.H{"id": id, "phone": "+998901234567"}, bridge, "-100")
	expect(409, "POST", "/api/integrations/telegram/auth/phone/contact", gin.H{"id": id, "phone": "+998909999999"}, bridge, "401")
	code := contact(id, "+998901234567", "401")
	expect(409, "POST", "/api/integrations/telegram/auth/phone/contact", gin.H{"id": id, "phone": "+998901234567"}, bridge, "401")
	var stored bson.M
	if e := db.Collection("phone_proofs").FindOne(ctx, bson.M{}).Decode(&stored); e != nil {
		t.Fatal(e)
	}
	if stored["codeHash"] == code || stored["_id"] == id {
		t.Fatal("stored raw code or launch token")
	}
	status, registered, cookie := call("POST", "/api/auth/phone/verify", gin.H{"id": id, "code": code}, "", "", true, "")
	if status != 200 || !strings.Contains(cookie, "HttpOnly") || !strings.Contains(cookie, "Secure") || registered["refreshToken"] != nil {
		t.Fatalf("shared browser session not created: %d %v %s", status, registered, cookie)
	}
	token := registered["token"].(string)
	uid := registered["user"].(map[string]any)["uid"].(string)
	expect(409, "POST", "/api/auth/phone/verify", gin.H{"id": id, "code": code}, "", "")
	me := expect(200, "GET", "/api/me", nil, token, "")
	if me["verifiedPhoneE164"] != "+998901234567" || me["passwordHash"] != nil {
		t.Fatal("wrong identity or password leak")
	}
	// Signed Telegram launches use a short one-use ticket and the same Go session.
	expect(401, "POST", "/api/integrations/telegram/auth/launch", gin.H{}, "", "401")
	if expect(200, "POST", "/api/integrations/telegram/auth/launch", gin.H{}, bridge, "900")["linked"] != false {
		t.Fatal("unlinked actor inherited a Go user")
	}
	launch := expect(200, "POST", "/api/integrations/telegram/auth/launch", gin.H{}, bridge, "401")
	if launch["uid"] != uid || launch["token"] != nil {
		t.Fatal("wrong launch identity or service-issued browser token")
	}
	launchBody := gin.H{"ticket": launch["ticket"]}
	expect(400, "POST", "/api/auth/phone/telegram", launchBody, "", "")
	launchStatus, launched, launchCookie := call("POST", "/api/auth/phone/telegram", launchBody, "", "", true, "")
	if launchStatus != 200 || launched["user"].(map[string]any)["uid"] != uid || !strings.Contains(launchCookie, "HttpOnly") {
		t.Fatal("launch did not establish the shared Go session")
	}
	if replay, _, _ := call("POST", "/api/auth/phone/telegram", launchBody, "", "", true, ""); replay != 409 {
		t.Fatal("launch ticket replayed")
	}
	expiring := expect(200, "POST", "/api/integrations/telegram/auth/launch", gin.H{}, bridge, "401")
	if _, e := db.Collection("telegram_login_tickets").UpdateMany(ctx, bson.M{"userId": uid, "used": false}, bson.M{"$set": bson.M{"expiresAt": time.Now().Add(-time.Second)}}); e != nil {
		t.Fatal(e)
	}
	if status, _, _ := call("POST", "/api/auth/phone/telegram", gin.H{"ticket": expiring["ticket"]}, "", "", true, ""); status != 409 {
		t.Fatal("expired launch ticket accepted")
	}
	expect(200, "POST", "/api/profile", gin.H{"name": "Edited", "phone": "+998909999999", "verifiedPhoneE164": "+998909999999"}, token, "")
	login := start("login", "+998901234567", "", "")
	loginCode := contact(login, "+998901234567", "401")
	result := expect(200, "POST", "/api/auth/phone/verify", gin.H{"id": login, "code": loginCode}, "", "")
	if result["user"].(map[string]any)["uid"] != uid {
		t.Fatal("login duplicated identity after contact edit")
	}
	// A reloaded router restores the exact same account using the shared refresh cookie.
	r = New(db, cfg)
	refreshStatus, _, _ := call("POST", "/api/auth/refresh", nil, "", "", true, strings.Split(cookie, ";")[0])
	if refreshStatus != 200 {
		t.Fatal("session did not survive restart")
	}
	// Concurrent registration for the same proven phone cannot create a second UID.
	ids := []string{start("register", "+998901234568", "", "seller"), start("register", "+998901234568", "", "seller")}
	codes := []string{contact(ids[0], "+998901234568", "402"), contact(ids[1], "+998901234568", "403")}
	statuses := make([]int, 2)
	var wg sync.WaitGroup
	for i := range ids {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			statuses[i], _, _ = call("POST", "/api/auth/phone/verify", gin.H{"id": ids[i], "code": codes[i]}, "", "", false, "")
		}(i)
	}
	wg.Wait()
	if !(statuses[0] == 200 && statuses[1] == 409 || statuses[1] == 200 && statuses[0] == 409) {
		t.Fatalf("concurrent verification %v", statuses)
	}
	if n, _ := db.Collection("users").CountDocuments(ctx, bson.M{}); n != 2 {
		t.Fatal("partial duplicate signup")
	}
	if n, _ := db.Collection("markets").CountDocuments(ctx, bson.M{"status": models.MarketDraft}); n != 1 {
		t.Fatal("seller draft missing or duplicated")
	}
	if n, _ := db.Collection("markets").CountDocuments(ctx, bson.M{"status": models.MarketApproved}); n != 0 {
		t.Fatal("seller auto-approved")
	}
	// Five incorrect codes lock this proof even when the correct code arrives later.
	locked := start("login", "+998901234567", "", "")
	good := contact(locked, "+998901234567", "401")
	wrong := "000000"
	if good == wrong {
		wrong = "111111"
	}
	for i := 0; i < 5; i++ {
		expect(403, "POST", "/api/auth/phone/verify", gin.H{"id": locked, "code": wrong}, "", "")
	}
	if got, _, _ := call("POST", "/api/auth/phone/verify", gin.H{"id": locked, "code": good}, "", "", false, ""); got != 429 && got != 409 {
		t.Fatal("exhausted proof accepted")
	}
	// Freeze blocks password and OTP login even with the right proof.
	beforeFreeze := expect(200, "POST", "/api/integrations/telegram/auth/launch", gin.H{}, bridge, "401")
	_, err = db.Collection("users").UpdateOne(ctx, bson.M{"_id": uid}, bson.M{"$set": bson.M{"frozen": true}})
	if err != nil {
		t.Fatal(err)
	}
	expect(403, "POST", "/api/auth/login", gin.H{"email": "buyer@test.invalid", "password": "test-password-123"}, "", "")
	expect(403, "GET", "/api/me", nil, token, "")
	expect(403, "GET", "/api/orders", nil, token, "")
	if status, _, _ := call("POST", "/api/auth/phone/telegram", gin.H{"ticket": beforeFreeze["ticket"]}, "", "", true, ""); status != 403 {
		t.Fatal("a ticket restored a frozen account")
	}
	frozen := start("login", "+998901234567", "", "")
	frozenCode := contact(frozen, "+998901234567", "401")
	expect(403, "POST", "/api/auth/phone/verify", gin.H{"id": frozen, "code": frozenCode}, "", "")
}
