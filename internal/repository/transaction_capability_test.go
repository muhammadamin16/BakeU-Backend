package repository

import (
	"context"
	"errors"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/event"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"os"
	"strings"
	"testing"
	"time"
)

func TestDisposableReplicaSetTransactionCommitAndRollback(t *testing.T) {
	uri := os.Getenv("BAKEU_TEST_MONGO_URI")
	if !strings.HasPrefix(uri, "mongodb://127.0.0.1:") {
		t.Skip("requires explicit disposable loopback replica set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	commands := []string{}
	client, err := mongo.Connect(options.Client().ApplyURI(uri).SetMonitor(&event.CommandMonitor{Started: func(_ context.Context, e *event.CommandStartedEvent) { commands = append(commands, e.CommandName) }}))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Disconnect(ctx)
	db := client.Database("bakeu_tx_probe_" + bson.NewObjectID().Hex())
	defer db.Drop(ctx)
	report, err := InspectMongoReadiness(ctx, client)
	if err != nil || !report.Ready {
		t.Fatalf("readiness failed: %+v", report)
	}
	for _, name := range commands {
		if name != "ping" && name != "hello" && name != "isMaster" {
			t.Fatalf("readiness issued a non-probe command: %s", name)
		}
	}
	// Create collections before the transaction for supported older server versions.
	for _, name := range []string{"orders", "notifications"} {
		if err := db.CreateCollection(ctx, name); err != nil {
			t.Fatal(err)
		}
	}
	session, err := client.StartSession()
	if err != nil {
		t.Fatal(err)
	}
	defer session.EndSession(ctx)
	_, err = session.WithTransaction(ctx, func(tx context.Context) (any, error) {
		for _, name := range []string{"orders", "notifications"} {
			if _, e := db.Collection(name).InsertOne(tx, bson.M{"_id": "committed"}); e != nil {
				return nil, e
			}
		}
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	abort := errors.New("intentional disposable rollback")
	_, err = session.WithTransaction(ctx, func(tx context.Context) (any, error) {
		for _, name := range []string{"orders", "notifications"} {
			if _, e := db.Collection(name).InsertOne(tx, bson.M{"_id": "rolled-back"}); e != nil {
				return nil, e
			}
		}
		return nil, abort
	})
	if !errors.Is(err, abort) {
		t.Fatal(err)
	}
	for _, name := range []string{"orders", "notifications"} {
		n, e := db.Collection(name).CountDocuments(ctx, bson.M{})
		if e != nil || n != 1 {
			t.Fatalf("transaction was not atomic for %s", name)
		}
		if e := db.Collection(name).FindOne(ctx, bson.M{"_id": "committed"}).Err(); e != nil {
			t.Fatal(e)
		}
	}
}
