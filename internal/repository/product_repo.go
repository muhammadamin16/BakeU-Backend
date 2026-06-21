package repository

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"bakeu/internal/models"
)

// ProductRepo provides access to the products collection.
type ProductRepo struct {
	products *mongo.Collection
}

func NewProductRepo(db *mongo.Database) *ProductRepo {
	return &ProductRepo{products: db.Collection("products")}
}

// Create assigns a new ID, stores the product, and returns the ID.
func (r *ProductRepo) Create(ctx context.Context, p *models.Product) (string, error) {
	p.ID = bson.NewObjectID().Hex()
	if _, err := r.products.InsertOne(ctx, p); err != nil {
		return "", err
	}
	return p.ID, nil
}

// Get loads a single product. Returns ErrNotFound if it does not exist.
func (r *ProductRepo) Get(ctx context.Context, id string) (*models.Product, error) {
	var p models.Product
	if err := r.products.FindOne(ctx, bson.M{"_id": id}).Decode(&p); err != nil {
		if isNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if p.ImageURLs == nil {
		p.ImageURLs = []string{}
	}
	return &p, nil
}

// Update fully overwrites the product document at _id=p.ID.
func (r *ProductRepo) Update(ctx context.Context, p *models.Product) error {
	_, err := r.products.ReplaceOne(ctx, bson.M{"_id": p.ID}, p)
	return err
}

// Delete removes a product document.
func (r *ProductRepo) Delete(ctx context.Context, id string) error {
	_, err := r.products.DeleteOne(ctx, bson.M{"_id": id})
	return err
}

// List returns products optionally filtered by category and seller, and by a
// case-insensitive substring match on the title. When restrictToSellers is
// non-nil, only products from those sellers are returned (used to hide products
// of bakeries whose market is not approved); an empty slice yields no products.
func (r *ProductRepo) List(ctx context.Context, category, sellerID, search string, restrictToSellers []string) ([]models.Product, error) {
	filter := bson.M{}
	if category != "" {
		filter["category"] = category
	}
	switch {
	case restrictToSellers != nil && sellerID != "":
		// Intersect the requested seller with the approved set.
		if !contains(restrictToSellers, sellerID) {
			return []models.Product{}, nil
		}
		filter["sellerId"] = sellerID
	case restrictToSellers != nil:
		filter["sellerId"] = bson.M{"$in": restrictToSellers}
	case sellerID != "":
		filter["sellerId"] = sellerID
	}
	if search != "" {
		filter["title"] = bson.M{"$regex": search, "$options": "i"}
	}

	cur, err := r.products.Find(ctx, filter)
	if err != nil {
		return nil, err
	}
	var products []models.Product
	if err := cur.All(ctx, &products); err != nil {
		return nil, err
	}
	for i := range products {
		if products[i].ImageURLs == nil {
			products[i].ImageURLs = []string{}
		}
	}
	return products, nil
}

// contains reports whether s is present in xs.
func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// SetImageURLs updates only the imageURLs (and updatedAt) of a product.
func (r *ProductRepo) SetImageURLs(ctx context.Context, id string, urls []string) error {
	_, err := r.products.UpdateOne(ctx, bson.M{"_id": id}, bson.M{
		"$set": bson.M{
			"imageURLs": urls,
			"updatedAt": time.Now().UTC(),
		},
	})
	return err
}
