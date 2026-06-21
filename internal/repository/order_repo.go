package repository

import (
	"context"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"bakeu/internal/models"
)

// OrderRepo provides access to the orders collection.
type OrderRepo struct {
	orders *mongo.Collection
}

func NewOrderRepo(db *mongo.Database) *OrderRepo {
	return &OrderRepo{orders: db.Collection("orders")}
}

// Create assigns a new ID, stores the order, and returns the ID.
func (r *OrderRepo) Create(ctx context.Context, o *models.Order) (string, error) {
	o.ID = bson.NewObjectID().Hex()
	if _, err := r.orders.InsertOne(ctx, o); err != nil {
		return "", err
	}
	return o.ID, nil
}

// Get loads a single order. Returns ErrNotFound if it does not exist.
func (r *OrderRepo) Get(ctx context.Context, id string) (*models.Order, error) {
	var o models.Order
	if err := r.orders.FindOne(ctx, bson.M{"_id": id}).Decode(&o); err != nil {
		if isNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &o, nil
}

// Update fully overwrites the order document at _id=o.ID.
func (r *OrderRepo) Update(ctx context.Context, o *models.Order) error {
	_, err := r.orders.ReplaceOne(ctx, bson.M{"_id": o.ID}, o)
	return err
}

// ListByCustomer returns all orders placed by a customer.
func (r *OrderRepo) ListByCustomer(ctx context.Context, customerID string) ([]models.Order, error) {
	return r.listWhere(ctx, "customerId", customerID)
}

// ListBySeller returns all orders directed to a seller.
func (r *OrderRepo) ListBySeller(ctx context.Context, sellerID string) ([]models.Order, error) {
	return r.listWhere(ctx, "sellerId", sellerID)
}

func (r *OrderRepo) listWhere(ctx context.Context, field, value string) ([]models.Order, error) {
	cur, err := r.orders.Find(ctx, bson.M{field: value})
	if err != nil {
		return nil, err
	}
	var orders []models.Order
	if err := cur.All(ctx, &orders); err != nil {
		return nil, err
	}
	return orders, nil
}
