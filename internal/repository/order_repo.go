package repository

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"bakeu/internal/models"
)

// bucketTimezone is the offset used to bucket orders into local calendar days
// for the dashboard charts (Asia/Tashkent, UTC+5, no DST).
const bucketTimezone = "+05:00"

// DayBucket is one day's aggregated value (revenue or order count). Day is the
// start of the local calendar day the orders fall into.
type DayBucket struct {
	Day   time.Time `bson:"_id"`
	Value float64   `bson:"value"`
}

// TopCustomer is a customer's spend within a period. Name is not populated by
// the repository (resolved by the caller from the users collection).
type TopCustomer struct {
	CustomerID string  `bson:"_id" json:"customerId"`
	Name       string  `bson:"-" json:"name"`
	Total      float64 `bson:"total" json:"total"`
	Orders     int     `bson:"orders" json:"orders"`
}

// OrderRepo provides access to the orders collection.
type OrderRepo struct {
	db     *mongo.Database
	client *mongo.Client
	orders *mongo.Collection
}

func NewOrderRepo(db *mongo.Database) *OrderRepo {
	return &OrderRepo{db: db, client: db.Client(), orders: db.Collection("orders")}
}

// CreateAll assigns IDs and inserts all orders in a single transaction, so a
// multi-seller checkout either fully succeeds or leaves no orders behind.
func (r *OrderRepo) CreateAll(ctx context.Context, orders []*models.Order) error {
	if len(orders) == 0 {
		return nil
	}
	_, err := r.CreateCommitted(ctx, orders[0].CustomerID, randomToken(), RequestHash(orders), orders, nil)
	return err
}

