package repository

import (
	"context"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"bakeu/internal/models"
)

// CartRepo provides access to the carts collection (one doc per user).
type CartRepo struct {
	carts *mongo.Collection
}

func NewCartRepo(db *mongo.Database) *CartRepo {
	return &CartRepo{carts: db.Collection("carts")}
}

// Get returns the user's cart, or an empty cart if none exists yet.
func (r *CartRepo) Get(ctx context.Context, uid string) (*models.Cart, error) {
	var cart models.Cart
	if err := r.carts.FindOne(ctx, bson.M{"_id": uid}).Decode(&cart); err != nil {
		if isNotFound(err) {
			return &models.Cart{UID: uid, Items: []models.CartItem{}}, nil
		}
		return nil, err
	}
	if cart.Items == nil {
		cart.Items = []models.CartItem{}
	}
	return &cart, nil
}

// Save persists the cart at _id=cart.UID.
func (r *CartRepo) Save(ctx context.Context, cart *models.Cart) error {
	_, err := r.carts.ReplaceOne(ctx, bson.M{"_id": cart.UID}, cart, options.Replace().SetUpsert(true))
	return err
}

// Clear removes the user's cart document.
func (r *CartRepo) Clear(ctx context.Context, uid string) error {
	_, err := r.carts.DeleteOne(ctx, bson.M{"_id": uid})
	return err
}
