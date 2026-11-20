package repository

import (
	"context"
	"errors"

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

// ErrCartConflict signals that another request changed the cart.
var ErrCartConflict = errors.New("cart changed")

// Mutate applies a line edit against the latest revision, retrying when another
// request edited or removed the cart first. Existing unversioned carts are
// eligible once and receive a revision on their first successful update.
func (r *CartRepo) Mutate(ctx context.Context, uid string, expected *string, edit func(*models.Cart) error) (*models.Cart, error) {
	for range 12 {
		cart, err := r.Get(ctx, uid)
		if err != nil {
			return nil, err
		}
		if expected != nil && cart.Revision != *expected {
			return nil, ErrCartConflict
		}
		filter := bson.M{"_id": uid, "revision": cart.Revision}
		if cart.Revision == "" {
			filter["$or"] = bson.A{bson.M{"revision": ""}, bson.M{"revision": bson.M{"$exists": false}}}
			delete(filter, "revision")
		}
		if err := edit(cart); err != nil {
			return nil, err
		}
		allowInsert := cart.Revision == ""
		cart.Revision = bson.NewObjectID().Hex()
		result, err := r.carts.ReplaceOne(ctx, filter, cart, options.Replace().SetUpsert(allowInsert))
		if isDuplicate(err) || (err == nil && result.MatchedCount == 0 && result.UpsertedCount == 0) {
			continue
		}
		if err != nil {
			return nil, err
		}
		return cart, nil
	}
	return nil, ErrCartConflict
}

// Clear removes the user's cart document.
func (r *CartRepo) Clear(ctx context.Context, uid string) error {
	_, err := r.carts.DeleteOne(ctx, bson.M{"_id": uid})
	return err
}
