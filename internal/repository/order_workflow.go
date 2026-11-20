package repository

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"bakeu/internal/models"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

type orderOperation struct {
	ID        string         `bson:"_id"`
	Hash      string         `bson:"hash"`
	Orders    []models.Order `bson:"orders"`
	CreatedAt time.Time      `bson:"createdAt"`
}

func RequestHash(body any) string { b, _ := json.Marshal(body); return digest(string(b)) }
func (r *OrderRepo) priorOperation(ctx context.Context, id, hash string) ([]models.Order, error) {
	var op orderOperation
	e := r.db.Collection("order_operations").FindOne(ctx, bson.M{"_id": id}).Decode(&op)
	if isNotFound(e) {
		return nil, ErrNotFound
	}
	if e != nil {
		return nil, e
	}
	if op.Hash != hash {
		return nil, ErrConflict
	}
	for i := range op.Orders {
		op.Orders[i].Project()
	}
	return op.Orders, nil
}
func (r *OrderRepo) ReplayCheckout(ctx context.Context, uid, key, hash string) ([]models.Order, error) {
	return r.priorOperation(ctx, digest("checkout:"+uid+":"+key), hash)
}
func (r *OrderRepo) CreateCommitted(ctx context.Context, uid, key, hash string, orders []*models.Order) ([]models.Order, error) {
	opID := digest("checkout:" + uid + ":" + key)
	if out, e := r.priorOperation(ctx, opID, hash); e == nil {
		return out, nil
	} else if !errors.Is(e, ErrNotFound) {
		return nil, e
	}
	for _, o := range orders {
		if o.ID == "" {
			o.ID = bson.NewObjectID().Hex()
		}
		o.Number = "BU-" + o.ID
		o.Version = 1
		o.UpdatedAt = &o.CreatedAt
		o.History = []models.OrderEvent{{Status: models.OrderPending, At: o.CreatedAt, ActorID: uid, Version: 1}}
	}
	session, e := r.client.StartSession()
	if e != nil {
		return nil, e
	}
	defer session.EndSession(ctx)
	var out []models.Order
	_, e = session.WithTransaction(ctx, func(ctx context.Context) (any, error) {
		if previous, e := r.priorOperation(ctx, opID, hash); e == nil {
			out = previous
			return nil, nil
		} else if !errors.Is(e, ErrNotFound) {
			return nil, e
		}
		out = []models.Order{}
		for _, o := range orders {
			if _, e := r.orders.InsertOne(ctx, o); e != nil {
				return nil, e
			}
			if e := enqueueOrderEvents(ctx, r.db, o, "order.created"); e != nil {
				return nil, e
			}
			out = append(out, *o)
		}
		if _, e := r.db.Collection("carts").DeleteOne(ctx, bson.M{"_id": uid}); e != nil {
			return nil, e
		}
		_, e := r.db.Collection("order_operations").InsertOne(ctx, orderOperation{ID: opID, Hash: hash, Orders: out, CreatedAt: time.Now().UTC()})
		return nil, e
	})
	if isDuplicate(e) {
		return r.priorOperation(ctx, opID, hash)
	}
	if e != nil {
		return nil, e
	}
	for i := range out {
		out[i].Project()
	}
	return out, nil
}
func (r *OrderRepo) Hydrate(ctx context.Context, o *models.Order) error {
	if o.ShopID == "" {
		m, e := NewMarketRepo(r.db).GetBySeller(ctx, o.SellerID)
		if e != nil {
			return e
		}
		o.ShopID = m.ID
		o.ShopName = m.Name
	}
	if o.BuyerName == "" || o.BuyerPhone == "" {
		u, e := NewUserRepo(r.db).Get(ctx, o.CustomerID)
		if e != nil {
			return e
		}
		o.BuyerName = u.Name
		o.BuyerPhone = u.Phone
	}
	o.Project()
	return nil
}

