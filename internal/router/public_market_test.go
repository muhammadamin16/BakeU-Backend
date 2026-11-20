package router

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"bakeu/internal/auth"
	"bakeu/internal/config"
)

func TestPublicMarketsReadFromMongoDB(t *testing.T) {
	uri := os.Getenv("BAKEU_TEST_MONGO_URI")
	if !strings.HasPrefix(uri, "mongodb://127.0.0.1:") {
		t.Skip("set BAKEU_TEST_MONGO_URI to a disposable loopback MongoDB")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Disconnect(ctx)
	db := client.Database("bakeu_catalog_test_" + bson.NewObjectID().Hex())
	defer db.Drop(ctx)
	_, err = db.Collection("markets").InsertMany(ctx, []bson.M{
		{"_id": "a", "sellerId": "owner-a", "status": "approved", "name": "Bakery", "latitude": 41.3, "longitude": 69.2, "email": "private@example.com", "phone": "private"},
		{"_id": "b", "sellerId": "owner-b", "status": "pending"},
		{"_id": "c", "sellerId": "owner-c", "status": "approved", "name": "Bakery 2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	r := New(db, config.Config{JWTSecret: "test-secret"})
	get := func(path string, status int) string {
		t.Helper()
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != status {
			t.Fatalf("%s returned %d: %s", path, w.Code, w.Body.String())
		}
		return w.Body.String()
	}
	var first struct {
		Items []struct{ ID string }
		Next  string
	}
	if err := json.Unmarshal([]byte(get("/api/markets?limit=1", 200)), &first); err != nil ||
		len(first.Items) != 1 || first.Items[0].ID != "a" || first.Next != "a" {
		t.Fatalf("unexpected first page: %+v, %v", first, err)
	}
	if body := get("/api/markets?limit=1&after=a", 200); !strings.Contains(body, `"id":"c"`) ||
		!strings.Contains(body, `"next":""`) {
		t.Fatalf("unexpected second page: %s", body)
	}
	body := get("/api/markets/a", 200)
	if strings.Contains(body, "private") || !strings.Contains(body, `"lat":41.3`) {
		t.Fatalf("unexpected storefront data: %s", body)
	}
	get("/api/markets/b", 404)
	get("/api/markets?limit=0", 400)
	_, err = db.Collection("users").InsertMany(ctx, []bson.M{
		{"_id": "owner-a", "role": "seller"},
		{"_id": "owner-b", "role": "seller"},
		{"_id": "buyer", "role": "customer"},
	})
	if err != nil {
		t.Fatal(err)
	}
	authorized := func(method, path, userID, body, contentType string, want int) string {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", contentType)
		if userID != "" {
			token, err := auth.NewManager("test-secret").Sign(userID)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != want {
			t.Fatalf("%s %s by %s returned %d: %s", method, path, userID, w.Code, w.Body.String())
		}
		return w.Body.String()
	}
	// Ownership and HTTP response checks share the same seeded storefronts.
	patch := func(userID, body string, want int) {
		authorized(http.MethodPatch, "/api/market/mine", userID, body, "application/json", want)
	}
	patch("", `{"name":"Denied"}`, 401)
	patch("buyer", `{"name":"Denied"}`, 403)
	patch("owner-b", `{"name":"Denied","sellerId":"owner-a"}`, 404)
	patch("owner-a", `{"lat":91,"lng":69}`, 400)
	patch("owner-a", `{"lat":41.4}`, 400)
	patch("owner-a", `{"name":"New name","lat":41.4,"lng":69.3,"sellerId":"buyer","status":"pending"}`, 200)
	body = get("/api/markets/a", 200)
	if !strings.Contains(body, `"sellerId":"owner-a"`) || !strings.Contains(body, `"name":"New name"`) ||
		!strings.Contains(body, `"lat":41.4`) {
		t.Fatalf("unexpected updated storefront: %s", body)
	}
	patch("owner-a", `{"clearPoint":true}`, 200)
	if body := get("/api/markets/a", 200); strings.Contains(body, `"lat":`) || strings.Contains(body, `"lng":`) {
		t.Fatalf("coordinates stayed visible after clearing: %s", body)
	}

	const metadata = `{"title":"Cake","price":10000,"available":true}`
	const variants = `{"title":"Cake","price":10000,"available":true,"variants":[{"name":"Large","price":20000}]}`
	created := authorized("POST", "/api/products", "owner-a", variants, "application/json", 201)
	var product struct{ ID string }
	if err := json.Unmarshal([]byte(created), &product); err != nil || product.ID == "" {
		t.Fatal("expected product ID")
	}
	path := "/api/products/" + product.ID
	var file bytes.Buffer
	writer := multipart.NewWriter(&file)
	part, err := writer.CreateFormFile("image", "cake.png")
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(part, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	uploaded := authorized("POST", path+"/image", "owner-a", file.String(), writer.FormDataContentType(), 200)
	var media struct{ ImageURLs []string }
	if err := json.Unmarshal([]byte(uploaded), &media); err != nil || len(media.ImageURLs) != 1 {
		t.Fatalf("expected image URL: %s", uploaded)
	}
	authorized("PUT", path, "owner-a", metadata, "application/json", 200)
	r = New(db, config.Config{JWTSecret: "test-secret"})
	body = get("/api/products?seller=owner-a", 200)
	if !strings.Contains(body, `"name":"Large"`) || !strings.Contains(body, media.ImageURLs[0]) {
		t.Fatalf("catalog lost product data: %s", body)
	}
	get(media.ImageURLs[0], 200)

}
