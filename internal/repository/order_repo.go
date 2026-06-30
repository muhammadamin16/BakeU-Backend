package repository

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"bakeu/internal/models"
)

// bucketTimezone is the offset used to bucket orders into local calendar days
// for the dashboard charts (Asia/Tashkent, UTC+5, no DST).
const bucketTimezone = "+05:00"

// activeStatuses are the order statuses that still require seller action.
var activeStatuses = bson.A{
	models.OrderPending,
	models.OrderPreparing,
	models.OrderOutForDelivery,
}

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

// DeliveredRevenueByDay returns the sum of delivered-order totals for a seller,
// bucketed by local calendar day, over [start, end). Days with no delivered
// orders are absent from the result (the caller backfills zeros).
func (r *OrderRepo) DeliveredRevenueByDay(ctx context.Context, sellerID string, start, end time.Time) ([]DayBucket, error) {
	return r.bucketByDay(ctx, bson.M{
		"sellerId":  sellerID,
		"status":    models.OrderDelivered,
		"createdAt": bson.M{"$gte": start, "$lt": end},
	}, bson.M{"$sum": "$total"})
}

// ActiveByDay returns the count of a seller's active orders bucketed by local
// calendar day over [start, end).
func (r *OrderRepo) ActiveByDay(ctx context.Context, sellerID string, start, end time.Time) ([]DayBucket, error) {
	return r.bucketByDay(ctx, bson.M{
		"sellerId":  sellerID,
		"status":    bson.M{"$in": activeStatuses},
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
// (pending / preparing / out_for_delivery). Statuses with no orders are absent.
func (r *OrderRepo) ActiveBreakdown(ctx context.Context, sellerID string) (map[models.OrderStatus]int, error) {
	cur, err := r.orders.Aggregate(ctx, mongo.Pipeline{
		{{Key: "$match", Value: bson.M{"sellerId": sellerID, "status": bson.M{"$in": activeStatuses}}}},
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
			"status":    models.OrderDelivered,
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