// TransitionCommitted serializes status, history, inbox/outbox and idempotency response in one transaction.
func (r *OrderRepo) TransitionCommitted(ctx context.Context, id, sellerID string, expected, to models.OrderStatus, key string, expectedVersion *int) (*models.Order, error) {
	opID := digest("status:" + sellerID + ":" + id + ":" + key)
	hash := RequestHash(struct {
		From, To models.OrderStatus
		Version  *int
	}{expected, to, expectedVersion})
	if previous, e := r.priorOperation(ctx, opID, hash); e == nil {
		return &previous[0], nil
	} else if !errors.Is(e, ErrNotFound) {
		return nil, e
	}
	session, e := r.client.StartSession()
	if e != nil {
		return nil, e
	}
	defer session.EndSession(ctx)
	var changed *models.Order
	_, e = session.WithTransaction(ctx, func(ctx context.Context) (any, error) {
		if prev, e := r.priorOperation(ctx, opID, hash); e == nil {
			changed = &prev[0]
			return nil, nil
		} else if !errors.Is(e, ErrNotFound) {
			return nil, e
		}
		o, e := r.Get(ctx, id)
		if e != nil {
			return nil, e
		}
		if o.SellerID != sellerID {
			return nil, ErrForbidden
		}
		rawStatus := o.Status
		if e = r.Hydrate(ctx, o); e != nil {
			return nil, e
		}
		if o.Status != expected || (expectedVersion != nil && o.Version != *expectedVersion) || !models.CanTransitionV2(o.Status, to, o.Fulfillment) {
			return nil, ErrConflict
		}
		now := time.Now().UTC()
		o.Status = to
		o.Version++
		o.WorkflowVersion = 2
		o.UpdatedAt = &now
		event := models.OrderEvent{Status: to, At: now, ActorID: sellerID, Version: o.Version}
		o.History = append(o.History, event)
		filter := bson.M{"_id": id, "sellerId": sellerID, "status": rawStatus}
		if expectedVersion != nil {
			filter["version"] = *expectedVersion
		}
		result, e := r.orders.UpdateOne(ctx, filter, bson.M{"$set": bson.M{"status": to, "version": o.Version, "workflowVersion": 2, "updatedAt": now, "shopId": o.ShopID, "shopName": o.ShopName, "buyerName": o.BuyerName, "buyerPhone": o.BuyerPhone, "fulfillment": o.Fulfillment}, "$push": bson.M{"history": event}})
		if e != nil {
			return nil, e
		}
		if result.MatchedCount != 1 {
			return nil, ErrConflict
		}
		if e = enqueueOrderEvents(ctx, r.db, o, "order.status_changed"); e != nil {
			return nil, e
		}
		_, e = r.db.Collection("order_operations").InsertOne(ctx, orderOperation{ID: opID, Hash: hash, Orders: []models.Order{*o}, CreatedAt: now})
		changed = o
		return nil, e
	})
	if isDuplicate(e) {
		previous, err := r.priorOperation(ctx, opID, hash)
		if err != nil {
			return nil, err
		}
		return &previous[0], nil
	}
	return changed, e
}
func (r *OrderRepo) Page(ctx context.Context, uid string, seller bool, view string, page int) ([]models.Order, *int, error) {
	field := "customerId"
	if seller {
		field = "sellerId"
	}
	filter := bson.M{field: uid}
	terminal := bson.A{models.OrderCompleted, models.OrderCancelled, models.OrderRejected}
	switch view {
	case "all":
	case "active":
		filter["status"] = bson.M{"$nin": terminal}
	case "history":
		filter["status"] = bson.M{"$in": terminal}
	default:
		status := models.OrderStatus(view)
		if !models.ValidOrderStatus(status) {
			return nil, nil, ErrForbidden
		}
		if status == models.OrderDelivering {
			filter["status"] = bson.M{"$in": bson.A{models.OrderDelivering, models.OrderOutForDelivery}}
		} else {
			filter["status"] = status
		}
	}
	cur, e := r.orders.Find(ctx, filter, options.Find().SetSort(bson.D{{Key: "createdAt", Value: -1}, {Key: "_id", Value: -1}}).SetSkip(int64(page*5)).SetLimit(6))
	if e != nil {
		return nil, nil, e
	}
	out := []models.Order{}
	if e = cur.All(ctx, &out); e != nil {
		return nil, nil, e
	}
	var next *int
	if len(out) > 5 {
		v := page + 1
		next = &v
		out = out[:5]
	}
	for i := range out {
		if e = r.Hydrate(ctx, &out[i]); e != nil {
			return nil, nil, e
		}
	}
	return out, next, nil
}
