package models

import "time"

// Review is a customer's rating + comment on a product, stored in the reviews
// collection. ProductID links the review to its product (a top-level
// collection, not a subcollection).
type Review struct {
	ID         string    `bson:"_id" json:"id"`
	ProductID  string    `bson:"productId" json:"productId"`
	SellerID   string    `bson:"sellerId" json:"sellerId"`
	CustomerID string    `bson:"customerId" json:"customerId"`
	Rating     int       `bson:"rating" json:"rating"`
	Comment    string    `bson:"comment" json:"comment"`
	CreatedAt  time.Time `bson:"createdAt" json:"createdAt"`
}
