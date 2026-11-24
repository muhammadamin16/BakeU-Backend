package repository

import (
	"context"
	"regexp"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

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
	for i := range p.Variants {
		p.Variants[i].ID = bson.NewObjectID().Hex()
	}
	if _, err := r.products.InsertOne(ctx, p); err != nil {
		return "", err
	}
	return p.ID, nil
}

// Get loads a single product. Returns ErrNotFound if it does not exist.
func (r *ProductRepo) Get(ctx context.Context, id string) (*models.Product, error) {
	var p models.Product
	if err := r.products.FindOne(ctx, bson.M{"_id": id, "deletedAt": bson.M{"$exists": false}}).Decode(&p); err != nil {
		if isNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if p.ImageURLs == nil {
		p.ImageURLs = []string{}
	}
	p.ExposeVariantIDs()
	return &p, nil
}

// UpdateDetails patches editable fields while preserving concurrent media and reviews.
func (r *ProductRepo) UpdateDetails(ctx context.Context, p *models.Product, variants *[]models.ProductVariant) (*models.Product, error) {
	fields := bson.M{"title": p.Title, "description": p.Description, "price": p.Price,
		"category": p.Category, "available": p.Available, "updatedAt": p.UpdatedAt}
	if variants != nil {
		fields["variants"] = *variants
	}
	return r.patch(ctx, bson.M{"_id": p.ID, "sellerId": p.SellerID, "deletedAt": bson.M{"$exists": false}}, bson.M{"$set": fields})
}

func (r *ProductRepo) patch(ctx context.Context, filter, update any) (*models.Product, error) {
	var product models.Product
	err := r.products.FindOneAndUpdate(ctx, filter, update,
		options.FindOneAndUpdate().SetReturnDocument(options.After)).Decode(&product)
	if isNotFound(err) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if product.ImageURLs == nil {
		product.ImageURLs = []string{}
	}
	product.ExposeVariantIDs()
	return &product, nil
}

// Archive preserves the document, media and references. The owner predicate
// and active-state predicate also protect against stale/concurrent writes.
func (r *ProductRepo) Archive(ctx context.Context, id, sellerID string) error {
	now := time.Now().UTC()
	result, err := r.products.UpdateOne(ctx,
		bson.M{"_id": id, "sellerId": sellerID, "deletedAt": bson.M{"$exists": false}},
		bson.M{"$set": bson.M{"deletedAt": now, "updatedAt": now, "available": false}})
	if err != nil {
		return err
	}
	if result.MatchedCount != 1 {
		return ErrNotFound
	}
	return nil
}

// List returns products optionally filtered by category and seller, and by a
// case-insensitive substring match on the title. When restrictToSellers is
// non-nil, only products from those sellers are returned (used to hide products
// of bakeries whose market is not approved); an empty slice yields no products.
func (r *ProductRepo) List(ctx context.Context, category, sellerID, search string, restrictToSellers []string) ([]models.Product, error) {
	return r.ListPage(ctx, category, sellerID, search, restrictToSellers, "", 0)
}

func (r *ProductRepo) ListPage(ctx context.Context, category, sellerID, search string, restrictToSellers []string, after string, limit int) ([]models.Product, error) {
	filter := bson.M{"deletedAt": bson.M{"$exists": false}}
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
		// QuoteMeta: the user input is a literal substring, not a pattern — a
		// crafted regex could otherwise burn CPU or match unexpectedly.
		filter["title"] = bson.M{"$regex": regexp.QuoteMeta(search), "$options": "i"}
	}

	find := options.Find()
	if limit > 0 {
		if after != "" {
			filter["_id"] = bson.M{"$gt": after}
		}
		find.SetSort(bson.D{{Key: "_id", Value: 1}}).SetLimit(int64(limit))
	}
	cur, err := r.products.Find(ctx, filter, find)
	if err != nil {
		return nil, err
	}
	var products []models.Product
	if err := cur.All(ctx, &products); err != nil {
		return nil, err
	}
	for i := range products {
		products[i].ExposeVariantIDs()
		if products[i].ImageURLs == nil {
			products[i].ImageURLs = []string{}
		}
	}
	return products, nil
}

// CountBySeller returns the number of products listed by a seller.
func (r *ProductRepo) CountBySeller(ctx context.Context, sellerID string) (int64, error) {
	return r.products.CountDocuments(ctx, bson.M{"sellerId": sellerID, "deletedAt": bson.M{"$exists": false}})
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

// AppendImage checks the cap and appends the image in one update.
func (r *ProductRepo) AppendImage(ctx context.Context, id, sellerID, url string, limit int) (*models.Product, error) {
	filter := bson.M{"_id": id, "sellerId": sellerID, "deletedAt": bson.M{"$exists": false},
		"$expr": bson.M{"$lt": bson.A{
			bson.M{"$size": bson.M{"$ifNull": bson.A{"$imageURLs", bson.A{}}}}, limit,
		}},
	}
	return r.patch(ctx, filter, []bson.M{{"$set": bson.M{
		"updatedAt": time.Now().UTC(),
		"imageURLs": bson.M{"$concatArrays": bson.A{bson.M{"$ifNull": bson.A{"$imageURLs", bson.A{}}}, bson.A{url}}},
	}}})
}
