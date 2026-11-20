package repository

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

func TestSessionRotationAndRevocation(t *testing.T) {
	uri := os.Getenv("BAKEU_TEST_MONGO_URI")
	if !strings.HasPrefix(uri, "mongodb://127.0.0.1:") {
		t.Skip("set BAKEU_TEST_MONGO_URI to a disposable loopback MongoDB")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Disconnect(context.Background())
	repo := NewSessionRepo(client.Database("bakeu_session_test_" + bson.NewObjectID().Hex()))
	defer repo.sessions.Database().Drop(ctx)

	session, original, err := repo.Create(ctx, "buyer")
	if err != nil {
		t.Fatal(err)
	}
	unknown, _ := newRefreshToken(session.ID)
	if _, _, err := repo.Rotate(ctx, unknown); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("unknown token: %v", err)
	}
	rotated, next, err := repo.Rotate(ctx, original)
	if err != nil || rotated != session || next == original {
		t.Fatalf("rotation failed: %v", err)
	}
	if _, _, err := repo.Rotate(ctx, original); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("replay: %v", err)
	}
	if active, err := repo.Active(ctx, session.ID, session.UserID); err != nil || active {
		t.Fatalf("replayed session active = %v, %v", active, err)
	}
	if _, _, err := repo.Rotate(ctx, next); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("revoked successor: %v", err)
	}

	first, token, err := repo.Create(ctx, "buyer")
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := repo.Create(ctx, "buyer")
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Revoke(ctx, token); err != nil {
		t.Fatal(err)
	}
	if active, err := repo.Active(ctx, first.ID, first.UserID); err != nil || active {
		t.Fatalf("logged-out session active = %v, %v", active, err)
	}
	if active, err := repo.Active(ctx, second.ID, second.UserID); err != nil || !active {
		t.Fatalf("other session active = %v, %v", active, err)
	}
	if err := repo.RevokeAll(ctx, "buyer"); err != nil {
		t.Fatal(err)
	}
	if active, err := repo.Active(ctx, second.ID, second.UserID); err != nil || active {
		t.Fatalf("session active after logout-all = %v, %v", active, err)
	}
}
