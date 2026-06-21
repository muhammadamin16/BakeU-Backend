package models

import "time"

// MarketStatus tracks a bakery market through the approval pipeline.
type MarketStatus string

const (
	MarketPending  MarketStatus = "pending"
	MarketApproved MarketStatus = "approved"
	MarketRejected MarketStatus = "rejected"
)

// Valid reports whether s is a known market status.
func (s MarketStatus) Valid() bool {
	return s == MarketPending || s == MarketApproved || s == MarketRejected
}

// Fulfillment describes how a bakery hands orders to customers.
type Fulfillment string

const (
	FulfillmentPickup   Fulfillment = "pickup"
	FulfillmentDelivery Fulfillment = "delivery"
	FulfillmentBoth     Fulfillment = "both"
)

// Valid reports whether f is a known fulfillment option.
func (f Fulfillment) Valid() bool {
	return f == FulfillmentPickup || f == FulfillmentDelivery || f == FulfillmentBoth
}

// Market is a seller's bakery storefront, stored in the markets collection. The
// document _id is an app-generated ID; SellerID is the owning user and is
// unique (one market per seller). A market must be MarketApproved before its
// products are visible to customers.
type Market struct {
	ID              string       `bson:"_id" json:"id"`
	SellerID        string       `bson:"sellerId" json:"sellerId"`
	Name            string       `bson:"name" json:"name"`
	Description     string       `bson:"description" json:"description"`
	Phone           string       `bson:"phone" json:"phone"`
	Email           string       `bson:"email" json:"email"`
	City            string       `bson:"city" json:"city"`
	Area            string       `bson:"area" json:"area"`
	Fulfillment     Fulfillment  `bson:"fulfillment" json:"fulfillment"`
	LogoURL         string       `bson:"logoURL" json:"logoURL"`
	Status          MarketStatus `bson:"status" json:"status"`
	RejectionReason string       `bson:"rejectionReason" json:"rejectionReason"`
	CreatedAt       time.Time    `bson:"createdAt" json:"createdAt"`
	UpdatedAt       time.Time    `bson:"updatedAt" json:"updatedAt"`
	ReviewedAt      time.Time    `bson:"reviewedAt,omitempty" json:"reviewedAt,omitempty"`
}
