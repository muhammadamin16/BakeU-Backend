// Package mongoclient initializes the MongoDB connection and the database
// handle shared across the app, replacing the former Firebase clients.
package mongoclient

import (
	"context"
	"errors"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/mongo/readpref"

	"bakeu/internal/config"
	"bakeu/internal/repository"
)

// Clients bundles the live Mongo client and the working database handle. The
// shape mirrors the former firebaseclient.Clients so the rest of the app wires
// up the same way.
type Clients struct {
	Client *mongo.Client
	DB     *mongo.Database
}

// New connects to MongoDB, verifies the connection with a ping, and ensures the
// indexes the app relies on exist.
func New(ctx context.Context, cfg config.Config) (*Clients, error) {
	if err := cfg.ValidateMongoConfig(); err != nil {
		return nil, err
	}
	client, err := mongo.Connect(options.Client().ApplyURI(cfg.MongoURI))
	if err != nil {
		return nil, errors.New("MongoDB client configuration is invalid; check MONGODB_URI syntax and DNS")
	}
	if err := client.Ping(ctx, readpref.Primary()); err != nil {
		_ = client.Disconnect(ctx)
		var command mongo.CommandError
		if errors.As(err, &command) && (command.Code == 18 || command.Code == 13) {
			return nil, errors.New("MongoDB authentication or authorization failed; check the account and authSource")
		}
		return nil, errors.New("MongoDB primary is unreachable or authentication failed; check private networking, replicaSet, TLS and credentials")
	}

	db := client.Database(cfg.MongoDB)
	if err := ensureIndexes(ctx, db); err != nil {
		_ = client.Disconnect(ctx)
		return nil, errors.New("cannot ensure MongoDB indexes; check database permissions and unique-index conflicts")
	}

	if cfg.RefreshSessions {
		_, err := db.Collection("auth_sessions").Indexes().CreateMany(ctx, []mongo.IndexModel{
			{Keys: bson.D{{Key: "expiresAt", Value: 1}}, Options: options.Index().SetExpireAfterSeconds(0)},
			{Keys: bson.D{{Key: "userId", Value: 1}}},
		})
		if err != nil {
			_ = client.Disconnect(ctx)
			return nil, errors.New("cannot ensure MongoDB session indexes; check database permissions and unique-index conflicts")
		}
	}

	if err := repository.NewTelegramRepo(db).EnsureIndexes(ctx); err != nil {
		_ = client.Disconnect(ctx)
		return nil, errors.New("cannot ensure MongoDB Telegram indexes; check database permissions and unique-index conflicts")
	}
	return &Clients{Client: client, DB: db}, nil
}

// ensureIndexes creates the indexes the queries depend on. CreateMany is
// idempotent, so this is safe to run on every startup.
func ensureIndexes(ctx context.Context, db *mongo.Database) error {
	if _, err := db.Collection("users").Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: "email", Value: 1}},
		Options: options.Index().SetUnique(true),
	}); err != nil {
		return err
	}
	if _, err := db.Collection("products").Indexes().CreateMany(ctx, []mongo.IndexModel{
		{Keys: bson.D{{Key: "sellerId", Value: 1}}},
		{Keys: bson.D{{Key: "category", Value: 1}}},
	}); err != nil {
		return err
	}
	if _, err := db.Collection("orders").Indexes().CreateMany(ctx, []mongo.IndexModel{
		{Keys: bson.D{{Key: "customerId", Value: 1}}},
		{Keys: bson.D{{Key: "sellerId", Value: 1}}},
		// Every seller-dashboard query is "this seller's orders in a date range".
		{Keys: bson.D{{Key: "sellerId", Value: 1}, {Key: "createdAt", Value: 1}}},
	}); err != nil {
		return err
	}
	if _, err := db.Collection("reviews").Indexes().CreateMany(ctx, []mongo.IndexModel{
		{Keys: bson.D{{Key: "productId", Value: 1}}},
		{Keys: bson.D{{Key: "sellerId", Value: 1}}},
		// One review per customer per product. NOTE: creation fails (and the app
		// won't start) if existing data already has duplicate pairs — dedupe first.
		{
			Keys:    bson.D{{Key: "productId", Value: 1}, {Key: "customerId", Value: 1}},
			Options: options.Index().SetUnique(true),
		},
	}); err != nil {
		return err
	}
	if _, err := db.Collection("markets").Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: "sellerId", Value: 1}},
		Options: options.Index().SetUnique(true),
	}); err != nil {
		return err
	}
	return nil
}

// Close disconnects the Mongo client. Safe to call on a partially initialized
// struct.
func (c *Clients) Close() {
	if c.Client != nil {
		_ = c.Client.Disconnect(context.Background())
	}
}
