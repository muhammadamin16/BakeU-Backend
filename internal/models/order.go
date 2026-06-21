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

// OrderItem is a snapshot of a product at the time of ordering (title/price are
// copied so later product edits don't change historical orders).
type OrderItem struct {
	ProductID string  `bson:"productId" json:"productId"`
	Title     string  `bson:"title" json:"title"`
	Price     float64 `bson:"price" json:"price"`
	Qty       int     `bson:"qty" json:"qty"`
}

// Order is a placed order for one seller, stored in the orders collection. A
// checkout containing items from multiple sellers produces one Order per
// seller.
type Order struct {
	ID              string      `bson:"_id" json:"id"`
	CustomerID      string      `bson:"customerId" json:"customerId"`
	SellerID        string      `bson:"sellerId" json:"sellerId"`
	Items           []OrderItem `bson:"items" json:"items"`
	Total           float64     `bson:"total" json:"total"`
	Status          OrderStatus `bson:"status" json:"status"`
	DeliveryAddress string      `bson:"deliveryAddress" json:"deliveryAddress"`
	CreatedAt       time.Time   `bson:"createdAt" json:"createdAt"`
}
