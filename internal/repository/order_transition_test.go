package repository

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"bakeu/internal/models"
)

func TestOrderTransitionRejectsStaleStatusAndWrongSeller(t *testing.T) {
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
	db := client.Database("bakeu_order_test_" + bson.NewObjectID().Hex())
	defer db.Drop(ctx)
	repo := NewOrderRepo(db)
	_, err = repo.orders.InsertOne(ctx, bson.M{
		"_id": "order", "sellerId": "seller", "status": models.OrderPending, "total": 10000, "auditMarker": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		seller string
		status models.OrderStatus
		want   bool
	}{
		{"different-seller", models.OrderCancelled, false},
		{"seller", models.OrderPreparing, true},
		{"seller", models.OrderCancelled, false},
	} {
		got, err := repo.Transition(ctx, "order", tc.seller, models.OrderPending, tc.status)
		if err != nil || got != tc.want {
			t.Fatalf("transition by %s to %s = %t, %v", tc.seller, tc.status, got, err)
		}
	}
	var doc bson.M
	if err := repo.orders.FindOne(ctx, bson.M{"_id": "order"}).Decode(&doc); err != nil {
		t.Fatal(err)
	}
	if doc["status"] != "preparing" || doc["auditMarker"] != true {
		t.Fatal("status update did not preserve the expected order state")
	}
}
