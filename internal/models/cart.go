package models

// CartItem is a single line in a customer's cart.
type CartItem struct {
	VariantID string `bson:"variantId,omitempty" json:"variantId,omitempty"`
	ProductID string `bson:"productId" json:"productId"`
	Qty       int    `bson:"qty" json:"qty"`
}

// Cart is a customer's shopping cart, stored in the carts collection with the
// user ID as the document _id.
type Cart struct {
	UID   string     `bson:"_id" json:"uid"`
	Items []CartItem `bson:"items" json:"items"`
}
