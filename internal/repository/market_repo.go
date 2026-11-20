package repository

import (
	"context"
	"errors"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"bakeu/internal/models"
)

// MarketRepo provides access to the markets collection.
type MarketRepo struct {
	markets *mongo.Collection
}

func NewMarketRepo(db *mongo.Database) *MarketRepo {
	return &MarketRepo{markets: db.Collection("markets")}
}

// Create assigns a new ID and inserts the market.
func (r *MarketRepo) Create(ctx context.Context, m *models.Market) error {
	m.ID = bson.NewObjectID().Hex()
	_, err := r.markets.InsertOne(ctx, m)
	return err
}

// Get loads a market by ID. Returns ErrNotFound if none exists.
func (r *MarketRepo) Get(ctx context.Context, id string) (*models.Market, error) {
	var m models.Market
	if err := r.markets.FindOne(ctx, bson.M{"_id": id}).Decode(&m); err != nil {
		if isNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &m, nil
}

// GetBySeller loads the market owned by a seller. Returns ErrNotFound if the
// seller has not opened a market yet.
func (r *MarketRepo) GetBySeller(ctx context.Context, sellerID string) (*models.Market, error) {
	var m models.Market
	if err := r.markets.FindOne(ctx, bson.M{"sellerId": sellerID}).Decode(&m); err != nil {
		if isNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &m, nil
}

// Update fully overwrites the market document at _id=m.ID.
func (r *MarketRepo) Update(ctx context.Context, m *models.Market) error {
	_, err := r.markets.ReplaceOne(ctx, bson.M{"_id": m.ID}, m)
	return err
}

// ApprovedSellerIDs returns the seller IDs whose market is approved. Used to
// gate which products are visible to customers.
func (r *MarketRepo) ApprovedSellerIDs(ctx context.Context) ([]string, error) {
	cur, err := r.markets.Find(ctx,
		bson.M{"status": models.MarketApproved},
		options.Find().SetProjection(bson.M{"sellerId": 1}),
	)
	if err != nil {
		return nil, err
	}
	var docs []struct {
		SellerID string `bson:"sellerId"`
	}
	if err := cur.All(ctx, &docs); err != nil {
		return nil, err
	}
	ids := make([]string, len(docs))
	for i, d := range docs {
		ids[i] = d.SellerID
	}
	return ids, nil
}

// IsApprovedSeller reports whether the seller has an approved market.
func (r *MarketRepo) IsApprovedSeller(ctx context.Context, sellerID string) (bool, error) {
	m, err := r.GetBySeller(ctx, sellerID)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return m.Status == models.MarketApproved, nil
}

// ListApproved returns approved storefronts in stable ID order.
func (r *MarketRepo) ListApproved(ctx context.Context, after string, limit int) ([]models.Market, error) {
	filter := bson.M{"status": models.MarketApproved}
	if after != "" {
		filter["_id"] = bson.M{"$gt": after}
	}
	cursor, err := r.markets.Find(ctx, filter, options.Find().
		SetSort(bson.D{{Key: "_id", Value: 1}}).SetLimit(int64(limit)))
	if err != nil {
		return nil, err
	}
	markets := make([]models.Market, 0)
	if err := cursor.All(ctx, &markets); err != nil {
		return nil, err
	}
	return markets, nil
}

// EditApproved patches the authenticated seller's approved storefront.
func (r *MarketRepo) EditApproved(ctx context.Context, sellerID string, fields bson.M) (*models.Market, error) {
	fields["updatedAt"] = time.Now().UTC()
	var market models.Market
	err := r.markets.FindOneAndUpdate(ctx,
		bson.M{"sellerId": sellerID, "status": models.MarketApproved},
		bson.M{"$set": fields}, options.FindOneAndUpdate().SetReturnDocument(options.After)).Decode(&market)
	if isNotFound(err) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &market, nil
}
