package repository

import (
	"context"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

// TransactionsAvailable inspects topology without a write or transaction probe.
// A successful ping on a standalone Railway Mongo instance is not sufficient.
func TransactionsAvailable(ctx context.Context, client *mongo.Client) (bool, error) {
	report, err := InspectMongoReadiness(ctx, client)
	return report.Ready, err
}

func (r *TelegramRepo) DiagnosticCounts(ctx context.Context) (map[string]int64, error) {
	counts := map[string]int64{}
	for _, name := range []string{"users", "markets", "products", "orders", "telegram_bindings"} {
		n, err := r.db.Collection(name).CountDocuments(ctx, bson.M{})
		if err != nil {
			return nil, err
		}
		counts[name] = n
	}
	for _, state := range []string{"pending", "retry", "leased", "sending", "sent", "blocked", "failed", "uncertain", "unlinked"} {
		n, err := r.db.Collection("order_notifications").CountDocuments(ctx, bson.M{"state": state})
		if err != nil {
			return nil, err
		}
		counts["notifications_"+state] = n
	}
	return counts, nil
}

func (r *TelegramRepo) TransactionsAvailable(ctx context.Context) (bool, error) {
	return TransactionsAvailable(ctx, r.db.Client())
}

func (r *TelegramRepo) StorageReadiness(ctx context.Context) (MongoReadiness, error) {
	return InspectMongoReadiness(ctx, r.db.Client())
}