// HasDeliveredProduct reports whether the customer has at least one delivered
// order containing the given product. Used to gate reviews to verified buyers.
func (r *OrderRepo) HasDeliveredProduct(ctx context.Context, customerID, productID string) (bool, error) {
	err := r.orders.FindOne(ctx, bson.M{
		"customerId":      customerID,
		"status":          bson.M{"$in": bson.A{models.OrderDelivered, models.OrderCompleted}},
		"items.productId": productID,
	}).Err()
	if isNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
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

// Transition applies the status change only if the seller and current status
// still match. A concurrent status change is reported to the caller.
func (r *OrderRepo) Transition(ctx context.Context, id, sellerID string, from, to models.OrderStatus) (*models.Order, error) {
	now := time.Now().UTC()
	event := models.OrderEvent{Status: to, At: now, ActorID: sellerID}
	var order models.Order
	err := r.orders.FindOneAndUpdate(ctx, bson.M{"_id": id, "sellerId": sellerID, "status": from},
		bson.M{"$set": bson.M{"status": to, "updatedAt": now}, "$push": bson.M{"history": event}},
		options.FindOneAndUpdate().SetReturnDocument(options.After)).Decode(&order)
	if isNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &order, nil
}

// ListByCustomer returns all orders placed by a customer.
func (r *OrderRepo) ListByCustomer(ctx context.Context, customerID string) ([]models.Order, error) {
	return r.listWhere(ctx, "customerId", customerID)
}

// ListBySeller returns all orders directed to a seller.
func (r *OrderRepo) ListBySeller(ctx context.Context, sellerID string) ([]models.Order, error) {
	return r.listWhere(ctx, "sellerId", sellerID)
}

// DeliveredRevenueByDay returns the sum of delivered-order totals for a seller,
// bucketed by local calendar day, over [start, end). Days with no delivered
// orders are absent from the result (the caller backfills zeros).
func (r *OrderRepo) DeliveredRevenueByDay(ctx context.Context, sellerID string, start, end time.Time) ([]DayBucket, error) {
	return r.bucketByDay(ctx, bson.M{
		"sellerId":  sellerID,
		"status":    bson.M{"$in": bson.A{models.OrderDelivered, models.OrderCompleted}},
		"createdAt": bson.M{"$gte": start, "$lt": end},
	}, bson.M{"$sum": "$total"})
}

// ActiveByDay returns the count of a seller's active orders bucketed by local
// calendar day over [start, end).
func (r *OrderRepo) ActiveByDay(ctx context.Context, sellerID string, start, end time.Time) ([]DayBucket, error) {
	return r.bucketByDay(ctx, bson.M{
		"sellerId":  sellerID,
		"status":    bson.M{"$in": models.ActiveOrderStatuses()},
		"createdAt": bson.M{"$gte": start, "$lt": end},
	}, bson.M{"$sum": 1})
}

// bucketByDay groups orders matching filter into local calendar days, summing
// valueExpr (a $sum expression) per day.
func (r *OrderRepo) bucketByDay(ctx context.Context, filter bson.M, valueExpr bson.M) ([]DayBucket, error) {
	cur, err := r.orders.Aggregate(ctx, mongo.Pipeline{
		{{Key: "$match", Value: filter}},
		{{Key: "$group", Value: bson.M{
			"_id":   bson.M{"$dateTrunc": bson.M{"date": "$createdAt", "unit": "day", "timezone": bucketTimezone}},
			"value": valueExpr,
		}}},
	})
	if err != nil {
		return nil, err
	}
	var buckets []DayBucket
	if err := cur.All(ctx, &buckets); err != nil {
		return nil, err
	}
	return buckets, nil
}

// ActiveBreakdown returns the per-status counts of a seller's active orders
// across both fulfillment workflows. Statuses with no orders are absent.
func (r *OrderRepo) ActiveBreakdown(ctx context.Context, sellerID string) (map[models.OrderStatus]int, error) {
	cur, err := r.orders.Aggregate(ctx, mongo.Pipeline{
		{{Key: "$match", Value: bson.M{"sellerId": sellerID, "status": bson.M{"$in": models.ActiveOrderStatuses()}}}},
		{{Key: "$group", Value: bson.M{"_id": "$status", "count": bson.M{"$sum": 1}}}},
	})
	if err != nil {
		return nil, err
	}
	var rows []struct {
		Status models.OrderStatus `bson:"_id"`
		Count  int                `bson:"count"`
	}
	if err := cur.All(ctx, &rows); err != nil {
		return nil, err
	}
	out := make(map[models.OrderStatus]int, len(rows))
	for _, row := range rows {
		out[row.Status] = row.Count
	}
	return out, nil
}

// MonthlySummary aggregates a seller's delivered orders over [start, end) into
// grand totals plus the top spenders. Returns (0, 0, nil, nil) for an empty
// period. TopCustomer.Name is left blank for the caller to resolve.
func (r *OrderRepo) MonthlySummary(ctx context.Context, sellerID string, start, end time.Time) (orders int, revenue float64, top []TopCustomer, err error) {
	cur, err := r.orders.Aggregate(ctx, mongo.Pipeline{
		{{Key: "$match", Value: bson.M{
			"sellerId":  sellerID,
			"status":    bson.M{"$in": bson.A{models.OrderDelivered, models.OrderCompleted}},
			"createdAt": bson.M{"$gte": start, "$lt": end},
		}}},
		{{Key: "$facet", Value: bson.M{
			"totals": bson.A{
				bson.M{"$group": bson.M{"_id": nil, "orders": bson.M{"$sum": 1}, "revenue": bson.M{"$sum": "$total"}}},
			},
			"top": bson.A{
				bson.M{"$group": bson.M{"_id": "$customerId", "total": bson.M{"$sum": "$total"}, "orders": bson.M{"$sum": 1}}},
				bson.M{"$sort": bson.M{"total": -1}},
				bson.M{"$limit": 5},
			},
		}}},
	})
	if err != nil {
		return 0, 0, nil, err
	}
	var rows []struct {
		Totals []struct {
			Orders  int     `bson:"orders"`
			Revenue float64 `bson:"revenue"`
		} `bson:"totals"`
		Top []TopCustomer `bson:"top"`
	}
	if err := cur.All(ctx, &rows); err != nil {
		return 0, 0, nil, err
	}
	if len(rows) == 0 {
		return 0, 0, nil, nil
	}
	if len(rows[0].Totals) > 0 {
		orders = rows[0].Totals[0].Orders
		revenue = rows[0].Totals[0].Revenue
	}
	return orders, revenue, rows[0].Top, nil
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
