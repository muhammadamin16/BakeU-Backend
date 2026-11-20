package models

import "time"

type ProductVariant struct {
	Name  string  `bson:"name" json:"name"`
	Price float64 `bson:"price" json:"price"`
}

// Product is a bakery item listed by a seller, stored in the products
// collection. The document _id is the app-generated product ID.
type Product struct {
	Variants    []ProductVariant `bson:"variants,omitempty" json:"variants,omitempty"`
	ID          string           `bson:"_id" json:"id"`
	SellerID    string           `bson:"sellerId" json:"sellerId"`
	Title       string           `bson:"title" json:"title"`
	Description string           `bson:"description" json:"description"`
	Price       float64          `bson:"price" json:"price"`
	Category    string           `bson:"category" json:"category"`
	ImageURLs   []string         `bson:"imageURLs" json:"imageURLs"`
	Available   bool             `bson:"available" json:"available"`
	RatingAvg   float64          `bson:"ratingAvg" json:"ratingAvg"`
	RatingCount int              `bson:"ratingCount" json:"ratingCount"`
	CreatedAt   time.Time        `bson:"createdAt" json:"createdAt"`
	UpdatedAt   time.Time        `bson:"updatedAt" json:"updatedAt"`
}
