package models

import (
	"crypto/sha256"
	"encoding/hex"
	"math"
	"time"
)

// MaxProductPrice is shared by product writes and checkout. A listing must
// never accept a price that a one-unit order would reject.
const MaxProductPrice = 1e12

func ValidProductPrice(price float64) bool {
	return price > 0 && price <= MaxProductPrice && !math.IsNaN(price) && !math.IsInf(price, 0)
}

type ProductVariant struct {
	ID    string  `bson:"id" json:"id"`
	Name  string  `bson:"name" json:"name"`
	Price float64 `bson:"price" json:"price"`
}

// ExposeVariantIDs gives older name-only variants deterministic IDs without a
// database write on reads. Sellers preserve these IDs when editing a variant.
func (p *Product) ExposeVariantIDs() {
	for i := range p.Variants {
		if p.Variants[i].ID == "" {
			sum := sha256.Sum256([]byte(p.ID + "\x00" + p.Variants[i].Name))
			p.Variants[i].ID = hex.EncodeToString(sum[:12])
		}
	}
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
	// Archived listings retain their MongoDB document, media and historical IDs.
	DeletedAt *time.Time `bson:"deletedAt,omitempty" json:"-"`
}
