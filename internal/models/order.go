package models

import "time"

// OrderStatus tracks an order through fulfillment.
type OrderStatus string

const (
	OrderPending        OrderStatus = "pending"
	OrderPreparing      OrderStatus = "preparing"
	OrderOutForDelivery OrderStatus = "out_for_delivery"
	OrderDelivered      OrderStatus = "delivered"
	OrderCancelled      OrderStatus = "cancelled"
)

// ValidOrderStatus reports whether s is a known status.
func ValidOrderStatus(s OrderStatus) bool {
	switch s {
	case OrderPending, OrderPreparing, OrderOutForDelivery, OrderDelivered, OrderCancelled:
		return true
	default:
		return false
	}
}

// orderTransitions lists the statuses an order may move to from each status.
// Delivered and cancelled are terminal: once reached, an order cannot change
// (re-flagging delivered orders would also distort dashboard revenue).
var orderTransitions = map[OrderStatus][]OrderStatus{
	OrderPending:        {OrderPreparing, OrderCancelled},
	OrderPreparing:      {OrderOutForDelivery, OrderDelivered, OrderCancelled},
	OrderOutForDelivery: {OrderDelivered, OrderCancelled},
}

// CanTransition reports whether an order may move from one status to another.
func CanTransition(from, to OrderStatus) bool {
	for _, s := range orderTransitions[from] {
		if s == to {
			return true
		}
	}
	return false
}

// OrderItem is a snapshot of a product at the time of ordering (title/price are
// copied so later product edits don't change historical orders).
type OrderItem struct {
	ProductID string  `bson:"productId" json:"productId"`
	Title     string  `bson:"title" json:"title"`
	Price     float64 `bson:"price" json:"price"`
	Qty       int     `bson:"qty" json:"qty"`
}

type OrderEvent struct {
	Status  OrderStatus `bson:"status" json:"status"`
	At      time.Time   `bson:"at" json:"at"`
	ActorID string      `bson:"actorId" json:"actorId"`
}

// Order is a placed order for one seller, stored in the orders collection. A
// checkout containing items from multiple sellers produces one Order per
// seller.
type Order struct {
	History         []OrderEvent `bson:"history,omitempty" json:"history,omitempty"`
	UpdatedAt       *time.Time   `bson:"updatedAt,omitempty" json:"updatedAt,omitempty"`
	ID              string       `bson:"_id" json:"id"`
	CustomerID      string       `bson:"customerId" json:"customerId"`
	SellerID        string       `bson:"sellerId" json:"sellerId"`
	Items           []OrderItem  `bson:"items" json:"items"`
	Total           float64      `bson:"total" json:"total"`
	Status          OrderStatus  `bson:"status" json:"status"`
	DeliveryAddress string       `bson:"deliveryAddress" json:"deliveryAddress"`
	CreatedAt       time.Time    `bson:"createdAt" json:"createdAt"`
}
