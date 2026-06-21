package router

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"bakeu/internal/config"
)

// TestNewRegistersRoutes ensures the router builds without panicking (which
// catches conflicting gin route registrations) and that the unauthenticated
// health endpoint works. mongo.Connect is lazy — it does not dial until an
// operation runs — so route registration never touches the network.
func TestNewRegistersRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// A lazy handle only — the driver doesn't dial until a query runs, and this
	// test never runs one, so the URI here is never actually contacted.
	client, err := mongo.Connect(options.Client().ApplyURI("mongodb://localhost:27017"))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}

	r := New(client.Database("bakeu_test"), config.Config{JWTSecret: "test-secret"})
	if r == nil {
		t.Fatal("New returned nil engine")
	}

	if len(r.Routes()) == 0 {
		t.Fatal("no routes registered")
	}

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /healthz = %d, want 200", w.Code)
	}
}
