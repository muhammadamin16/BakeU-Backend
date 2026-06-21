package repository

import (
	"context"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"bakeu/internal/models"
)

// ReviewRepo manages product reviews and keeps the parent product's aggregate
// rating in sync.
type ReviewRepo struct {
	client   *mongo.Client
	reviews  *mongo.Collection
	products *mongo.Collection
}

func NewReviewRepo(db *mongo.Database) *ReviewRepo {
	return &ReviewRepo{
		client:   db.Client(),
		reviews:  db.Collection("reviews"),
		products: db.Collection("products"),
	}
}

// Create inserts a review and atomically updates the product's ratingAvg and
// ratingCount in a single transaction. Returns ErrNotFound if the product does
// not exist.
func (r *ReviewRepo) Create(ctx context.Context, productID string, rev *models.Review) (string, error) {
	rev.ID = bson.NewObjectID().Hex()
	rev.ProductID = productID

	session, err := r.client.StartSession()
	if err != nil {
		return "", err
	}
	defer session.EndSession(ctx)

	_, err = session.WithTransaction(ctx, func(ctx context.Context) (any, error) {
		// Read the product first so we can recompute the running average.
		var p models.Product
		err := r.products.FindOne(ctx, bson.M{"_id": productID}).Decode(&p)
		if err != nil {
			if isNotFound(err) {
				return nil, ErrNotFound
			}
			return nil, err
		}

		newCount := p.RatingCount + 1
		newAvg := (p.RatingAvg*float64(p.RatingCount) + float64(rev.Rating)) / float64(newCount)

		if _, err := r.reviews.InsertOne(ctx, rev); err != nil {
			return nil, err
		}
		_, err = r.products.UpdateOne(ctx, bson.M{"_id": productID}, bson.M{
			"$set": bson.M{"ratingCount": newCount, "ratingAvg": newAvg},
		})
		return nil, err
	})
	if err != nil {
		return "", err
	}
	return rev.ID, nil
}

// ListByProduct returns all reviews for a product.
func (r *ReviewRepo) ListByProduct(ctx context.Context, productID string) ([]models.Review, error) {
	cur, err := r.reviews.Find(ctx, bson.M{"productId": productID})
	if err != nil {
		return nil, err
	}
	var reviews []models.Review
	if err := cur.All(ctx, &reviews); err != nil {
		return nil, err
	}
	return reviews, nil
}
