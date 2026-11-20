package router

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

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
}
