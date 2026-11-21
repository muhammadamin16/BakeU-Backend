package repository

import (
	"context"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

type Favorites struct {
	UID        string   `bson:"_id" json:"uid"`
	ProductIDs []string `bson:"productIds" json:"productIds"`
	Revision   string   `bson:"revision" json:"revision"`
}
type FavoritesRepo struct{ collection *mongo.Collection }

func NewFavoritesRepo(db *mongo.Database) *FavoritesRepo {
	return &FavoritesRepo{collection: db.Collection("favorites")}
}
func (r *FavoritesRepo) Get(ctx context.Context, uid string) (*Favorites, error) {
	out := &Favorites{UID: uid, ProductIDs: []string{}}
	err := r.collection.FindOne(ctx, bson.M{"_id": uid}).Decode(out)
	if isNotFound(err) {
		return out, nil
	}
	if out.ProductIDs == nil {
		out.ProductIDs = []string{}
	}
	return out, err
}
func (r *FavoritesRepo) Change(ctx context.Context, uid string, expected *string, edit func(*Favorites) error) (*Favorites, error) {
	for range 12 {
		out, err := r.Get(ctx, uid)
		if err != nil {
			return nil, err
		}
		if expected != nil && out.Revision != *expected {
			return nil, ErrConflict
		}
		filter := bson.M{"_id": uid, "revision": out.Revision}
		if out.Revision == "" {
			delete(filter, "revision")
			filter["$or"] = bson.A{bson.M{"revision": ""}, bson.M{"revision": bson.M{"$exists": false}}}
		}
		if err = edit(out); err != nil {
			return nil, err
		}
		insert := out.Revision == ""
		out.Revision = bson.NewObjectID().Hex()
		result, err := r.collection.ReplaceOne(ctx, filter, out, options.Replace().SetUpsert(insert))
		if isDuplicate(err) || (err == nil && result.MatchedCount == 0 && result.UpsertedCount == 0) {
			continue
		}
		if err != nil {
			return nil, err
		}
		return out, nil
	}
	return nil, ErrConflict
}
