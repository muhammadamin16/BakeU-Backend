package models

import (
	"encoding/json"
	"fmt"
	"math"
	"time"
)

// OrderStatus tracks an order through fulfillment.
type OrderStatus string

const (
	OrderPending        OrderStatus = "pending"
	OrderAccepted       OrderStatus = "accepted"
	OrderReady          OrderStatus = "ready"
	OrderDelivering     OrderStatus = "delivering"
	OrderCompleted      OrderStatus = "completed"
	OrderRejected       OrderStatus = "rejected"
	OrderPreparing      OrderStatus = "preparing"
	OrderOutForDelivery OrderStatus = "out_for_delivery"
	OrderDelivered      OrderStatus = "delivered"
	OrderCancelled      OrderStatus = "cancelled"
)

// ValidOrderStatus reports whether s is a known status.
func ValidOrderStatus(s OrderStatus) bool {
	switch s {
	case OrderPending, OrderAccepted, OrderPreparing, OrderReady, OrderDelivering, OrderOutForDelivery, OrderDelivered, OrderCompleted, OrderCancelled, OrderRejected:
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
	VariantID   string  `bson:"variantId,omitempty" json:"variantId,omitempty"`
	VariantName string  `bson:"variantName,omitempty" json:"variantName,omitempty"`
	ProductID   string  `bson:"productId" json:"productId"`
	Title       string  `bson:"title" json:"title"`
	Price       float64 `bson:"price" json:"price"`
	Qty         int     `bson:"qty" json:"qty"`
}

// Order is a placed order for one seller, stored in the orders collection. A
// checkout containing items from multiple sellers produces one Order per
// seller.
type Order struct {
	Number          string       `bson:"number,omitempty" json:"number"`
	ShopID          string       `bson:"shopId,omitempty" json:"shopId"`
	ShopName        string       `bson:"shopName,omitempty" json:"shopName"`
	BuyerName       string       `bson:"buyerName,omitempty" json:"buyerName"`
	BuyerPhone      string       `bson:"buyerPhone,omitempty" json:"buyerPhone"`
	Fulfillment     string       `bson:"fulfillment,omitempty" json:"fulfillment"`
	Location        *Coordinates `bson:"location,omitempty" json:"location"`
	ScheduledAt     *time.Time   `bson:"scheduledAt,omitempty" json:"scheduledAt"`
	Comment         string       `bson:"comment,omitempty" json:"comment"`
	Version         int          `bson:"version,omitempty" json:"version"`
	WorkflowVersion int          `bson:"workflowVersion,omitempty" json:"workflowVersion"`
	UpdatedAt       *time.Time   `bson:"updatedAt,omitempty" json:"updatedAt"`
	History         []OrderEvent `bson:"history,omitempty" json:"history"`
	Address         string       `bson:"-" json:"address"`

	ID              string      `bson:"_id" json:"id"`
	CustomerID      string      `bson:"customerId" json:"customerId"`
	SellerID        string      `bson:"sellerId" json:"sellerId"`
	Items           []OrderItem `bson:"items" json:"items"`
	Total           float64     `bson:"total" json:"total"`
	Status          OrderStatus `bson:"status" json:"status"`
	DeliveryAddress string      `bson:"deliveryAddress" json:"deliveryAddress"`
	CreatedAt       time.Time   `bson:"createdAt" json:"createdAt"`
}

// Coordinates are an optional pair; zero is a valid coordinate.
type Coordinates struct {
	Latitude  float64 `bson:"latitude" json:"latitude"`
	Longitude float64 `bson:"longitude" json:"longitude"`
}
type OrderEvent struct {
	Status  OrderStatus `bson:"status" json:"status"`
	At      time.Time   `bson:"at" json:"at"`
	ActorID string      `bson:"actorId" json:"actorId"`
	Version int         `bson:"version" json:"version"`
}

// V2 transitions are acyclic and distinct for pickup. The old graph remains for legacy clients/orders.
func CanTransitionV2(from, to OrderStatus, fulfillment string) bool {
	switch from {
	case OrderPending:
		return to == OrderAccepted || to == OrderRejected
	case OrderAccepted:
		return to == OrderPreparing || to == OrderCancelled
	case OrderPreparing:
		return to == OrderReady || to == OrderCancelled
	case OrderReady:
		if to == OrderCancelled {
			return true
		}
		if fulfillment == "pickup" {
			return to == OrderCompleted
		}
		return to == OrderDelivering
	case OrderDelivering, OrderOutForDelivery:
		return to == OrderDelivered
	case OrderDelivered:
		return to == OrderCompleted
	}
	return false
}
func (o *Order) Project() {
	o.Address = o.DeliveryAddress
	if o.Number == "" {
		o.Number = "BU-" + o.ID
	}
	if o.Fulfillment == "" {
		o.Fulfillment = "delivery"
	}
	if o.Status == OrderDelivered && o.WorkflowVersion < 2 {
		o.Status = OrderCompleted
	}
	if o.Status == OrderOutForDelivery {
		o.Status = OrderDelivering
	}
	if o.Items == nil {
		o.Items = []OrderItem{}
	}
	if o.History == nil {
		o.History = []OrderEvent{}
	}
}

func (c *Coordinates) UnmarshalJSON(data []byte) error {
	var pair struct {
		Latitude  *float64 `json:"latitude"`
		Longitude *float64 `json:"longitude"`
	}
	if e := json.Unmarshal(data, &pair); e != nil {
		return e
	}
	if pair.Latitude == nil || pair.Longitude == nil {
		return fmt.Errorf("latitude and longitude are both required")
	}
	if math.IsNaN(*pair.Latitude) || math.IsNaN(*pair.Longitude) || math.IsInf(*pair.Latitude, 0) || math.IsInf(*pair.Longitude, 0) || math.Abs(*pair.Latitude) > 90 || math.Abs(*pair.Longitude) > 180 {
		return fmt.Errorf("invalid coordinates")
	}
	c.Latitude = *pair.Latitude
	c.Longitude = *pair.Longitude
	return nil
}
