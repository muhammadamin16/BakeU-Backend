package handlers

import (
	"bakeu/internal/repository"
	"context"
	"github.com/gin-gonic/gin"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestReachableStandaloneMongoIsNotTransactionReady(t *testing.T) {
	uri := os.Getenv("BAKEU_TEST_STANDALONE_MONGO_URI")
	if !strings.HasPrefix(uri, "mongodb://127.0.0.1:") {
		t.Skip("requires disposable loopback standalone Mongo")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Disconnect(ctx)
	if err = client.Ping(ctx, nil); err != nil {
		t.Fatal(err)
	}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/healthz", Health)
	r.GET("/readyz", NewHealthHandler(client).Ready)
	r.GET("/capabilities", (&OrderHandler{orders: repository.NewOrderRepo(client.Database("bakeu_readiness_test"))}).Capabilities)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/healthz", nil))
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/readyz", nil))
	if w.Code != 503 || !strings.Contains(w.Body.String(), "MONGO_TRANSACTIONS_REQUIRED") || !strings.Contains(w.Body.String(), "MONGO_STANDALONE") {
		t.Fatal(w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/capabilities", nil))
	if w.Code != 503 || !strings.Contains(w.Body.String(), "MONGO_STANDALONE") || strings.Contains(w.Body.String(), "workflowVersion") {
		t.Fatal(w.Code, w.Body.String())
	}

}
